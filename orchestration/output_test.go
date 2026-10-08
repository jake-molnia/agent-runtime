package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/opencode"
	"github.com/jake-molnia/agent-runtime/sandbox"
)

const outputPrompt = `{"id":"msg_prompt","type":"user","text":"review","time":{"created":1}}`
const outputFinal = `{"id":"msg_final","type":"assistant","agent":"review","model":{"id":"model","providerID":"provider"},"time":{"created":2,"completed":3},"finish":"stop","content":[{"id":"text_final","type":"text","text":"{\"ok\":true}"}]}`

func outputEnvelope(messages ...string) string {
	return `{"data":[` + strings.Join(messages, ",") + `],"cursor":{}}`
}

func TestParseOutput(t *testing.T) {
	tool := `{"id":"msg_tool","type":"assistant","time":{"created":1,"completed":2},"finish":"tool-calls","content":[{"id":"call","type":"tool","name":"read","state":{"status":"completed","content":[{"type":"text","text":"not final"}]}}]}`
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{"final", outputEnvelope(outputFinal, outputPrompt), `{"ok":true}`},
		{"successful idle", outputEnvelope(`{"id":"msg_idle","type":"idle","outcome":"succeeded","time":{"created":4}}`, outputFinal, outputPrompt), `{"ok":true}`},
		{"tool step", outputEnvelope(outputFinal, tool, outputPrompt), `{"ok":true}`},
		{"skill instruction", outputEnvelope(outputFinal, `{"id":"msg_skill","type":"skill","skill":"review","name":"review","text":"instructions","time":{"created":1}}`, outputPrompt), `{"ok":true}`},
		{"old history", outputEnvelope(outputFinal, outputPrompt, `{"id":"msg_old","type":"assistant","finish":"error"}`), `{"ok":true}`},
		{"split text with reasoning", outputEnvelope(strings.Replace(outputFinal, `"content":[{"id":"text_final","type":"text","text":"{\"ok\":true}"}]`, `"content":[{"id":"reason","type":"reasoning","text":"not JSON"},{"id":"text1","type":"text","text":"{\"ok\":"},{"id":"text2","type":"text","text":"true}"}]`, 1), outputPrompt), `{"ok":true}`},
		{"scalar JSON", outputEnvelope(strings.Replace(outputFinal, `{\"ok\":true}`, `true`, 1), outputPrompt), `true`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseOutput(json.RawMessage(test.data), "msg_prompt")
			if err != nil || string(got) != test.want {
				t.Fatalf("ParseOutput = %s, %v; want %s", got, err, test.want)
			}
		})
	}
}

func TestParseOutputRejectsUnsafeResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
	}{
		{"missing prompt", outputEnvelope(outputFinal)},
		{"missing assistant", outputEnvelope(outputPrompt)},
		{"failed idle", outputEnvelope(`{"id":"msg_idle","type":"idle","outcome":"failed","time":{"created":4}}`, outputFinal, outputPrompt)},
		{"interrupted idle", outputEnvelope(`{"id":"msg_idle","type":"idle","outcome":"interrupted","time":{"created":4}}`, outputFinal, outputPrompt)},
		{"ambiguous idle", outputEnvelope(outputFinal, `{"id":"msg_idle","type":"idle","outcome":"succeeded","time":{"created":4}}`, outputPrompt)},
		{"duplicate idle", outputEnvelope(`{"id":"msg_idle1","type":"idle","outcome":"succeeded","time":{"created":4}}`, `{"id":"msg_idle2","type":"idle","outcome":"succeeded","time":{"created":4}}`, outputFinal, outputPrompt)},
		{"newer prompt", outputEnvelope(outputFinal, strings.Replace(outputPrompt, "msg_prompt", "msg_other", 1), outputPrompt)},
		{"wrong prompt type", outputEnvelope(outputFinal, strings.Replace(outputPrompt, `"type":"user"`, `"type":"synthetic"`, 1))},
		{"duplicate assistant", outputEnvelope(outputFinal, outputFinal, outputPrompt)},
		{"multiple finals", outputEnvelope(outputFinal, strings.Replace(outputFinal, "msg_final", "msg_other", 1), outputPrompt)},
		{"incomplete", outputEnvelope(strings.Replace(outputFinal, `,"completed":3`, "", 1), outputPrompt)},
		{"backwards time", outputEnvelope(strings.Replace(outputFinal, `"completed":3`, `"completed":1`, 1), outputPrompt)},
		{"missing created", outputEnvelope(strings.Replace(outputFinal, `"created":2,`, "", 1), outputPrompt)},
		{"missing finish", outputEnvelope(strings.Replace(outputFinal, `,"finish":"stop"`, "", 1), outputPrompt)},
		{"truncated", outputEnvelope(strings.Replace(outputFinal, `"stop"`, `"length"`, 1), outputPrompt)},
		{"error", outputEnvelope(strings.Replace(outputFinal, `"finish":"stop"`, `"finish":"stop","error":{"type":"unknown","message":"failed"}`, 1), outputPrompt)},
		{"retry", outputEnvelope(strings.Replace(outputFinal, `"finish":"stop"`, `"finish":"stop","retry":{"attempt":1}`, 1), outputPrompt)},
		{"failed earlier assistant", outputEnvelope(outputFinal, `{"id":"msg_tool","type":"assistant","time":{"created":1,"completed":2},"finish":"tool-calls","error":{"type":"unknown"}}`, outputPrompt)},
		{"tool text", outputEnvelope(strings.Replace(outputFinal, `"type":"text"`, `"type":"tool"`, 1), outputPrompt)},
		{"reasoning only", outputEnvelope(strings.Replace(outputFinal, `"type":"text"`, `"type":"reasoning"`, 1), outputPrompt)},
		{"mixed tool content", outputEnvelope(strings.Replace(outputFinal, `"content":[`, `"content":[{"type":"tool","text":"{}"},`, 1), outputPrompt)},
		{"unknown content", outputEnvelope(strings.Replace(outputFinal, `"type":"text"`, `"type":"new-kind"`, 1), outputPrompt)},
		{"unknown message", outputEnvelope(outputFinal, `{"id":"msg_unknown","type":"new-kind","time":{"created":1}}`, outputPrompt)},
		{"missing text", outputEnvelope(strings.Replace(outputFinal, `,"text":"{\"ok\":true}"`, "", 1), outputPrompt)},
		{"fenced JSON", outputEnvelope(strings.Replace(outputFinal, `{\"ok\":true}`, "```json\\n{\\\"ok\\\":true}\\n```", 1), outputPrompt)},
		{"multiple JSON values", outputEnvelope(strings.Replace(outputFinal, `{\"ok\":true}`, `{} {}`, 1), outputPrompt)},
		{"trailing prose", outputEnvelope(strings.Replace(outputFinal, `{\"ok\":true}`, `{} done`, 1), outputPrompt)},
		{"compaction boundary", outputEnvelope(outputFinal, `{"id":"msg_compact","type":"compaction","time":{"created":1}}`, outputPrompt)},
		{"shell boundary", outputEnvelope(outputFinal, `{"id":"msg_shell","type":"shell","time":{"created":1}}`, outputPrompt)},
		{"legacy V1", `[{"info":{"role":"assistant","parentID":"msg_prompt"},"parts":[{"type":"text","text":"{}"}]}]`},
		{"missing data", `{"cursor":{}}`},
		{"missing cursor", `{"data":[]}`},
		{"malformed", `{`},
		{"oversized", strings.Repeat(" ", MaxOutputBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseOutput(json.RawMessage(test.data), "msg_prompt")
			if err == nil || got != nil {
				t.Fatalf("accepted unsafe output: %s, %v", got, err)
			}
		})
	}
	if _, err := ParseOutput(json.RawMessage(outputEnvelope(outputFinal, outputPrompt)), ""); err == nil {
		t.Fatal("accepted empty prompt ID")
	}
}

type outputTransport func(*http.Request) (*http.Response, error)

func (transport outputTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

type outputBody struct {
	io.Reader
	read   int
	closed bool
}

func (body *outputBody) Read(buffer []byte) (int, error) {
	count, err := body.Reader.Read(buffer)
	body.read += count
	return count, err
}

func (body *outputBody) Close() error {
	body.closed = true
	return nil
}

func TestReadOutputPagination(t *testing.T) {
	request := Request{Key: "run"}
	prepared := Prepared{SessionID: "ses_run", MessageID: "msg_prompt", Lease: sandbox.Lease{Host: "sandbox"}}
	var bodies []*outputBody
	calls := 0
	engine := &Engine{SecretKey: []byte(strings.Repeat("k", 32))}
	engine.Transport = outputTransport(func(native *http.Request) (*http.Response, error) {
		calls++
		if native.Method != http.MethodGet || native.URL.Path != "/api/session/ses_run/message" {
			t.Fatalf("unexpected request: %s %s", native.Method, native.URL)
		}
		username, password, ok := native.BasicAuth()
		if !ok || username != "opencode" || password != engine.Password(request.Key) {
			t.Fatal("missing run authentication")
		}
		query := native.URL.Query()
		var data string
		switch calls {
		case 1:
			if query.Get("order") != "desc" || query.Get("cursor") != "" || query.Get("limit") != "50" {
				t.Fatalf("unexpected first query: %v", query)
			}
			data = `{"data":[` + outputFinal + `],"cursor":{"next":"opaque"}}`
		case 2:
			if query.Get("cursor") != "opaque" || query.Has("order") {
				t.Fatalf("unexpected cursor query: %v", query)
			}
			data = outputEnvelope(outputPrompt)
		default:
			t.Fatal("read beyond run prompt")
		}
		body := &outputBody{Reader: strings.NewReader(data)}
		bodies = append(bodies, body)
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})
	got, err := engine.ReadOutput(context.Background(), request, prepared)
	if err != nil || string(got) != `{"ok":true}` || calls != 2 {
		t.Fatalf("ReadOutput = %s, %v, calls %d", got, err, calls)
	}
	for _, body := range bodies {
		if !body.closed {
			t.Fatal("response body not closed")
		}
	}
}

func TestReadOutputBounds(t *testing.T) {
	for _, test := range []struct {
		name        string
		pages       []string
		wantSuccess bool
	}{
		{"exact limit", []string{outputEnvelope(outputFinal, outputPrompt) + strings.Repeat(" ", MaxOutputBytes-len(outputEnvelope(outputFinal, outputPrompt)))}, true},
		{"oversized body", []string{strings.Repeat(" ", MaxOutputBytes+100)}, false},
		{"cumulative bound", []string{`{"data":[` + strings.Replace(outputFinal, `{\"ok\":true}`, strings.Repeat("a", MaxOutputBytes/2), 1) + `],"cursor":{"next":"opaque"}}`, outputEnvelope(outputPrompt) + strings.Repeat(" ", MaxOutputBytes/2)}, false},
		{"repeated cursor", []string{`{"data":[` + outputFinal + `],"cursor":{"next":"opaque"}}`, `{"data":[` + outputFinal + `],"cursor":{"next":"opaque"}}`}, false},
		{"empty page", []string{outputEnvelope()}, false},
		{"missing prompt", []string{outputEnvelope(outputFinal)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var bodies []*outputBody
			calls := 0
			engine := &Engine{Transport: outputTransport(func(*http.Request) (*http.Response, error) {
				if calls >= len(test.pages) {
					t.Fatal("unexpected extra request")
				}
				body := &outputBody{Reader: strings.NewReader(test.pages[calls])}
				bodies = append(bodies, body)
				calls++
				return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
			})}
			got, err := engine.ReadOutput(context.Background(), Request{Key: "run"}, Prepared{SessionID: "ses_run", MessageID: "msg_prompt", Lease: sandbox.Lease{Host: "sandbox"}})
			if (err == nil) != test.wantSuccess || (!test.wantSuccess && got != nil) {
				t.Fatalf("ReadOutput = %s, %v", got, err)
			}
			read := 0
			for _, body := range bodies {
				read += body.read
				if !body.closed {
					t.Fatal("response body not closed")
				}
			}
			if read > MaxOutputBytes+1 {
				t.Fatalf("read %d bytes beyond bounded limit", read)
			}
		})
	}
}

func TestReadOutputTransportErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
		body := &outputBody{Reader: strings.NewReader("sensitive response")}
		engine := &Engine{Transport: outputTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: body}, nil
		})}
		_, err := engine.ReadOutput(context.Background(), Request{Key: "run"}, Prepared{SessionID: "ses_run", MessageID: "msg_prompt", Lease: sandbox.Lease{Host: "sandbox"}})
		var nativeError *opencode.HTTPError
		if !errors.As(err, &nativeError) || nativeError.Status != status || !body.closed || body.read != 0 {
			t.Fatalf("HTTP error = %v, body %#v", err, body)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine := &Engine{Transport: outputTransport(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})}
	_, err := engine.ReadOutput(ctx, Request{Key: "run"}, Prepared{SessionID: "ses_run", MessageID: "msg_prompt", Lease: sandbox.Lease{Host: "sandbox"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

type outputBrokenReader struct{}

func (outputBrokenReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestReadOutputReadFailure(t *testing.T) {
	body := &outputBody{Reader: outputBrokenReader{}}
	engine := &Engine{Transport: outputTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})}
	got, err := engine.ReadOutput(context.Background(), Request{Key: "run"}, Prepared{SessionID: "ses_run", MessageID: "msg_prompt", Lease: sandbox.Lease{Host: "sandbox"}})
	if !errors.Is(err, io.ErrUnexpectedEOF) || got != nil || !body.closed {
		t.Fatalf("ReadOutput = %s, %v, closed %v", got, err, body.closed)
	}
}

func TestParseOutputNativeCLI2026(t *testing.T) {
	data := `{"data":[{"id":"msg_11d72b1fa001ZqY0oIGDuJgzHm","time":{"created":1791495418362},"type":"idle","outcome":"succeeded"},{"id":"msg_11d72b1cc001GIXlq72ycX7JpX","time":{"created":1791495418344,"streamed":1791495418357,"completed":1791495418359},"type":"assistant","agent":"smoke-review","model":{"id":"json","providerID":"smoke","variant":"default"},"content":[{"type":"text","text":"{\"ok\":true,\"smoke\":\"native-v2\"}"}],"finish":"stop","rawFinish":"stop","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}},{"id":"msg_prompt","time":{"created":1791495418302},"text":"Return only JSON.","type":"user"}],"cursor":{"previous":"eyJpZCI6Im1zZ18xMWQ3MmIxZmEwMDFacVkwb0lHRHVKZ3pIbSIsIm9yZGVyIjoiZGVzYyIsImRpcmVjdGlvbiI6InByZXZpb3VzIn0","next":"eyJpZCI6Im1zZ19wcm9tcHQiLCJvcmRlciI6ImRlc2MiLCJkaXJlY3Rpb24iOiJuZXh0In0"}}`
	got, err := ParseOutput(json.RawMessage(data), "msg_prompt")
	if err != nil || string(got) != `{"ok":true,"smoke":"native-v2"}` {
		t.Fatalf("native CLI 2.0.26 output = %s, %v", got, err)
	}
}

func TestReadOutputRequiresRunIdentity(t *testing.T) {
	engine := &Engine{Transport: outputTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("made request without run identity")
		return nil, errors.New("unexpected request")
	})}
	for _, prepared := range []Prepared{{}, {SessionID: "ses_run"}, {MessageID: "msg_prompt"}} {
		if _, err := engine.ReadOutput(context.Background(), Request{Key: "run"}, prepared); err == nil {
			t.Fatal("accepted missing run identity")
		}
	}
	if _, err := engine.ReadOutput(context.Background(), Request{}, Prepared{SessionID: "ses_run", MessageID: "msg_prompt"}); err == nil {
		t.Fatal("accepted missing run key")
	}
}
