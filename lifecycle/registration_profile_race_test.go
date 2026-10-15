package lifecycle

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jake-molnia/agent-runtime/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

func TestConcurrentRegistrationConflictsWithPublishedOtherProfile(t *testing.T) {
	c, api, _ := fixture(t)
	c.Profiles["other"] = c.Profiles["default"]
	gets := 0
	api.PrependReactor("get", "persistentvolumeclaims", func(a ktesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets != 2 {
			return false, nil, nil
		}
		get := a.(ktesting.GetAction)
		raw, err := api.Tracker().Get(sandbox.WorkspacePVCs, get.GetNamespace(), get.GetName())
		if err != nil {
			return true, nil, err
		}
		obj := raw.(*unstructured.Unstructured)
		// The other registrant wins publication after our absent lookup and PVC creation.
		s := State{PoolID: &c.PoolID, BindingGeneration: 1, Request: Request{WorkspaceID: testWorkspace, Profile: "other", OperationID: "winner", Revision: 1, Action: ReleaseCompute}, ProfileHash: digest(c.Profiles["other"]), Phase: "released", CompletedOperation: "winner"}
		encoded, err := json.Marshal(s)
		if err != nil {
			return true, nil, err
		}
		annotations := obj.GetAnnotations()
		annotations[stateAnnotation] = string(encoded)
		obj.SetAnnotations(annotations)
		if err = api.Tracker().Update(sandbox.WorkspacePVCs, obj, get.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	h := Handler(c, &fakeTasks{}, "control")
	response, _ := workspaceCall(t, h, http.MethodPut, workspacePath(), `{"pool":"local","profile":"default"}`, "")
	if response.Code != http.StatusConflict {
		t.Fatalf("want conflict for other published profile, got %d: %s", response.Code, response.Body)
	}
	saved, _, err := c.workspaceBinding(t.Context(), testWorkspace)
	if err != nil || saved.Request.Profile != "other" {
		t.Fatal("winning profile overwritten", saved, err)
	}
	claims, err := api.Resource(sandbox.WorkspacePVCs).Namespace("test").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(claims.Items) != 1 {
		t.Fatal(claims, err)
	}
}
