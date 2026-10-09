package notebooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

const MaxNotebookBytes = 64 << 10

const maxRecordBytes = 3 << 20
const maxPayloadBytes = 1 << 20

var (
	ErrConflict  = errors.New("notebook revision conflict")
	ErrIntegrity = errors.New("notebook record integrity failure")
	identifier   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

type Directory struct{ Root string }

type Session struct {
	RunID    string          `json:"run_id"`
	Digest   string          `json:"digest"`
	Base     uint64          `json:"base"`
	Notebook string          `json:"notebook"`
	Input    json.RawMessage `json:"input"`
}

type Record struct {
	Revision uint64          `json:"revision"`
	RunID    string          `json:"run_id"`
	Digest   string          `json:"digest"`
	Notebook string          `json:"notebook"`
	Output   json.RawMessage `json:"output"`
}

func validKey(key string) bool {
	if len(key) == 0 || len(key) > 256 {
		return false
	}
	for _, part := range strings.Split(key, "/") {
		if !identifier.MatchString(part) {
			return false
		}
	}
	return true
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func secureMkdirAll(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), current) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrIntegrity
		}
	}
	return nil
}

func syncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (store Directory) directory(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if store.Root == "" || !validKey(key) {
		return "", errors.New("invalid notebook root or task key")
	}
	path := filepath.Join(store.Root, hash(key))
	if err := secureMkdirAll(path); err != nil {
		return "", err
	}
	if err := syncDirectory(store.Root); err != nil {
		return "", err
	}
	return path, nil
}

func readFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxRecordBytes {
		return nil, ErrIntegrity
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, ErrIntegrity
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRecordBytes {
		return nil, ErrIntegrity
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var sealed struct {
		Checksum string          `json:"checksum"`
		Data     json.RawMessage `json:"data"`
	}
	if decode(data, &sealed) != nil || len(sealed.Checksum) != 64 || !json.Valid(sealed.Data) {
		return nil, ErrIntegrity
	}
	checksum := sha256.Sum256(sealed.Data)
	if sealed.Checksum != hex.EncodeToString(checksum[:]) {
		return nil, ErrIntegrity
	}
	return sealed.Data, nil
}

func decode(data []byte, result any) error {
	reader := bytes.NewReader(data)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return ErrIntegrity
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrIntegrity
	}
	return nil
}

func writeRecord(ctx context.Context, directory, name string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	checksum := sha256.Sum256(payload)
	data, err := json.Marshal(struct {
		Checksum string          `json:"checksum"`
		Data     json.RawMessage `json:"data"`
	}{hex.EncodeToString(checksum[:]), payload})
	if err != nil {
		return err
	}
	if len(data) > maxRecordBytes {
		return errors.New("notebook record too large")
	}
	file, err := os.CreateTemp(directory, ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(file.Name(), filepath.Join(directory, name)); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func withLock(ctx context.Context, path string, action func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lockPath := filepath.Join(path, ".lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return ErrIntegrity
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			syscall.Nanosleep(&syscall.Timespec{Nsec: 10_000_000}, nil)
		}
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return action()
}

func history(ctx context.Context, directory string) ([]Record, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	commits := make([]Record, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if name == ".lock" || strings.HasPrefix(name, ".tmp-") {
			continue
		}
		if strings.HasSuffix(name, ".begin") {
			if len(name) != 64+len(".begin") || !entry.Type().IsRegular() {
				return nil, ErrIntegrity
			}
			continue
		}
		if !strings.HasSuffix(name, ".commit") || len(name) != 64+len(".commit") || entry.Type()&os.ModeSymlink != 0 {
			return nil, ErrIntegrity
		}
		data, err := readFile(ctx, filepath.Join(directory, name))
		if err != nil {
			return nil, err
		}
		var record Record
		if decode(data, &record) != nil || record.Revision == 0 || !identifier.MatchString(record.RunID) || !identifier.MatchString(record.Digest) || len(record.Notebook) > MaxNotebookBytes || !json.Valid(record.Output) || name != hash(record.RunID)+".commit" {
			return nil, ErrIntegrity
		}
		commits = append(commits, record)
	}
	sort.Slice(commits, func(left, right int) bool { return commits[left].Revision < commits[right].Revision })
	for index, record := range commits {
		if record.Revision != uint64(index+1) {
			return nil, ErrIntegrity
		}
	}
	return commits, nil
}

func latest(ctx context.Context, directory string) (Record, error) {
	records, err := history(ctx, directory)
	if err != nil {
		return Record{}, err
	}
	if len(records) == 0 {
		return Record{}, nil
	}
	return records[len(records)-1], nil
}

func validBase(session Session, history []Record) bool {
	if session.Base > uint64(len(history)) {
		return false
	}
	if session.Base == 0 {
		return session.Notebook == ""
	}
	return session.Notebook == history[session.Base-1].Notebook
}

func (store Directory) Latest(ctx context.Context, key string) (Record, error) {
	path, err := store.directory(ctx, key)
	if err != nil {
		return Record{}, err
	}
	return latest(ctx, path)
}

func compact(data json.RawMessage) (json.RawMessage, error) {
	if len(data) == 0 || len(data) > maxPayloadBytes || !json.Valid(data) {
		return nil, errors.New("bounded JSON required")
	}
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, data); err != nil {
		return nil, err
	}
	return json.RawMessage(buffer.Bytes()), nil
}

func (store Directory) Begin(ctx context.Context, key, runID, digest string, input json.RawMessage) (Session, error) {
	if !identifier.MatchString(runID) || !identifier.MatchString(digest) {
		return Session{}, errors.New("invalid run identity")
	}
	input, err := compact(input)
	if err != nil {
		return Session{}, err
	}
	path, err := store.directory(ctx, key)
	if err != nil {
		return Session{}, err
	}
	var session Session
	err = withLock(ctx, path, func() error {
		beginPath := filepath.Join(path, hash(runID)+".begin")
		data, err := readFile(ctx, beginPath)
		if err == nil {
			if decode(data, &session) != nil || session.RunID != runID || len(session.Notebook) > MaxNotebookBytes {
				return ErrIntegrity
			}
			if session.Digest != digest || !bytes.Equal(session.Input, input) {
				return ErrConflict
			}
			records, err := history(ctx, path)
			if err != nil {
				return err
			}
			if !validBase(session, records) {
				return ErrIntegrity
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		previous, err := latest(ctx, path)
		if err != nil {
			return err
		}
		session = Session{RunID: runID, Digest: digest, Base: previous.Revision, Notebook: previous.Notebook, Input: input}
		return writeRecord(ctx, path, hash(runID)+".begin", session)
	})
	return session, err
}

// ValidateOutput checks the task result before it is persisted as a workflow message.
func ValidateOutput(output json.RawMessage) error {
	bounded, err := compact(output)
	if err != nil {
		return err
	}
	_, _, err = outputNotebook(bounded)
	return err
}

func outputNotebook(output json.RawMessage) (string, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return "", "", errors.New("output object required")
	}
	values := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return "", "", err
		}
		key, ok := token.(string)
		if !ok {
			return "", "", errors.New("invalid output key")
		}
		if _, exists := values[key]; exists {
			return "", "", errors.New("duplicate output key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return "", "", err
		}
		values[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return "", "", err
	}
	var status, notebook string
	if raw := values["status"]; len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &status) != nil {
		return "", "", errors.New("output status required")
	}
	if raw := values["notebook"]; len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &notebook) != nil {
		return "", "", errors.New("output notebook required")
	}
	if status != "completed" && status != "no_change" && status != "blocked" {
		return "", "", errors.New("invalid output status")
	}
	if len(notebook) > MaxNotebookBytes {
		return "", "", errors.New("notebook too large")
	}
	return status, notebook, nil
}

func notebookForStatus(status, proposed, previous string) string {
	if status == "blocked" {
		return previous
	}
	return proposed
}

func (store Directory) Commit(ctx context.Context, key, runID, digest string, output json.RawMessage) (Record, error) {
	if !identifier.MatchString(runID) || !identifier.MatchString(digest) {
		return Record{}, errors.New("invalid run identity")
	}
	output, err := compact(output)
	if err != nil {
		return Record{}, err
	}
	status, notebook, err := outputNotebook(output)
	if err != nil {
		return Record{}, err
	}
	path, err := store.directory(ctx, key)
	if err != nil {
		return Record{}, err
	}
	var record Record
	err = withLock(ctx, path, func() error {
		data, err := readFile(ctx, filepath.Join(path, hash(runID)+".begin"))
		if err != nil {
			return err
		}
		var session Session
		if decode(data, &session) != nil || session.RunID != runID {
			return ErrIntegrity
		}
		if session.Digest != digest {
			return ErrConflict
		}
		records, err := history(ctx, path)
		if err != nil {
			return err
		}
		if !validBase(session, records) {
			return ErrIntegrity
		}
		commitPath := filepath.Join(path, hash(runID)+".commit")
		data, err = readFile(ctx, commitPath)
		if err == nil {
			if decode(data, &record) != nil || record.RunID != runID || record.Revision != session.Base+1 {
				return ErrIntegrity
			}
			if record.Digest != digest || !bytes.Equal(record.Output, output) {
				return ErrConflict
			}
			if record.Notebook != notebookForStatus(status, notebook, session.Notebook) {
				return ErrIntegrity
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		previous, err := latest(ctx, path)
		if err != nil {
			return err
		}
		if previous.Revision != session.Base {
			return ErrConflict
		}
		notebook = notebookForStatus(status, notebook, session.Notebook)
		record = Record{Revision: previous.Revision + 1, RunID: runID, Digest: digest, Notebook: notebook, Output: output}
		if err := writeRecord(ctx, path, hash(runID)+".commit", record); err != nil {
			return fmt.Errorf("commit notebook: %w", err)
		}
		return nil
	})
	return record, err
}
