package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/agentexec"
	"github.com/jake-molnia/agent-runtime/githubreview"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
	"github.com/jake-molnia/agent-runtime/orchestration"
	"github.com/jake-molnia/agent-runtime/sandbox"
	"google.golang.org/grpc"
	process "sigs.k8s.io/agent-sandbox/packages/sandboxd/spec/process/v1"
)

type checkoutTokenStub struct {
	got   githubreview.Input
	calls int
	err   error
}

func (t *checkoutTokenStub) CheckoutToken(_ context.Context, input githubreview.Input) (string, error) {
	t.got = input
	t.calls++
	return "private-contents-token", t.err
}

type checkoutProcess struct {
	process.ProcessServiceClient
	request *process.ExecuteRequest
}

func (p *checkoutProcess) Execute(_ context.Context, r *process.ExecuteRequest, _ ...grpc.CallOption) (*process.ExecuteResponse, error) {
	p.request = r
	return &process.ExecuteResponse{}, nil
}

type checkoutProvision struct {
	agentexec.Backend
	process *checkoutProcess
	calls   int
	prompt  string
}

func (b *checkoutProvision) Provision(ctx context.Context, d orchestration.Definition, r orchestration.Request) (orchestration.Prepared, error) {
	b.calls++
	b.prompt = r.Prompt
	return orchestration.Prepared{}, d.Prepare(ctx, &sandbox.Runtime{Processes: b.process}, map[string]string{"provider": "must-not-reach-git"})
}
func TestReviewCheckoutUsesTrustedInitialIdentity(t *testing.T) {
	input := githubreview.Input{InstallationID: 1, RepositoryID: 2, Repository: "owner/repo", Number: 3, BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}
	raw, _ := json.Marshal(map[string]any{"review": input})
	ctx, done, err := reviewCheckoutStep(context.Background(), hatchetbridge.ConfiguredStep{Initial: raw})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	tokens := &checkoutTokenStub{}
	p := &checkoutProcess{}
	backend := &checkoutProvision{process: p}
	wrapped := reviewCheckoutBackend{Backend: backend, tokens: tokens}
	request := orchestration.Request{Prompt: `{"review":{"repository":"attacker/other"}}`}
	if _, err = wrapped.Provision(ctx, orchestration.Definition{Directory: "/workspace/checkout"}, request); err != nil {
		t.Fatal(err)
	}
	if tokens.got != input || tokens.calls != 1 || backend.calls != 1 {
		t.Fatalf("wrong authority: %+v", tokens)
	}
	if p.request.Config.Command[3] != input.Repository || p.request.Config.Command[4] != input.BaseSHA || p.request.Config.Command[5] != input.HeadSHA {
		t.Fatal("checkout did not use pinned identity")
	}
	if len(p.request.Config.EnvVars) != 1 || p.request.Config.EnvVars["AGENT_RUNTIME_CHECKOUT_TOKEN"] != "private-contents-token" {
		t.Fatal("checkout credential scope changed")
	}
	if backend.prompt != request.Prompt || strings.Contains(backend.prompt, "private-contents-token") {
		t.Fatal("credential entered model prompt")
	}
}
func TestReviewCheckoutRequiresAuthorityBeforeProvision(t *testing.T) {
	tokens := &checkoutTokenStub{}
	base := &checkoutProvision{}
	backend := reviewCheckoutBackend{Backend: base, tokens: tokens}
	if _, err := backend.Provision(context.Background(), orchestration.Definition{}, orchestration.Request{}); err == nil || tokens.calls != 0 || base.calls != 0 {
		t.Fatal("provisioned without trusted initial identity")
	}
	ctx := context.WithValue(context.Background(), checkoutIdentityKey{}, githubreview.Input{})
	tokens.err = errors.New("revoked")
	if _, err := backend.Provision(ctx, orchestration.Definition{}, orchestration.Request{}); err == nil || base.calls != 0 {
		t.Fatal("provisioned after authorization failed")
	}
}
