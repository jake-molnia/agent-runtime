package lifecycle

import (
	"context"
	"testing"

	"github.com/jake-molnia/agent-runtime/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestProfileMigrationPreservesStorageAndFencesOldExecution(t *testing.T) {
	c, api, _ := fixture(t)
	ctx := context.Background()
	target := c.Profiles["default"]
	target.PodTemplate = *target.PodTemplate.DeepCopy()
	target.PodTemplate.Spec.Containers[0].Image = "worker:v2"
	c.Profiles["next"] = target
	running := run(t, c, request(1, EnsureRunning))
	migration := ProfileMigration{WorkspaceID: running.Request.WorkspaceID, FromProfile: "default", ToProfile: "next", ExpectedProfileHash: running.ProfileHash, ExpectedRevision: 1, ExpectedEpoch: running.Epoch}
	if _, err := c.MigrateProfile(ctx, migration); err == nil {
		t.Fatal("migrated live allocation")
	}
	released := run(t, c, stopRequest(2, ReleaseCompute, running))
	migration.ExpectedRevision = released.Request.Revision
	before, err := api.Resource(sandbox.WorkspacePVCs).Namespace(target.Namespace).Get(ctx, sandbox.WorkspacePVCName(migration.WorkspaceID), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		state, err := c.MigrateProfile(ctx, migration)
		if err != nil {
			t.Fatal(err)
		}
		if state.Epoch != released.Epoch || state.ProfileHash != digest(target) || state.Request.Profile != "next" {
			t.Fatal("incorrect migrated state")
		}
	}
	after, err := api.Resource(sandbox.WorkspacePVCs).Namespace(target.Namespace).Get(ctx, before.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if before.GetUID() != after.GetUID() || digest(before.Object["spec"]) != digest(after.Object["spec"]) {
		t.Fatal("workspace storage changed")
	}
	if _, err := c.Admit(ctx, request(3, EnsureRunning)); err == nil {
		t.Fatal("old profile admitted")
	}
	next := request(3, EnsureRunning)
	next.Profile = "next"
	resumed := run(t, c, next)
	if resumed.Epoch != released.Epoch+1 {
		t.Fatal("allocation epoch did not advance")
	}
	if _, err := c.MigrateProfile(ctx, migration); err == nil {
		t.Fatal("stale migration accepted after execution resumed")
	}
}

func TestProfileMigrationRejectsChangedStorageAndStaleState(t *testing.T) {
	for _, test := range []string{"namespace", "storage", "hash", "revision", "epoch", "pending"} {
		t.Run(test, func(t *testing.T) {
			c, _, _ := fixture(t)
			state := run(t, c, request(1, EnsureRunning))
			state = run(t, c, stopRequest(2, ReleaseCompute, state))
			target := c.Profiles["default"]
			migration := ProfileMigration{WorkspaceID: state.Request.WorkspaceID, FromProfile: "default", ToProfile: "next", ExpectedProfileHash: state.ProfileHash, ExpectedRevision: 2, ExpectedEpoch: state.Epoch}
			switch test {
			case "namespace":
				target.Namespace = "elsewhere"
			case "storage":
				target.Storage.Size = "99Gi"
			case "hash":
				migration.ExpectedProfileHash = "wrong"
			case "revision":
				migration.ExpectedRevision++
			case "epoch":
				migration.ExpectedEpoch++
			case "pending":
				r := request(3, EnsureRunning)
				if _, err := c.Admit(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				migration.ExpectedRevision = 3
			}
			c.Profiles["next"] = target
			if _, err := c.MigrateProfile(context.Background(), migration); err == nil {
				t.Fatal("unsafe migration accepted")
			}
		})
	}
}
