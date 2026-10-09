package messages

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var (
	ErrNotFound  = errors.New("message not found")
	ErrConflict  = errors.New("immutable message conflict")
	ErrIntegrity = errors.New("message integrity check failed")
)

type Store interface {
	Put(context.Context, string, Message) (Reference, error)
	Get(context.Context, string, Reference) (Message, error)
	Once(context.Context, string, string, func() (Message, error)) (Reference, error)
	PutAttachment(context.Context, string, string, []byte) (Attachment, error)
	GetAttachment(context.Context, string, Attachment) ([]byte, error)
}

type Directory struct {
	Root string
}

func (store Directory) directory(ctx context.Context, scope, kind string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if store.Root == "" || !validName(scope) {
		return "", errors.New("message store requires a root and trusted scope")
	}
	path := filepath.Join(store.Root, digest([]byte(scope)), kind)
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", err
	}
	for _, parent := range []string{filepath.Dir(store.Root), store.Root, filepath.Dir(path), path} {
		if err := syncDirectory(parent); err != nil {
			return "", err
		}
	}
	return path, nil
}

func readBounded(ctx context.Context, path string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("stored content exceeds size limit")
	}
	if err == nil {
		err = ctx.Err()
	}
	return data, err
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func publish(ctx context.Context, directory, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(directory, name)
	if err = os.Link(file.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, readErr := readBounded(ctx, path, int64(len(data)))
		if readErr != nil || !bytes.Equal(existing, data) {
			return ErrConflict
		}
	}
	return syncDirectory(directory)
}

func (store Directory) Put(ctx context.Context, scope string, message Message) (Reference, error) {
	data, err := Encode(message)
	if err != nil {
		return Reference{}, err
	}
	directory, err := store.directory(ctx, scope, "messages")
	if err != nil {
		return Reference{}, err
	}
	ref := Reference{ID: message.ID, Digest: digest(data)}
	record := append([]byte(ref.Digest+"\n"), data...)
	return ref, publish(ctx, directory, digest([]byte(message.ID)), record)
}

func (store Directory) lookup(ctx context.Context, scope, id string) (Message, Reference, error) {
	if !validName(id) {
		return Message{}, Reference{}, errors.New("invalid message ID")
	}
	directory, err := store.directory(ctx, scope, "messages")
	if err != nil {
		return Message{}, Reference{}, err
	}
	record, err := readBounded(ctx, filepath.Join(directory, digest([]byte(id))), MaxMessageBytes+65)
	if err != nil {
		return Message{}, Reference{}, err
	}
	if len(record) < 65 || record[64] != '\n' {
		return Message{}, Reference{}, ErrIntegrity
	}
	data := record[65:]
	expected := string(record[:64])
	if !validDigest(expected) || digest(data) != expected {
		return Message{}, Reference{}, ErrIntegrity
	}
	message, err := Decode(data)
	if err != nil || message.ID != id {
		return Message{}, Reference{}, ErrIntegrity
	}
	return message, Reference{ID: id, Digest: expected}, nil
}

func (store Directory) Get(ctx context.Context, scope string, ref Reference) (Message, error) {
	if err := ref.Validate(); err != nil {
		return Message{}, err
	}
	message, actual, err := store.lookup(ctx, scope, ref.ID)
	if err == nil && ref != actual {
		err = ErrIntegrity
	}
	return message, err
}

func (store Directory) Once(ctx context.Context, scope, id string, create func() (Message, error)) (Reference, error) {
	if !validName(id) || create == nil {
		return Reference{}, errors.New("invalid message execution")
	}
	directory, err := store.directory(ctx, scope, "locks")
	if err != nil {
		return Reference{}, err
	}
	lock, err := os.OpenFile(filepath.Join(directory, digest([]byte(id))), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Reference{}, err
	}
	defer lock.Close()
	for {
		if err = ctx.Err(); err != nil {
			return Reference{}, err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return Reference{}, err
		}
		select {
		case <-ctx.Done():
			return Reference{}, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_, ref, err := store.lookup(ctx, scope, id)
	if err == nil {
		return ref, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Reference{}, err
	}
	message, err := create()
	if err != nil {
		return Reference{}, err
	}
	if message.ID != id {
		return Reference{}, errors.New("execution returned an unexpected message ID")
	}
	return store.Put(ctx, scope, message)
}

func (store Directory) PutAttachment(ctx context.Context, scope, mediaType string, data []byte) (Attachment, error) {
	if !validName(mediaType) || len(data) > MaxAttachmentBytes {
		return Attachment{}, errors.New("invalid attachment or size limit exceeded")
	}
	directory, err := store.directory(ctx, scope, "attachments")
	if err != nil {
		return Attachment{}, err
	}
	ref := Attachment{Reference: Reference{ID: digest(data), Digest: digest(data)}, Size: int64(len(data)), MediaType: mediaType}
	return ref, publish(ctx, directory, ref.ID, data)
}

func (store Directory) GetAttachment(ctx context.Context, scope string, attachment Attachment) ([]byte, error) {
	if err := attachment.Reference.Validate(); err != nil {
		return nil, err
	}
	if attachment.ID != attachment.Digest || attachment.Size < 0 || attachment.Size > MaxAttachmentBytes || !validName(attachment.MediaType) {
		return nil, errors.New("invalid attachment reference")
	}
	directory, err := store.directory(ctx, scope, "attachments")
	if err != nil {
		return nil, err
	}
	data, err := readBounded(ctx, filepath.Join(directory, attachment.ID), attachment.Size)
	if err == nil && (int64(len(data)) != attachment.Size || digest(data) != attachment.Digest) {
		err = ErrIntegrity
	}
	return data, err
}
