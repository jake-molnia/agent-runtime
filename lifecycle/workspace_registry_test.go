package lifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jake-molnia/agent-runtime/sandbox"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestConcurrentWorkspaceRegistrationReservesOneNamespace(t *testing.T) {
	c, api, _ := fixture(t)
	other := c.Profiles["default"]
	other.Namespace = "other"
	c.Profiles["other"] = other
	replica, err := New(c.Control, c.Profiles, c.SecretKey, "test")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	start := make(chan struct{})
	for i, profile := range []string{"default", "other"} {
		controller := c
		if i == 1 {
			controller = replica
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			body, _ := json.Marshal(map[string]string{"pool": "local", "profile": profile})
			req := httptest.NewRequest("PUT", workspacePath(), strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer control")
			out := httptest.NewRecorder()
			Handler(controller, &fakeTasks{}, "control").ServeHTTP(out, req)
			codes <- out.Code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	success, conflict := 0, 0
	for code := range codes {
		if code == http.StatusCreated {
			success++
		} else if code == http.StatusConflict {
			conflict++
		} else {
			t.Fatal(code)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	pvcs, err := api.Resource(sandbox.WorkspacePVCs).Namespace("").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(pvcs.Items) != 1 {
		t.Fatal(pvcs, err)
	}
	if _, _, err := replica.workspaceBinding(t.Context(), testWorkspace); err != nil {
		t.Fatal("winning registration unusable", err)
	}
	if _, err := New(c.Control, c.Profiles, c.SecretKey); err == nil {
		t.Fatal("ambiguous default registry namespace accepted")
	}
}

func TestWorkerContainerMustMountWritableWorkspace(t *testing.T) {
	for _, kind := range []string{"sidecar-only", "read-only", "empty-path"} {
		t.Run(kind, func(t *testing.T) {
			c, _, _ := fixture(t)
			profile := c.Profiles["default"]
			profile.PodTemplate = *profile.PodTemplate.DeepCopy()
			profile.PodTemplate.Spec.Containers = append(profile.PodTemplate.Spec.Containers, corev1.Container{Name: "sidecar", Image: "sidecar:fixed", VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/data"}}})
			worker := &profile.PodTemplate.Spec.Containers[0]
			switch kind {
			case "sidecar-only":
				worker.VolumeMounts = nil
			case "read-only":
				worker.VolumeMounts[0].ReadOnly = true
			case "empty-path":
				worker.VolumeMounts[0].MountPath = ""
			}
			if _, err := New(c.Control, map[string]Profile{"default": profile}, c.SecretKey); err == nil {
				t.Fatal("invalid worker storage accepted")
			}
		})
	}
}
