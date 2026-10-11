package hatchetbridge

import (
	"github.com/jake-molnia/agent-runtime/lifecycle"
	"testing"
)

func TestT3RegistrationQueuesWorkspaceTransitionsOnHatchet107(t *testing.T) {
	client := offlineLifecycleClient(t)
	for _, pool := range []string{"", "gompers", "portal"} {
		workflow := RegisterT3(client, &lifecycle.Controller{PoolID: pool})
		definition, regular, durable, _ := workflow.Dump()
		if len(definition.ConcurrencyArr) != 1 || len(regular) != 1 || len(durable) != 0 {
			t.Fatal("invalid lifecycle task graph")
		}
		concurrency := definition.ConcurrencyArr[0]
		// 0.107 accepts GROUP_ROUND_ROBIN; QUEUE_NEWEST is a newer server strategy.
		// Keep max-one queuing so admissions are not cancelled by the scheduler.
		if concurrency.Expression != "input.workspaceId" || concurrency.GetMaxRuns() != 1 || concurrency.GetLimitStrategy().String() != "GROUP_ROUND_ROBIN" {
			t.Fatalf("unsupported or destructive lifecycle concurrency: %+v", concurrency)
		}
		if concurrency.GetName() != t3Name("t3-workspace", pool) || !concurrency.GetIsTenantScoped() {
			t.Fatalf("pool concurrency isolation lost: %+v", concurrency)
		}
	}
}
