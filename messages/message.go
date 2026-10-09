package messages

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	Version            = 1
	MaxMessageBytes    = 2 << 20
	MaxAttachmentBytes = 16 << 20
	MaxParts           = 32
)

type Kind string

const (
	Text Kind = "text"
	Data Kind = "data"
	File Kind = "file"
)

type Reference struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

type Actor struct {
	Agent    string `json:"agent"`
	Revision string `json:"revision"`
}

type Attachment struct {
	Reference
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
}

type Part struct {
	Name       string          `json:"name"`
	Kind       Kind            `json:"kind"`
	Text       string          `json:"text,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Schema     string          `json:"schema,omitempty"`
	Attachment *Attachment     `json:"attachment,omitempty"`
}

type Message struct {
	Version   int        `json:"version"`
	ID        string     `json:"id"`
	ContextID string     `json:"context_id"`
	TaskID    string     `json:"task_id"`
	From      Actor      `json:"from"`
	To        string     `json:"to"`
	InReplyTo *Reference `json:"in_reply_to,omitempty"`
	Parts     []Part     `json:"parts"`
}

func validName(value string) bool {
	return len(value) > 0 && len(value) <= 256 && utf8.ValidString(value) && !bytes.ContainsAny([]byte(value), "\x00\r\n")
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func (reference Reference) Validate() error {
	if !validName(reference.ID) || !validDigest(reference.Digest) {
		return errors.New("invalid message reference")
	}
	return nil
}

func (part Part) Validate() error {
	if !validName(part.Name) {
		return errors.New("invalid part name")
	}
	if part.Attachment != nil {
		if err := part.Attachment.Reference.Validate(); err != nil {
			return err
		}
		if part.Text != "" || len(part.Data) != 0 || part.Attachment.Size < 0 || part.Attachment.Size > MaxAttachmentBytes || !validName(part.Attachment.MediaType) {
			return errors.New("invalid attachment part")
		}
	}
	switch part.Kind {
	case Text:
		if len(part.Data) != 0 || part.Schema != "" || !utf8.ValidString(part.Text) || (part.Attachment == nil && part.Text == "") {
			return errors.New("invalid text part")
		}
	case Data:
		if !validName(part.Schema) || part.Text != "" || (part.Attachment == nil && (!utf8.Valid(part.Data) || !json.Valid(part.Data))) {
			return errors.New("invalid data part")
		}
	case File:
		if part.Attachment == nil || part.Schema != "" {
			return errors.New("file part requires an attachment")
		}
	default:
		return errors.New("unknown part kind")
	}
	return nil
}

func (message Message) Validate() error {
	if message.Version != Version || !validName(message.ID) || !validName(message.ContextID) || !validName(message.TaskID) || !validName(message.From.Agent) || !validName(message.From.Revision) || !validName(message.To) {
		return errors.New("invalid message envelope")
	}
	if message.InReplyTo != nil {
		if err := message.InReplyTo.Validate(); err != nil {
			return err
		}
		if message.InReplyTo.ID == message.ID {
			return errors.New("message cannot reply to itself")
		}
	}
	if len(message.Parts) == 0 || len(message.Parts) > MaxParts {
		return errors.New("invalid message part count")
	}
	names := make(map[string]bool, len(message.Parts))
	for _, part := range message.Parts {
		if err := part.Validate(); err != nil {
			return err
		}
		if names[part.Name] {
			return errors.New("duplicate message part name")
		}
		names[part.Name] = true
	}
	return nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func Encode(message Message) ([]byte, error) {
	if err := message.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(message)
	if err == nil && len(data) > MaxMessageBytes {
		err = errors.New("message exceeds size limit")
	}
	if err == nil {
		err = checkJSON(json.NewDecoder(bytes.NewReader(data)), 0)
	}
	return data, err
}

func decodeStrict(data []byte, target any) error {
	if !utf8.Valid(data) {
		return errors.New("JSON must be valid UTF-8")
	}
	if err := checkJSON(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON shape")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func checkJSON(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid JSON")
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return errors.New("invalid JSON object")
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate or invalid JSON key")
			}
			seen[name] = true
			if err := checkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := checkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

func Decode(data []byte) (Message, error) {
	var message Message
	if len(data) > MaxMessageBytes {
		return message, errors.New("message exceeds size limit")
	}
	if err := decodeStrict(data, &message); err != nil {
		return message, err
	}
	return message, message.Validate()
}

type Validator func(json.RawMessage) error

func Typed[T any](check func(T) error) Validator {
	return func(data json.RawMessage) error {
		var value T
		if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return errors.New("null structured result")
		}
		if err := decodeStrict(data, &value); err != nil {
			return err
		}
		if check == nil {
			return errors.New("typed validator requires a semantic check")
		}
		if err := check(value); err != nil {
			return fmt.Errorf("structured result failed validation: %w", err)
		}
		return nil
	}
}
