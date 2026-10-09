package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/jake-molnia/agent-runtime/opencode"
)

const MaxOutputBytes = 1 << 20

// Transcript pages include tool history and JSON-escaped final text.
const MaxTranscriptBytes = 64 << 20

type outputMessage struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Finish  string          `json:"finish"`
	Outcome string          `json:"outcome"`
	Status  string          `json:"status"`
	Reason  string          `json:"reason"`
	Error   json.RawMessage `json:"error"`
	Retry   json.RawMessage `json:"retry"`
	Time    struct {
		Created   *int64 `json:"created"`
		Completed *int64 `json:"completed"`
	} `json:"time"`
	Content []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	} `json:"content"`
}

type outputPage struct {
	Data   []outputMessage `json:"data"`
	Cursor *struct {
		Next string `json:"next"`
	} `json:"cursor"`
}

func decodeOutputPage(data []byte) (outputPage, error) {
	var page outputPage
	if len(data) > MaxTranscriptBytes {
		return page, errors.New("OpenCode transcript exceeds 64 MiB")
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return page, errors.New("invalid OpenCode V2 message response")
	}
	if page.Data == nil || page.Cursor == nil {
		return page, errors.New("missing OpenCode V2 message data or cursor")
	}
	return page, nil
}

func ParseOutput(data json.RawMessage, promptID string) (json.RawMessage, error) {
	page, err := decodeOutputPage(data)
	if err != nil {
		return nil, err
	}
	return parseOutputMessages(page.Data, promptID)
}

func parseOutputMessages(messages []outputMessage, promptID string) (json.RawMessage, error) {
	if promptID == "" {
		return nil, errors.New("missing OpenCode prompt message ID")
	}
	seen := make(map[string]bool)
	var final *outputMessage
	idle := false
	for index := range messages {
		message := &messages[index]
		if message.ID == "" || seen[message.ID] || message.Time.Created == nil {
			return nil, errors.New("ambiguous OpenCode message timeline")
		}
		seen[message.ID] = true
		if message.ID == promptID {
			if message.Type != "user" || final == nil {
				return nil, errors.New("missing final assistant response to OpenCode prompt")
			}
			var text bytes.Buffer
			for _, content := range final.Content {
				switch content.Type {
				case "text":
					if content.Text == nil || len(*content.Text) > MaxOutputBytes-text.Len() {
						return nil, errors.New("invalid or oversized OpenCode final text")
					}
					text.WriteString(*content.Text)
				case "reasoning":
				default:
					return nil, errors.New("OpenCode final assistant response contains non-text output")
				}
			}
			if !json.Valid(text.Bytes()) {
				return nil, errors.New("OpenCode final assistant text is not standalone JSON")
			}
			return json.RawMessage(bytes.Clone(text.Bytes())), nil
		}
		switch message.Type {
		case "idle":
			if idle || final != nil || message.Outcome != "succeeded" {
				return nil, errors.New("OpenCode run is failed, interrupted, or ambiguous")
			}
			idle = true
		case "compaction":
			if final == nil || message.Status != "completed" || message.Reason != "auto" {
				return nil, errors.New("OpenCode compaction is incomplete or not automatic")
			}
		case "user", "shell":
			return nil, errors.New("OpenCode output does not unambiguously belong to this prompt")
		case "assistant":
			if len(message.Error) != 0 || len(message.Retry) != 0 || message.Time.Completed == nil || *message.Time.Completed < *message.Time.Created {
				return nil, errors.New("OpenCode assistant response is incomplete or failed")
			}
			if final == nil {
				if message.Finish != "stop" {
					return nil, errors.New("OpenCode assistant response is not final")
				}
				final = message
			} else if message.Finish != "tool-calls" {
				return nil, errors.New("ambiguous OpenCode final assistant response")
			}
		case "agent-switched", "model-switched", "system", "synthetic", "skill":
		default:
			return nil, errors.New("unknown OpenCode V2 message type")
		}
	}
	return nil, errors.New("OpenCode prompt message not found")
}

func (e *Engine) ReadOutput(ctx context.Context, req Request, prepared Prepared) (json.RawMessage, error) {
	if req.Key == "" || prepared.SessionID == "" || prepared.MessageID == "" {
		return nil, errors.New("missing OpenCode run identity")
	}
	client, err := e.Client(req.Key, prepared)
	if err != nil {
		return nil, err
	}
	remaining := int64(MaxTranscriptBytes)
	query := url.Values{"order": {"desc"}, "limit": {"50"}}
	var messages []outputMessage
	cursors := make(map[string]bool)
	for {
		response, err := client.Do(ctx, http.MethodGet, "/api/session/{sessionID}/message", opencode.Arguments{
			Path: map[string]string{"sessionID": prepared.SessionID}, Query: query,
		})
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, remaining+1))
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		remaining -= int64(len(data))
		if remaining < 0 {
			return nil, errors.New("OpenCode transcript exceeds 64 MiB")
		}
		page, err := decodeOutputPage(data)
		if err != nil {
			return nil, err
		}
		messages = append(messages, page.Data...)
		for _, message := range page.Data {
			if message.ID == prepared.MessageID {
				return parseOutputMessages(messages, prepared.MessageID)
			}
		}
		if len(page.Data) == 0 || page.Cursor.Next == "" {
			return nil, errors.New("OpenCode prompt message not found")
		}
		if cursors[page.Cursor.Next] {
			return nil, errors.New("OpenCode message cursor did not advance")
		}
		cursors[page.Cursor.Next] = true
		query = url.Values{"cursor": {page.Cursor.Next}, "limit": {"50"}}
	}
}
