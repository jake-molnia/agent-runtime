package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const MaxEvent = 4 << 20

type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Events consumes a volatile stream. Re-query session state after any disconnect.
// The callback runs on the reader; it must not block on logging or UI delivery.
func (c *Client) Events(ctx context.Context, receive func(Event) error) error {
	res, err := c.EventSubscribe(ctx, Arguments{Header: http.Header{"Accept": []string{"text/event-stream"}}})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return ReadEvents(res.Body, receive)
}

func ReadEvents(r io.Reader, receive func(Event) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), MaxEvent)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			var event Event
			if err := json.Unmarshal([]byte(strings.TrimSuffix(data.String(), "\n")), &event); err != nil {
				return errors.New("invalid OpenCode event")
			}
			data.Reset()
			if err := receive(event); err != nil {
				return err
			}
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			if data.Len()+len(value)+1 > MaxEvent {
				return errors.New("OpenCode event exceeds limit")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
