package lifecycle

import (
	"context"
	"testing"

	"github.com/jake-molnia/agent-runtime/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAccessWithholdsCredentialsAfterPVCExpansion(t *testing.T) {
	c, api, _ := fixture(t)
	ctx := context.Background()
	s := run(t, c, request(1, EnsureRunning))
	if access, err := c.Access(ctx, "default", s.Request.WorkspaceID); err != nil || access.Token == "" {
		t.Fatal("initial worker not available", err)
	}
	pvc, err := api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.PVC, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedField(pvc.Object, "2Gi", "spec", "resources", "requests", "storage"); err != nil {
		t.Fatal(err)
	}
	if _, err = api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if access, err := c.Access(ctx, "default", s.Request.WorkspaceID); err == nil || access.Token != "" {
		t.Fatal("expanded PVC exposed worker credentials", err)
	}
	out, resource := workspaceCall(t, Handler(c, &fakeTasks{}, "control"), "GET", workspacePath(), "", "")
	if out.Code != 200 || resource.State != "unavailable" || resource.Execution != nil {
		t.Fatal("expanded PVC advertised execution", out.Code, out.Body)
	}
}
