package hatchetbridge

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/hatchet-dev/hatchet/pkg/client"
	"github.com/hatchet-dev/hatchet/pkg/worker"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/rs/zerolog"
)

// Run one action through the pinned SDK to obtain its actual, unexported context
// implementation. Only the worker transport is replaced.
func TestDurableWaitRuntimePinnedSDK(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logger := zerolog.Nop()
		transport := &waitSDKTransport{events: make(chan client.ActionEventType, 2)}
		w, err := worker.NewWorker(worker.WithClient(&waitSDKClient{transport: transport, logger: &logger}), worker.WithLogger(&logger))
		if err != nil {
			t.Fatal(err)
		}
		contexts := make(chan hatchet.DurableContext, 1)
		if err := w.RegisterAction("test:wait", func(ctx worker.HatchetContext) error {
			contexts <- worker.NewDurableHatchetContext(ctx)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		runCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- w.Run(runCtx) }()
		for range 2 {
			if event := <-transport.events; event == client.ActionEventTypeFailed {
				t.Fatal("SDK action failed")
			}
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		durable := <-contexts
		runtime, ok := durable.(durableWaitRuntime)
		if !ok {
			t.Fatalf("SDK context %T does not expose durable wait capabilities", durable)
		}
		if runtime.DurableTaskListener() != nil || runtime.DurableEvictionSupported() {
			t.Fatal("new context unexpectedly has modern durable infrastructure")
		}
		listener := &client.DurableTaskListener{}
		worker.SetContextDurableHooks(durable, nil, listener, true)
		if runtime.DurableTaskListener() != listener || !runtime.DurableEvictionSupported() {
			t.Fatal("SDK context did not expose attached modern durable infrastructure")
		}
		worker.SetContextDurableHooks(durable, nil, nil, false)
		if runtime.DurableTaskListener() != nil || runtime.DurableEvictionSupported() {
			t.Fatal("SDK context retained cleared durable infrastructure")
		}
	})
}

type waitSDKClient struct {
	client.Client
	transport *waitSDKTransport
	logger    *zerolog.Logger
}

func (c *waitSDKClient) Dispatcher() client.DispatcherClient { return c.transport }
func (c *waitSDKClient) Namespace() string                   { return "" }
func (c *waitSDKClient) RunnableActions() []string           { return nil }
func (c *waitSDKClient) CloudRegisterID() *string            { return nil }
func (c *waitSDKClient) Logger() *zerolog.Logger             { return c.logger }

type waitSDKTransport struct {
	client.DispatcherClient
	events chan client.ActionEventType
}

func (d *waitSDKTransport) GetActionListener(context.Context, *client.GetActionListenerRequest) (client.WorkerActionListener, *string, error) {
	id := "test-worker"
	return d, &id, nil
}

func (d *waitSDKTransport) SendStepActionEvent(_ context.Context, event *client.ActionEvent) (*client.ActionEventResponse, error) {
	d.events <- event.EventType
	return &client.ActionEventResponse{}, nil
}

func (d *waitSDKTransport) Actions(context.Context) (<-chan *client.Action, <-chan error, error) {
	actions := make(chan *client.Action, 1)
	actions <- &client.Action{ActionId: "test:wait", ActionType: client.ActionTypeStartStepRun, StepRunId: "test-run"}
	close(actions)
	return actions, nil, nil
}

func (*waitSDKTransport) Unregister() error { return nil }
