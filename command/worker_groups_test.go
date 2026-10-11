package command

import (
	"context"
	"encoding/base64"
	"testing"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

func TestWorkerGroupsOwnDisjointDependencyActions(t *testing.T) {
	claims := `{"server_url":"http://127.0.0.1:1","grpc_broadcast_address":"127.0.0.1:1","exp":4102444800,"sub":"00000000-0000-0000-0000-000000000001"}`
	t.Setenv("HATCHET_CLIENT_EMBEDDED_DATABASE_URL", "")
	t.Setenv("HATCHET_CLIENT_TLS_STRATEGY", "none")
	t.Setenv("HATCHET_CLIENT_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("HATCHET_CLIENT_HOST_PORT", "127.0.0.1:1")
	token := "offline." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".offline"
	client, err := hatchet.NewClient(hatchet.WithToken(token), hatchet.WithHostPort("127.0.0.1", 1), hatchet.WithNamespace(""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	agent := client.NewWorkflow("configured-review")
	agent.NewDurableTask("agent", func(hatchet.DurableContext, map[string]any) (map[string]any, error) { return nil, nil })
	adapter := client.NewWorkflow("review-adapter")
	adapter.NewTask("prepare", func(hatchet.Context, map[string]any) (map[string]any, error) { return nil, nil })
	adapter.NewDurableTask("wait-agent", func(hatchet.DurableContext, map[string]any) (map[string]any, error) { return nil, nil })
	ingress := client.NewWorkflow("review-ingress")
	ingress.NewDurableTask("wait-adapter", func(hatchet.DurableContext, map[string]any) (map[string]any, error) { return nil, nil })
	configured := []hatchet.WorkflowBase{agent}
	groups := workerWorkflowGroups(configured, adapter, ingress)
	owners := map[string]string{}
	for _, group := range groups {
		for _, workflow := range group.workflows {
			declaration, _, durable, _ := workflow.Dump()
			for _, task := range declaration.Tasks {
				if previous, exists := owners[task.Action]; exists {
					t.Fatalf("action %s consumes both %s and %s capacity", task.Action, previous, group.name)
				}
				owners[task.Action] = group.name
				if task.IsDurable && task.SlotRequests["durable"] != 1 {
					t.Fatalf("durable slot contract changed: %+v", task.SlotRequests)
				}
			}
			for _, action := range durable {
				if action.EvictionPolicy != nil {
					t.Fatal("tier separation must not evict live cancellation guards")
				}
			}
		}
	}
	// Check the actual SDK registration actions, not a simulated released-slot counter.
	for _, expected := range []struct {
		workflow *hatchet.Workflow
		tier     string
	}{{agent, "agents"}, {adapter, "review"}, {ingress, "ingress"}} {
		declaration, _, _, _ := expected.workflow.Dump()
		for _, task := range declaration.Tasks {
			if owners[task.Action] != expected.tier {
				t.Fatalf("%s routed to %s, want %s", task.Action, owners[task.Action], expected.tier)
			}
		}
	}
	if len(groups) != 3 {
		t.Fatalf("dependency tiers collapsed: %d", len(groups))
	}
	normal := workerWorkflowGroups(configured, nil, nil)
	if len(normal) != 1 || len(normal[0].workflows) != 1 || normal[0].workflows[0] != agent {
		t.Fatal("reviews-disabled worker lost configured workflows")
	}
}
