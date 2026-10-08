package tailnet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testScope(t *testing.T, id string, namespaces ...string) string {
	t.Helper()
	scope, err := NewScope(id, namespaces)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func mockClient(t *testing.T, handler func(*http.Request) (int, string)) *Client {
	t.Helper()
	return &Client{Scope: testScope(t, "local", "agents"), token: "test", expires: time.Now().Add(time.Hour), HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		status, body := handler(r)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
}

func TestReapPreservesUnscopedDevices(t *testing.T) {
	var deleted []string
	c := mockClient(t, func(r *http.Request) (int, string) {
		if r.Method == http.MethodDelete {
			deleted = append(deleted, r.URL.Path)
			return 200, `{}`
		}
		return 200, `{"devices":[{"id":"foreign-active","hostname":"ar-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","created":"2020-01-01T00:00:00Z","tags":["tag:agent-sandbox"]}]}`
	})
	if err := c.Reap(context.Background(), map[string]bool{"ar-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb": true}); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted foreign active devices: %v", deleted)
	}
}

func TestReapOwnership(t *testing.T) {
	claim := "ar-" + strings.Repeat("a", 32)
	stale := "ar-" + strings.Repeat("b", 32)
	local := &Client{Scope: testScope(t, "local", "agents")}
	foreign := &Client{Scope: testScope(t, "foreign", "agents")}
	oldConfig := &Client{Scope: testScope(t, "local", "agents", "removed")}
	type device struct {
		ID       string    `json:"id"`
		Hostname string    `json:"hostname"`
		Created  time.Time `json:"created"`
		Tags     []string  `json:"tags"`
	}
	old := time.Now().Add(-time.Hour)
	tag := []string{"tag:agent-sandbox"}
	cases := []struct {
		name, hostname string
		created        time.Time
		tags           []string
		active         map[string]bool
		wantDelete     bool
	}{
		{"active local", local.Hostname(claim), old, tag, map[string]bool{claim: true}, false},
		{"stale local", local.Hostname(stale), old, tag, map[string]bool{claim: true}, true},
		{"empty complete inventory", local.Hostname(stale), old, tag, map[string]bool{}, true},
		{"active foreign", foreign.Hostname(stale), old, tag, map[string]bool{claim: true}, false},
		{"removed namespace", oldConfig.Hostname(stale), old, tag, map[string]bool{}, false},
		{"legacy", stale, old, tag, map[string]bool{}, false},
		{"recent local", local.Hostname(stale), time.Now(), tag, map[string]bool{}, false},
		{"unknown creation time", local.Hostname(stale), time.Time{}, tag, map[string]bool{}, false},
		{"missing application tag", local.Hostname(stale), old, []string{"tag:other"}, map[string]bool{}, false},
		{"malformed scoped hostname", local.Hostname(stale) + "-1", old, tag, map[string]bool{}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var deleted []string
			c := mockClient(t, func(r *http.Request) (int, string) {
				if r.Method == "DELETE" {
					deleted = append(deleted, r.URL.Path)
					return 200, `{}`
				}
				body, err := json.Marshal(map[string]any{"devices": []device{{"device-id", tt.hostname, tt.created, tt.tags}}})
				if err != nil {
					t.Fatal(err)
				}
				return 200, string(body)
			})
			if err := c.Reap(context.Background(), tt.active); err != nil {
				t.Fatal(err)
			}
			var want []string
			if tt.wantDelete {
				want = []string{"/api/v2/device/device-id"}
			}
			if !reflect.DeepEqual(deleted, want) {
				t.Fatalf("deletions = %v, want %v", deleted, want)
			}
		})
	}
}

func TestReapRejectsUnknownInventoryAndScope(t *testing.T) {
	for _, tt := range []struct {
		name, scope string
		active      map[string]bool
	}{
		{"nil inventory", testScope(t, "local", "agents"), nil},
		{"invalid claim", testScope(t, "local", "agents"), map[string]bool{"invalid": true}},
		{"missing scope", "", map[string]bool{}},
		{"invalid scope", "local", map[string]bool{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := mockClient(t, func(r *http.Request) (int, string) { t.Fatalf("unexpected request %s", r.URL); return 500, "" })
			c.Scope = tt.scope
			if err := c.Reap(context.Background(), tt.active); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestReapDeviceListFailureDoesNotDelete(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"unavailable", 503, `{}`},
		{"truncated", 200, `{"devices":[{"id":"one"},`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := mockClient(t, func(r *http.Request) (int, string) {
				if r.Method != "GET" {
					t.Fatalf("unexpected %s", r.Method)
				}
				return tt.status, tt.body
			})
			if err := c.Reap(context.Background(), map[string]bool{}); err == nil {
				t.Fatal("expected list error")
			}
		})
	}
}

func TestIssueAndRevokeUseSameScope(t *testing.T) {
	claim := "ar-" + strings.Repeat("a", 32)
	scope := testScope(t, "local", "agents")
	hostname := (&Client{Scope: scope}).Hostname(claim)
	var deleted []string
	c := mockClient(t, func(r *http.Request) (int, string) {
		switch r.Method {
		case "POST":
			var body struct {
				Description  string `json:"description"`
				Capabilities struct {
					Devices struct {
						Create struct {
							Tags []string `json:"tags"`
						} `json:"create"`
					} `json:"devices"`
				} `json:"capabilities"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Description != hostname || !reflect.DeepEqual(body.Capabilities.Devices.Create.Tags, []string{"tag:agent-sandbox"}) {
				t.Fatalf("unexpected key request: %+v", body)
			}
			return 200, `{"id":"key-id","key":"auth-key"}`
		case "GET":
			return 200, fmt.Sprintf(`{"devices":[{"id":"local","hostname":%q,"tags":["tag:agent-sandbox"]},{"id":"foreign","hostname":%q,"tags":["tag:agent-sandbox"]},{"id":"legacy","hostname":%q,"tags":["tag:agent-sandbox"]},{"id":"untagged","hostname":%q}]}`, hostname, (&Client{Scope: testScope(t, "foreign", "agents")}).Hostname(claim), claim, hostname)
		case "DELETE":
			deleted = append(deleted, r.URL.Path)
			return 200, `{}`
		default:
			t.Fatalf("unexpected %s", r.Method)
			return 500, ""
		}
	})
	identity, err := c.Issue(context.Background(), claim, []string{"tag:agent-sandbox"})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Hostname != hostname || identity.ID != "key-id" || identity.Key != "auth-key" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if err := c.Revoke(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	want := []string{"/api/v2/tailnet/-/keys/key-id", "/api/v2/device/local"}
	if !reflect.DeepEqual(deleted, want) {
		t.Fatalf("deletions = %v, want %v", deleted, want)
	}
}

func TestRevokePreservesLegacyAndForeignDevices(t *testing.T) {
	claim := "ar-" + strings.Repeat("a", 32)
	for _, hostname := range []string{claim, (&Client{Scope: testScope(t, "foreign", "agents")}).Hostname(claim)} {
		t.Run(hostname, func(t *testing.T) {
			var calls int
			c := mockClient(t, func(r *http.Request) (int, string) {
				calls++
				if r.Method != "DELETE" || r.URL.Path != "/api/v2/tailnet/-/keys/known-key" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
				return 200, `{}`
			})
			if err := c.Revoke(context.Background(), Identity{ID: "known-key", Hostname: hostname}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("expected known key revocation, got %d calls", calls)
			}
		})
	}
}

func TestIssueRejectsUnscopedOrInvalidIdentity(t *testing.T) {
	for _, tt := range []struct {
		scope, claim string
		tags         []string
	}{
		{"", "ar-" + strings.Repeat("a", 32), []string{"tag:agent-sandbox"}},
		{testScope(t, "local", "agents"), "ar-invalid", []string{"tag:agent-sandbox"}},
		{testScope(t, "local", "agents"), "ar-" + strings.Repeat("a", 32), []string{"tag:other"}},
	} {
		c := mockClient(t, func(r *http.Request) (int, string) { t.Fatalf("unexpected request: %s", r.URL); return 500, "" })
		c.Scope = tt.scope
		if _, err := c.Issue(context.Background(), tt.claim, tt.tags); err == nil {
			t.Fatal("expected issue error")
		}
	}
}
