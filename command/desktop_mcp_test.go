package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDesktopMCPReadinessReadsCompleteInventory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var pages atomic.Int32
			var closed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					closed.Store(true)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var request struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
					Params struct {
						Cursor string `json:"cursor"`
					} `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				result := any(map[string]string{"protocolVersion": "2025-03-26"})
				if request.Method == "initialize" {
					w.Header().Set("Mcp-Session-Id", "inventory-session")
				} else {
					if r.Header.Get("Mcp-Session-Id") != "inventory-session" || r.Header.Get("MCP-Protocol-Version") != "2025-03-26" {
						t.Error("session and negotiated protocol not carried across requests")
					}
					if request.Method == "notifications/initialized" {
						w.WriteHeader(http.StatusAccepted)
						return
					}
					page := pages.Add(1)
					if request.Method != "tools/list" || request.ID != int(page)+1 {
						t.Errorf("unexpected method or reused page request ID: %+v", request)
					}
					switch page {
					case 1:
						if request.Params.Cursor != "" {
							t.Error("first page carried a cursor")
						}
						result = map[string]any{"tools": []any{map[string]string{"name": "navigate"}}, "nextCursor": "second page +/="}
					case 2:
						if request.Params.Cursor != "second page +/=" {
							t.Errorf("wrong cursor: %q", request.Params.Cursor)
						}
						result = map[string]any{"tools": []any{map[string]string{"name": "screenshot"}}, "nextCursor": "unneeded-page"}
					default:
						t.Error("continued listing after required tools were found")
					}
				}
				if !stream {
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, ": keepalive\n\nevent: message\ndata: {"+`"jsonrpc":"2.0","method":"notifications/message","params":{}`+"}\n\n")
				fmt.Fprintf(w, "data:{\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":\"ping\"}\n\n", request.ID)
				fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{}}\n\n", request.ID+100)
				data, _ := json.Marshal(result)
				fmt.Fprintf(w, "event: message\r\ndata:{\"jsonrpc\":\"2.0\",\r\n: another keepalive\r\ndata: \"id\":%d,\r\ndata:\"result\":%s}\r\n\r\n", request.ID, data)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := desktopMCPReady(ctx, server.URL, []string{"navigate", "screenshot"}); err != nil {
				t.Fatal(err)
			}
			if pages.Load() != 2 || !closed.Load() {
				t.Fatalf("pages=%d closed=%t", pages.Load(), closed.Load())
			}
		})
	}
}

func TestDesktopMCPReadinessRejectsUnrelatedJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":999,"result":{"protocolVersion":"2025-03-26","tools":[{"name":"screenshot"}]}}`)
	}))
	defer server.Close()
	if err := desktopMCPReady(context.Background(), server.URL, []string{"screenshot"}); err == nil {
		t.Fatal("accepted an unrelated JSON-RPC response")
	}
}

func TestDesktopMCPReadinessRejectsRepeatedCursor(t *testing.T) {
	var pages atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := any(map[string]string{"protocolVersion": "2025-03-26"})
		if request.Method == "tools/list" {
			pages.Add(1)
			result = map[string]any{"tools": []any{}, "nextCursor": "loop"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := desktopMCPReady(ctx, server.URL, []string{"screenshot"})
	if err == nil || !strings.Contains(err.Error(), "cursor") || pages.Load() != 2 {
		t.Fatalf("pages=%d error=%v", pages.Load(), err)
	}
}

func TestDesktopMCPReadinessBoundsSSE(t *testing.T) {
	for _, body := range []string{
		strings.Repeat(": keepalive\n\n", (2<<20)/13+1) + "data: {\"id\":1,\"result\":{\"protocolVersion\":\"2025-03-26\"}}\n\n",
		"data: {\"id\":1,\"result\":{\"protocolVersion\":\"2025-03-26\"}}\n",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.Method == "notifications/initialized" {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(strings.ReplaceAll(body, `"id":1`, fmt.Sprintf(`"id":%d`, request.ID))))
		}))
		err := desktopMCPReady(context.Background(), server.URL, nil)
		server.Close()
		if err == nil {
			t.Fatal("accepted an oversized or incomplete SSE response")
		}
	}
}

func TestDesktopMCPReadinessCancelsWhileAwaitingResponse(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- desktopMCPReady(ctx, server.URL, nil) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting for matching response ignored cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness did not stop after cancellation")
	}
}
