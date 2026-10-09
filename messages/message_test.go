package messages

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func sampleMessage() Message {
	return Message{Version: Version, ID: "request", ContextID: "context", TaskID: "task", From: Actor{Agent: "caller", Revision: "v1"}, To: "producer", Parts: []Part{{Name: "request", Kind: Text, Text: "review this"}}}
}

func TestMessageRoundTrip(t *testing.T) {
	message := sampleMessage()
	data, err := Encode(message)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(data)
	if err != nil || decoded.ID != message.ID || decoded.Parts[0].Text != message.Parts[0].Text {
		t.Fatalf("round trip failed: %+v %v", decoded, err)
	}
}

func TestMessageRejectsInvalidEnvelope(t *testing.T) {
	mutations := map[string]func(*Message){
		"version":                 func(message *Message) { message.Version++ },
		"recipient":               func(message *Message) { message.To = "" },
		"revision":                func(message *Message) { message.From.Revision = "" },
		"empty":                   func(message *Message) { message.Parts = nil },
		"duplicate names":         func(message *Message) { message.Parts = append(message.Parts, message.Parts[0]) },
		"ambiguous part":          func(message *Message) { message.Parts[0].Data = json.RawMessage(`{}`) },
		"unknown kind":            func(message *Message) { message.Parts[0].Kind = "unknown" },
		"file without attachment": func(message *Message) { message.Parts[0] = Part{Name: "file", Kind: File} },
		"invalid reference":       func(message *Message) { message.InReplyTo = &Reference{ID: "prior", Digest: "bad"} },
		"self reply":              func(message *Message) { message.InReplyTo = &Reference{ID: message.ID, Digest: digest(nil)} },
		"invalid utf8":            func(message *Message) { message.Parts[0].Text = string([]byte{255}) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			message := sampleMessage()
			mutate(&message)
			if _, err := Encode(message); err == nil {
				t.Fatal("invalid message accepted")
			}
		})
	}
}

func TestTypedValidatorRejectsAmbiguousJSON(t *testing.T) {
	type payload struct {
		Value string `json:"value"`
	}
	validator := Typed(func(value payload) error {
		if value.Value == "" {
			return errors.New("value required")
		}
		return nil
	})
	if err := validator(json.RawMessage(`{"value":"ok"}`)); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{`null`, `{}`, `{"value":""}`, `{"value":"ok","extra":true}`, `{"value":"ok","value":"other"}`, `{"value":"ok"} {}`, `{"value":"ok"`, `{"value":9}`, strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)} {
		if err := validator(json.RawMessage(data)); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}

func TestDecodeRejectsUnknownAndDuplicateFields(t *testing.T) {
	data, _ := Encode(sampleMessage())
	for _, invalid := range [][]byte{
		append(data, []byte(` {}`)...),
		[]byte(strings.Replace(string(data), `"version":1`, `"version":1,"extra":true`, 1)),
		[]byte(strings.Replace(string(data), `"version":1`, `"version":1,"version":1`, 1)),
		[]byte(strings.Repeat(" ", MaxMessageBytes+1)),
	} {
		if _, err := Decode(invalid); err == nil {
			t.Fatal("invalid message JSON accepted")
		}
	}
}
