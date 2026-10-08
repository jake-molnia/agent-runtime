package orchestration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/sandbox"
	"github.com/jake-molnia/agent-runtime/tailnet"
	"github.com/jake-molnia/agent-runtime/telemetry"
	"k8s.io/client-go/rest"
)

type cleanupTransport func(*http.Request) (*http.Response, error)

func (f cleanupTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func cleanupResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestCleanupUsesScopedDeviceHostname(t *testing.T) {
	for _, rawHostname := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw-claim-%t", rawHostname), func(t *testing.T) {
			ctx := context.Background()
			tel, err := telemetry.New(ctx, telemetry.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := tel.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			})
			req := Request{Key: "owned-run"}
			lease := sandbox.Lease{Namespace: "agents", Claim: sandbox.ClaimName(req.Key), UID: "claim-uid", Host: "sandbox.invalid"}
			scope, err := tailnet.NewScope("deployment", []string{lease.Namespace})
			if err != nil {
				t.Fatal(err)
			}
			tc := &tailnet.Client{Scope: scope, ClientID: "test", ClientSecret: func(context.Context) (string, error) { return "test", nil }}
			hostname := tc.Hostname(lease.Claim)
			var deleted []string
			tc.HTTP = &http.Client{Transport: cleanupTransport(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/api/v2/oauth/token":
					return cleanupResponse(`{"access_token":"test","expires_in":3600}`), nil
				case "/api/v2/tailnet/-/devices":
					return cleanupResponse(fmt.Sprintf(`{"devices":[{"id":"owned","hostname":%q,"tags":["tag:agent-sandbox"]},{"id":"legacy","hostname":%q,"tags":["tag:agent-sandbox"]}]}`, hostname, lease.Claim)), nil
				default:
					if r.Method != "DELETE" {
						t.Fatalf("unexpected tailnet request: %s %s", r.Method, r.URL)
					}
					deleted = append(deleted, r.URL.Path)
					return cleanupResponse(`{}`), nil
				}
			})}
			var claimDeleted bool
			control, err := sandbox.NewControl(&rest.Config{Host: "https://kubernetes.invalid", Transport: cleanupTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "DELETE" || !strings.HasSuffix(r.URL.Path, "/"+lease.Claim) {
					t.Fatalf("unexpected Kubernetes request: %s %s", r.Method, r.URL)
				}
				claimDeleted = true
				return cleanupResponse(`{"kind":"Status","apiVersion":"v1","status":"Success"}`), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			engine := &Engine{Tailnet: tc, Control: control, Telemetry: tel, Transport: cleanupTransport(func(r *http.Request) (*http.Response, error) { return cleanupResponse(`{}`), nil })}
			p := Prepared{Lease: lease, SessionID: "session", Hostname: hostname}
			if rawHostname {
				p.Hostname = lease.Claim
			}
			if err := engine.Cleanup(ctx, req, p); err != nil {
				t.Fatal(err)
			}
			if !claimDeleted {
				t.Fatal("claim was not deleted")
			}
			if !reflect.DeepEqual(deleted, []string{"/api/v2/device/owned"}) {
				t.Fatalf("device deletions: %v", deleted)
			}
		})
	}
}
