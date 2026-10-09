package githubreview

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

func uniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var consume func(int) error
	consume = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON nesting exceeds bounds")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delimiter {
		case '{':
			keys := make(map[string]bool)
			for decoder.More() {
				token, err = decoder.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || keys[key] {
					return errors.New("duplicate JSON object key")
				}
				keys[key] = true
				if err = consume(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err = consume(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	if err := consume(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
func checkOutputKeys(data []byte) error {
	if err := uniqueJSON(data); err != nil {
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	if len(root) != 2 || root["summary"] == nil || root["findings"] == nil {
		return errors.New("review output requires exact summary and findings keys")
	}
	var findings []json.RawMessage
	if err := json.Unmarshal(root["findings"], &findings); err != nil {
		return err
	}
	for _, finding := range findings {
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(finding, &keys); err != nil {
			return err
		}
		if len(keys) != 3 || keys["path"] == nil || keys["line"] == nil || keys["body"] == nil {
			return errors.New("finding requires exact path, line and body keys")
		}
	}
	return nil
}

func pinnedFiles(resolved Resolved) ([]File, error) {
	const prefix = "\nDiff JSON:\n"
	index := strings.LastIndex(resolved.Prompt, prefix)
	if index < 0 || len(resolved.Prompt) > MaxDiffBytes*6+4096 {
		return nil, errors.New("resolved prompt is missing bounded pinned diff")
	}
	raw := resolved.Prompt[index+len(prefix):]
	var files []File
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return nil, errors.New("invalid resolved diff")
	}
	if _, err := changedLines(files); err != nil {
		return nil, err
	}
	return files, nil
}
func sameFiles(first, second []File) bool {
	if len(first) != len(second) {
		return false
	}
	for index, file := range first {
		if file != second[index] {
			return false
		}
	}
	return true
}
func ValidateResolvedOutput(resolved Resolved, output json.RawMessage) error {
	if err := validateInput(resolved.Input); err != nil {
		return err
	}
	if resolved.Digest == "" || len(resolved.Digest) > 256 || resolved.Key != reviewKey(resolved.Input, resolved.Digest) {
		return errors.New("invalid resolved review key")
	}
	files, err := pinnedFiles(resolved)
	if err != nil {
		return err
	}
	lines, err := changedLines(files)
	if err != nil {
		return err
	}
	_, _, err = validateOutput(output, lines)
	return err
}
