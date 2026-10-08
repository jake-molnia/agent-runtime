package githubreview

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func webhookBody() string {
	input, _ := fixture()
	return fmt.Sprintf(`{"action":"opened","number":9,"installation":{"id":7},"repository":{"id":11,"full_name":"owner/repo"},"pull_request":{"number":9,"base":{"sha":%q,"repo":{"id":11,"full_name":"owner/repo"}},"head":{"sha":%q}}}`, input.BaseSHA, input.HeadSHA)
}
func signedRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Event", "pull_request")
	request.Header.Set("X-GitHub-Delivery", "delivery")
	mac := hmac.New(sha256.New, []byte("webhook-secret"))
	mac.Write([]byte(body))
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return request
}
func TestWebhookValidationAndSubmission(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        func(string) string
		mutate      func(*http.Request)
		status      int
		submitError bool
	}{
		{name: "accepted", status: 202},
		{name: "bad signature", mutate: func(request *http.Request) {
			request.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
		}, status: 401},
		{name: "bad content", mutate: func(request *http.Request) { request.Header.Set("Content-Type", "text/plain") }, status: 415},
		{name: "wrong event", mutate: func(request *http.Request) { request.Header.Set("X-GitHub-Event", "issues") }, status: 400},
		{name: "missing delivery", mutate: func(request *http.Request) { request.Header.Del("X-GitHub-Delivery") }, status: 400},
		{name: "ignored action", body: func(body string) string { return strings.Replace(body, "opened", "closed", 1) }, status: 204},
		{name: "wrong installation", body: func(body string) string {
			return strings.Replace(body, `"installation":{"id":7}`, `"installation":{"id":8}`, 1)
		}, status: 403},
		{name: "mismatched PR", body: func(body string) string {
			return strings.Replace(body, `"pull_request":{"number":9`, `"pull_request":{"number":8`, 1)
		}, status: 400},
		{name: "malformed JSON", body: func(body string) string { return body + "{}" }, status: 400},
		{name: "duplicate key", body: func(body string) string {
			return strings.Replace(body, `"action":"opened"`, `"action":"opened","action":"opened"`, 1)
		}, status: 400},
		{name: "oversized", body: func(body string) string { return strings.Repeat("a", MaxWebhookBytes+1) }, status: 413},
		{name: "submission failure", status: 503, submitError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler, err := NewWebhookHandler(WebhookConfig{Secret: func(context.Context) ([]byte, error) { return []byte("webhook-secret"), nil }, Actions: []string{"opened"}, Allowed: map[int64]int64{11: 7}, Submit: func(ctx context.Context, input Input) error {
				calls++
				if input.Repository != "owner/repo" || input.HeadSHA != strings.Repeat("b", 40) || input.DeliveryID != "delivery" {
					t.Errorf("bad typed input %+v", input)
				}
				if test.submitError {
					return errors.New("secret error")
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			body := webhookBody()
			if test.body != nil {
				body = test.body(body)
			}
			request := signedRequest(body)
			if test.mutate != nil {
				test.mutate(request)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status %d want %d: %s", recorder.Code, test.status, recorder.Body.String())
			}
			if test.status == 202 || test.submitError {
				if calls != 1 {
					t.Fatal("missing submission")
				}
			} else if calls != 0 {
				t.Fatal("submitted rejected webhook")
			}
			if strings.Contains(recorder.Body.String(), "secret error") {
				t.Fatal("leaked callback error")
			}
		})
	}
}

func TestWebhookRereadsExternallyRotatedSecret(t *testing.T) {
	secret := []byte("webhook-secret")
	reads, submissions := 0, 0
	handler, err := NewWebhookHandler(WebhookConfig{Secret: func(context.Context) ([]byte, error) {
		reads++
		return secret, nil
	}, Actions: []string{"opened"}, Allowed: map[int64]int64{11: 7}, Submit: func(context.Context, Input) error { submissions++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	body := webhookBody()
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, signedRequest(body))
	if first.Code != http.StatusAccepted {
		t.Fatal(first.Body.String())
	}
	secret = []byte("rotated-webhook-secret")
	old := httptest.NewRecorder()
	handler.ServeHTTP(old, signedRequest(body))
	if old.Code != http.StatusUnauthorized {
		t.Fatal("old secret remained cached")
	}
	request := signedRequest(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rotated := httptest.NewRecorder()
	handler.ServeHTTP(rotated, request)
	if rotated.Code != http.StatusAccepted || reads != 3 || submissions != 2 {
		t.Fatalf("rotation not honored: status=%d reads=%d submits=%d", rotated.Code, reads, submissions)
	}
}

func TestWebhookSourceFailureDoesNotEnqueueOrLeak(t *testing.T) {
	for _, source := range []struct {
		secret []byte
		err    error
	}{{err: errors.New("private-secret-sentinel")}, {}, {secret: make([]byte, 1025)}} {
		handler, err := NewWebhookHandler(WebhookConfig{Secret: func(context.Context) ([]byte, error) { return source.secret, source.err }, Actions: []string{"opened"}, Allowed: map[int64]int64{11: 7}, Submit: func(context.Context, Input) error { t.Error("unverified webhook submitted"); return nil }})
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedRequest(webhookBody()))
		if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "private-secret-sentinel") {
			t.Fatalf("source error leaked or accepted: %d %s", recorder.Code, recorder.Body.String())
		}
	}
}
