package githubreview

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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

func ValidateResolvedOutput(resolved Resolved, output json.RawMessage) error {
	if err := validateInput(resolved.Input); err != nil {
		return err
	}
	if resolved.Digest == "" || len(resolved.Digest) > 256 || resolved.Key != reviewKey(resolved.Input, resolved.Digest) {
		return errors.New("invalid resolved review key")
	}
	_, _, err := validateOutput(output)
	return err
}
