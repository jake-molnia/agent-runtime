package workflows

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func noSymlinks(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink forbidden: %s", current)
		}
		if current == filepath.Dir(current) {
			return nil
		}
	}
}

func existingAncestor(path string) (string, error) {
	for current := path; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(current); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", err
		}
		return current, noSymlinks(current)
	}
}

func safeDirectory(root string, create bool) (string, error) {
	if strings.Contains(root, `\`) {
		return "", errors.New("unsafe workflow root")
	}
	for _, component := range strings.Split(root, "/") {
		if component == ".." {
			return "", errors.New("unsafe workflow root")
		}
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(absolute, "workflows")
	if _, err := existingAncestor(directory); err != nil {
		return "", err
	}
	if create {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return "", err
		}
		if err := noSymlinks(directory); err != nil {
			return "", err
		}
	}
	return directory, nil
}

func readRegular(path string) ([]byte, error) {
	if err := noSymlinks(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("regular file required: %s", path)
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
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("file changed while opening: %s", path)
	}
	return io.ReadAll(file)
}

func Save(root string, snapshot Snapshot) error {
	if err := snapshot.Verify(); err != nil {
		return err
	}
	directory, err := safeDirectory(root, false)
	if err != nil {
		return err
	}
	ancestor, err := existingAncestor(directory)
	if err != nil {
		return err
	}
	if _, err := safeDirectory(root, true); err != nil {
		return err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	path := filepath.Join(directory, snapshot.Digest+".json")
	if err := os.Link(temporary.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if _, err := Read(root, snapshot.Digest); err != nil {
			return err
		}
	}
	for current := directory; ; current = filepath.Dir(current) {
		folder, err := os.Open(current)
		if err != nil {
			return err
		}
		syncErr := folder.Sync()
		closeErr := folder.Close()
		if syncErr != nil || closeErr != nil {
			return errors.Join(syncErr, closeErr)
		}
		if current == ancestor {
			return nil
		}
	}
}

func Read(root, digest string) (Snapshot, error) {
	if !validDigest(digest) {
		return Snapshot{}, errors.New("invalid workflow snapshot digest")
	}
	directory, err := safeDirectory(root, false)
	if err != nil {
		return Snapshot{}, err
	}
	data, err := readRegular(filepath.Join(directory, digest+".json"))
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Snapshot{}, errors.New("expected exactly one snapshot JSON document")
	}
	if snapshot.Digest != digest {
		return Snapshot{}, errors.New("stored workflow digest differs from requested digest")
	}
	if err := snapshot.Verify(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}
