// Package orchestration connects sandbox lifecycle to native OpenCode sessions.
package orchestration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/jake-molnia/agent-runtime/artifacts"
	"github.com/jake-molnia/agent-runtime/opencode"
	runtimeapi "github.com/jake-molnia/agent-runtime/runtime"
	"github.com/jake-molnia/agent-runtime/sandbox"
	"github.com/jake-molnia/agent-runtime/tailnet"
	"github.com/jake-molnia/agent-runtime/telemetry"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	process "sigs.k8s.io/agent-sandbox/packages/sandboxd/spec/process/v1"
)

type Definition struct {
	AllowProjectConfig bool
	Pool               string
	Namespace          string
	Agent              string
	Model              map[string]string
	Directory          string
	Timeout            time.Duration
	Tags               []string
	// Config and Prepare execute trusted application code, never functions selected by task input.
	Config  func(map[string]string) (json.RawMessage, error)
	Secrets func(context.Context) (map[string]string, error)
	Prepare func(context.Context, *sandbox.Runtime, map[string]string) error
}
type Request struct {
	Key         string    `json:"key"`
	Prompt      string    `json:"prompt"`
	SubmittedAt time.Time `json:"submitted_at"`
}
type Prepared struct {
	Lease      sandbox.Lease `json:"lease"`
	SessionID  string        `json:"session_id"`
	MessageID  string        `json:"message_id"`
	IdentityID string        `json:"identity_id,omitempty"`
	Hostname   string        `json:"hostname,omitempty"`
	Started    time.Time     `json:"started"`
}
type Result struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Sandbox   string `json:"sandbox"`
}
type Engine struct {
	Artifacts artifacts.Store
	Control   *sandbox.Control
	Tailnet   *tailnet.Client
	Telemetry *telemetry.Telemetry
	// SecretKey must be stable across workers and restarts, and never enter workflow inputs.
	SecretKey []byte
	Transport http.RoundTripper
}

func (e *Engine) Password(key string) string {
	mac := hmac.New(sha256.New, e.SecretKey)
	mac.Write([]byte("opencode:" + key))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func ids(key string) (string, string) {
	h := sha256.Sum256([]byte(key))
	s := hex.EncodeToString(h[:16])
	return "ses_" + s, "msg_" + s
}
func (e *Engine) Client(key string, p Prepared) (*opencode.Client, error) {
	headers := http.Header{}
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("opencode:"+e.Password(key))))
	return opencode.New("http://"+p.Lease.Host+":4096", headers, e.Transport)
}
func (e *Engine) Provision(ctx context.Context, d Definition, req Request) (out Prepared, err error) {
	if len(e.SecretKey) < 32 || req.Key == "" || d.Config == nil || d.Timeout <= 0 || d.Timeout > 24*time.Hour {
		return out, errors.New("invalid engine, definition or run key")
	}
	start := req.SubmittedAt
	if start.IsZero() {
		start = time.Now()
	}
	out.Started = start
	out.SessionID, out.MessageID = ids(req.Key)
	run := e.Telemetry.Run(start, d.Pool, "unknown")
	var secrets map[string]string
	var identity tailnet.Identity
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if out.Lease.UID != "" {
				err = errors.Join(err, e.Control.Delete(cleanup, out.Lease))
			}
			if identity.ID != "" {
				err = errors.Join(err, e.Tailnet.Revoke(cleanup, identity))
			}
		}
	}()
	g, parallel := errgroup.WithContext(ctx)
	g.Go(func() error {
		return run.Phase(parallel, telemetry.Claim, func(ctx context.Context) error {
			var claimErr error
			out.Lease, claimErr = e.Control.Claim(ctx, d.Namespace, d.Pool, req.Key, start.Add(d.Timeout+5*time.Minute))
			run.Launch(out.Lease.Launch)
			return claimErr
		})
	})
	g.Go(func() error {
		return run.Phase(parallel, telemetry.Secrets, func(ctx context.Context) error {
			if d.Secrets == nil {
				secrets = map[string]string{}
				return nil
			}
			var secretErr error
			secrets, secretErr = d.Secrets(ctx)
			return secretErr
		})
	})
	if len(d.Tags) > 0 {
		g.Go(func() error {
			return run.Phase(parallel, telemetry.Identity, func(ctx context.Context) error {
				if e.Tailnet == nil {
					return errors.New("tailnet client required")
				}
				var identityErr error
				identity, identityErr = e.Tailnet.Issue(ctx, sandbox.ClaimName(req.Key), d.Tags)
				return identityErr
			})
		})
	}
	if err = g.Wait(); err != nil {
		return out, err
	}
	out.IdentityID = identity.ID
	out.Hostname = identity.Hostname
	metrics, err := e.Telemetry.RuntimeMetrics()
	if err != nil {
		return out, err
	}
	rt, err := sandbox.Connect(out.Lease.Host, metrics.Transport(e.Transport), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithChainUnaryInterceptor(metrics.Unary()), grpc.WithChainStreamInterceptor(metrics.Stream()))
	if err != nil {
		return out, err
	}
	defer rt.Close()
	if err = run.Phase(ctx, telemetry.Runtime, rt.Files.Ready); err != nil {
		return out, err
	}
	config, err := d.Config(secrets)
	if err != nil {
		return out, err
	}
	input := runtimeapi.Init{AllowProjectConfig: d.AllowProjectConfig, RunID: req.Key, Password: e.Password(req.Key), TailnetKey: identity.Key, Hostname: identity.Hostname, Config: config}
	g, parallel = errgroup.WithContext(ctx)
	g.Go(func() error {
		return run.Phase(parallel, telemetry.Initialize, func(ctx context.Context) error { return e.initialize(ctx, out.Lease.Host, input, run) })
	})
	if d.Prepare != nil {
		g.Go(func() error {
			return run.Phase(parallel, telemetry.Checkout, func(ctx context.Context) error { return d.Prepare(ctx, rt, secrets) })
		})
	}
	if err = g.Wait(); err != nil {
		return out, err
	}
	run.Milestone(ctx, "harness_ready")
	client, err := e.Client(req.Key, out)
	if err != nil {
		return out, err
	}
	err = run.Phase(ctx, telemetry.Session, func(ctx context.Context) error {
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("opencode.session_id", out.SessionID))
		// A stable client-selected ID permits recovery if session creation's response was lost.
		res, getErr := client.Do(ctx, "GET", "/api/session/{sessionID}", opencode.Arguments{Path: map[string]string{"sessionID": out.SessionID}})
		if getErr == nil {
			res.Body.Close()
			return nil
		}
		var status *opencode.HTTPError
		if !errors.As(getErr, &status) || status.Status != 404 {
			return getErr
		}
		res, createErr := client.Do(ctx, "POST", "/api/session", opencode.Arguments{Body: map[string]any{"id": out.SessionID, "location": map[string]string{"directory": d.Directory}, "agent": d.Agent, "model": d.Model}})
		if createErr == nil {
			res.Body.Close()
		}
		return createErr
	})
	return out, err
}
func (e *Engine) initialize(ctx context.Context, host string, input runtimeapi.Init, run *telemetry.Run) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://"+host+":8081/initialize", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: e.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("runtime initialization connection failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("runtime initialization rejected")
	}
	var result runtimeapi.InitResult
	if err = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&result); err != nil {
		return errors.New("invalid runtime readiness response")
	}
	run.Duration(ctx, telemetry.Tailnet, result.TailnetSeconds)
	run.Duration(ctx, telemetry.Harness, result.HarnessSeconds)
	return nil
}

// Execute recovers through authoritative state; no terminal event is required for completion.
// wait is called for pending permissions/forms. A Hatchet durable wait can release its execution slot.
func (e *Engine) Execute(ctx context.Context, d Definition, req Request, p Prepared, wait func(context.Context, string) error) (result Result, err error) {
	client, err := e.Client(req.Key, p)
	if err != nil {
		return result, err
	}
	run := e.Telemetry.Run(p.Started, d.Pool, p.Lease.Launch)
	defer func() { run.Finish(ctx, err) }()
	deadline := p.Started.Add(d.Timeout)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	eventsCtx, stopEvents := context.WithCancel(ctx)
	defer stopEvents()
	changed := make(chan struct{}, 1)
	connected := make(chan struct{})
	var once sync.Once
	go func() {
		for eventsCtx.Err() == nil {
			_ = client.Events(eventsCtx, func(event opencode.Event) error {
				if event.Type == "server.connected" {
					once.Do(func() { close(connected) })
					return nil
				}
				var data struct {
					SessionID string `json:"sessionID"`
				}
				if json.Unmarshal(event.Data, &data) != nil || data.SessionID != p.SessionID {
					return nil
				}
				switch event.Type {
				case "session.step.started":
					run.Milestone(eventsCtx, "harness_running")
				case "session.text.delta":
					run.Milestone(eventsCtx, "first_response")
					run.Milestone(eventsCtx, "first_text")
				case "session.reasoning.delta", "session.tool.input.started":
					run.Milestone(eventsCtx, "first_response")
				case "session.tool.called":
					run.Event(eventsCtx, "tool")
				case "session.tool.failed":
					run.Event(eventsCtx, "tool_failed")
				case "session.step.ended":
					run.Event(eventsCtx, "turn")
				case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted", "permission.asked", "form.created":
					select {
					case changed <- struct{}{}:
					default:
					}
				}
				return nil
			})
			if eventsCtx.Err() != nil {
				return
			}
			run.Event(eventsCtx, "reconnect")
			select {
			case <-eventsCtx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	// Bound subscription setup without making a failed event stream block prompt admission.
	select {
	case <-connected:
	case <-ctx.Done():
		return result, ctx.Err()
	case <-time.After(2 * time.Second):
	}
	err = run.Phase(ctx, telemetry.Prompt, func(ctx context.Context) error {
		res, submitErr := client.Do(ctx, "POST", "/api/session/{sessionID}/prompt", opencode.Arguments{Path: map[string]string{"sessionID": p.SessionID}, Body: map[string]any{"id": p.MessageID, "text": req.Prompt}})
		if submitErr == nil {
			res.Body.Close()
		}
		return submitErr
	})
	if err != nil {
		return result, err
	}
	err = run.Phase(ctx, telemetry.Execution, func(ctx context.Context) error {
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("opencode.session_id", p.SessionID))
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			status, statusErr := Status(ctx, client, p.SessionID)
			if statusErr == nil {
				if status.Outcome != "" {
					result = Result{SessionID: p.SessionID, Status: status.Outcome, Sandbox: p.Lease.Sandbox}
					if status.Outcome != "succeeded" {
						return errors.New("agent execution " + status.Outcome)
					}
					return nil
				}
				if status.Pending != "" {
					if wait == nil {
						return &InteractionRequired{Kind: status.Pending, SessionID: p.SessionID}
					}
					if waitErr := wait(ctx, status.Pending); waitErr != nil {
						return waitErr
					}
				}
			} else {
				var httpErr *opencode.HTTPError
				if errors.As(statusErr, &httpErr) && (httpErr.Status == 401 || httpErr.Status == 403 || httpErr.Status == 404) {
					return statusErr
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			case <-changed:
			}
		}
	})
	return result, err
}

type InteractionRequired struct {
	Kind      string
	SessionID string
}

func (e *InteractionRequired) Error() string { return "agent requires " + e.Kind }

type SessionStatus struct {
	Outcome string
	Pending string
}

func Status(ctx context.Context, c *opencode.Client, id string) (SessionStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := opencode.Arguments{Path: map[string]string{"sessionID": id}}
	s, err := opencode.Decode[struct {
		Data struct {
			Outcome string `json:"outcome"`
		} `json:"data"`
	}](c.Do(ctx, "GET", "/api/session/{sessionID}", args))
	if err != nil {
		return SessionStatus{}, err
	}
	if s.Data.Outcome != "" {
		return SessionStatus{Outcome: s.Data.Outcome}, nil
	}
	permissions, err := opencode.Decode[struct {
		Data []json.RawMessage `json:"data"`
	}](c.Do(ctx, "GET", "/api/session/{sessionID}/permission", args))
	if err != nil {
		return SessionStatus{}, err
	}
	if len(permissions.Data) > 0 {
		return SessionStatus{Pending: "permission"}, nil
	}
	forms, err := opencode.Decode[struct {
		Data []json.RawMessage `json:"data"`
	}](c.Do(ctx, "GET", "/api/session/{sessionID}/form", args))
	if err != nil {
		return SessionStatus{}, err
	}
	if len(forms.Data) > 0 {
		return SessionStatus{Pending: "form"}, nil
	}
	return SessionStatus{}, nil
}
func (e *Engine) Cleanup(ctx context.Context, req Request, p Prepared) error {
	run := e.Telemetry.Run(p.Started, "", "unknown")
	return run.Phase(ctx, telemetry.Cleanup, func(ctx context.Context) error {
		if c, err := e.Client(req.Key, p); err == nil {
			interrupt, cancel := context.WithTimeout(ctx, 3*time.Second)
			res, _ := c.Do(interrupt, "POST", "/api/session/{sessionID}/interrupt", opencode.Arguments{Path: map[string]string{"sessionID": p.SessionID}, Body: map[string]any{}})
			if res != nil {
				res.Body.Close()
			}
			cancel()
		}
		var identityErr error
		if p.Hostname != "" && e.Tailnet != nil {
			identityErr = e.Tailnet.Revoke(ctx, tailnet.Identity{ID: p.IdentityID, Hostname: p.Hostname})
		}
		return errors.Join(e.Control.Delete(ctx, p.Lease), identityErr)
	})
}

// Command prepares a checkout using argv and a request-scoped environment, without logging command output.
func Command(argv []string, cwd string) func(context.Context, *sandbox.Runtime, map[string]string) error {
	return func(ctx context.Context, r *sandbox.Runtime, env map[string]string) error {
		response, err := r.Processes.Execute(ctx, &process.ExecuteRequest{Config: &process.ProcessConfig{Command: argv, Cwd: &cwd, EnvVars: env}})
		if err != nil {
			return errors.New("sandbox preparation transport failed")
		}
		if response.ExitCode != 0 {
			return errors.New("sandbox preparation failed")
		}
		return nil
	}
}

func (e *Engine) Collect(ctx context.Context, req Request, p Prepared) (string, error) {
	if e.Artifacts == nil {
		return "", errors.New("artifact store is not configured")
	}
	client, err := e.Client(req.Key, p)
	if err != nil {
		return "", err
	}
	response, err := client.Do(ctx, "GET", "/api/experimental/session/{sessionID}/export", opencode.Arguments{Path: map[string]string{"sessionID": p.SessionID}})
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	return e.Artifacts.Put(ctx, req.Key, response.Body)
}

// Cancel stops an owned run even when its provision task failed before persisting a result.
func (e *Engine) Cancel(ctx context.Context, d Definition, req Request) error {
	lease, err := e.Control.Find(ctx, d.Namespace, req.Key)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	session, message := ids(req.Key)
	p := Prepared{Lease: lease, SessionID: session, MessageID: message, Started: req.SubmittedAt}
	if len(d.Tags) > 0 {
		p.Hostname = lease.Claim
	}
	return e.Cleanup(ctx, req, p)
}
