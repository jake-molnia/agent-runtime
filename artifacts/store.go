// Package artifacts stores session exports separately from Hatchet task payloads.
package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type Store interface {
	Put(context.Context, string, io.Reader) (string, error)
}
type Directory struct {
	Root     string
	MaxBytes int64
}

func (d Directory) Put(ctx context.Context, key string, source io.Reader) (string, error) {
	if d.Root == "" {
		return "", errors.New("artifact directory required")
	}
	limit := d.MaxBytes
	if limit == 0 {
		limit = 256 << 20
	}
	if limit < 1 {
		return "", errors.New("invalid artifact limit")
	}
	if err := os.MkdirAll(d.Root, 0700); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(key))
	name := hex.EncodeToString(sum[:]) + ".json"
	file, err := os.CreateTemp(d.Root, ".export-")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	n, err := io.Copy(file, io.LimitReader(contextReader{ctx, source}, limit+1))
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", errors.New("session export exceeds artifact limit")
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(file.Name(), filepath.Join(d.Root, name)); err != nil {
		return "", err
	}
	dir, err := os.Open(d.Root)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return "", err
	}
	return name, nil
}

type contextReader struct {
	context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
