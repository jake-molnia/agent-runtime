// Package lifecycle coordinates retained T3 workspaces through Agent Sandbox.
package lifecycle

import (
	"context"
	"errors"
	"github.com/jake-molnia/agent-runtime/sandbox"
	corev1 "k8s.io/api/core/v1"
	"net/http"
)

type Action string

const (
	EnsureRunning  Action = "ensure_running"
	Suspend        Action = "suspend"
	ReleaseCompute Action = "release_compute"
)

type Request struct {
	ResolvePendingAllocation bool   `json:"resolvePendingAllocation,omitempty"`
	WorkspaceID              string `json:"workspaceId"`
	OperationID              string `json:"operationId"`
	Revision                 uint64 `json:"revision"`
	Action                   Action `json:"action"`
	Profile                  string `json:"profile"`
	ExpectedAllocationID     string `json:"expectedAllocationId,omitempty"`
	ExpectedGeneration       uint64 `json:"expectedGeneration,omitempty"`
	ExpectedIncarnation      string `json:"expectedIncarnation,omitempty"`
}

func (r Request) Validate() error {
	if r.WorkspaceID == "" || len(r.WorkspaceID) > 512 || r.OperationID == "" || len(r.OperationID) > 512 || r.Revision == 0 || r.Revision > 9007199254740991 || r.Profile == "" {
		return errors.New("workspace, operation, positive revision and profile required")
	}
	if r.Action != EnsureRunning && r.Action != Suspend && r.Action != ReleaseCompute {
		return errors.New("invalid lifecycle action")
	}
	if r.ResolvePendingAllocation {
		if r.Action == EnsureRunning || r.ExpectedAllocationID != "" || r.ExpectedGeneration != 0 || r.ExpectedIncarnation != "" {
			return errors.New("pending allocation resolution requires a stop without an established identity")
		}
		return nil
	}
	if r.Action != EnsureRunning && (r.ExpectedAllocationID == "" || r.ExpectedGeneration == 0 || r.ExpectedIncarnation == "") {
		return errors.New("destructive action requires expected allocation, generation and incarnation")
	}
	return nil
}

type Profile struct {
	Namespace       string                 `json:"namespace"`
	Storage         sandbox.StorageSpec    `json:"storage"`
	PodTemplate     corev1.PodTemplateSpec `json:"podTemplate"`
	WorkerContainer string                 `json:"workerContainer"`
	Port            int                    `json:"port"`
}
type Identity struct {
	WorkspaceID     string `json:"workspaceId"`
	AllocationID    string `json:"allocationId"`
	Generation      uint64 `json:"generation"`
	Incarnation     string `json:"incarnation"`
	ProtocolVersion int    `json:"protocolVersion"`
	PodUID          string `json:"podUid"`
}
type WorkerRequest struct {
	Identity    Identity `json:"identity"`
	Revision    uint64   `json:"revision"`
	OperationID string   `json:"operationId"`
}
type Quiescence struct {
	Identity
	Revision         uint64 `json:"revision"`
	Quiescent        bool   `json:"quiescent"`
	ActiveOperations int    `json:"activeOperations"`
	PendingCallbacks int    `json:"pendingCallbacks"`
}
type Worker interface {
	Identity(context.Context, string, string) (Identity, error)
	Quiesce(context.Context, string, string, WorkerRequest) (Quiescence, error)
	Resume(context.Context, string, string, WorkerRequest) error
}
type State struct {
	BindingGeneration    uint64                   `json:"bindingGeneration,omitempty"`
	LastProfileMigration *ProfileMigration        `json:"lastProfileMigration,omitempty"`
	PendingStopIdentity  *Identity                `json:"pendingStopIdentity,omitempty"`
	PoolID               *string                  `json:"poolId,omitempty"`
	Request              Request                  `json:"request"`
	ProfileHash          string                   `json:"profileHash"`
	Epoch                uint64                   `json:"epoch"`
	Phase                string                   `json:"phase"`
	Handle               *sandbox.WorkspaceHandle `json:"handle,omitempty"`
	Identity             *Identity                `json:"identity,omitempty"`
	Endpoint             string                   `json:"endpoint,omitempty"`
	TaskID               string                   `json:"taskId,omitempty"`
	CompletedOperation   string                   `json:"completedOperation,omitempty"`
	StartedAt            string                   `json:"startedAt,omitempty"`
	Error                *WorkspaceError          `json:"error,omitempty"`
	Operation            *Operation               `json:"operation,omitempty"`
}
type Access struct {
	State State  `json:"state"`
	Token string `json:"token,omitempty"`
}
type Controller struct {
	gatewayCache      gatewayCache
	RegistryNamespace string
	PoolID            string
	PublicURL         string
	Resources         func(context.Context) (ResourceSnapshot, error)
	Control           *sandbox.Control
	Profiles          map[string]Profile
	SecretKey         []byte
	Worker            Worker
}

func New(control *sandbox.Control, profiles map[string]Profile, key []byte, registryNamespaces ...string) (*Controller, error) {
	if control == nil || len(key) < 32 || len(profiles) == 0 {
		return nil, errors.New("control, profiles and 32-byte runtime key required")
	}
	for name, p := range profiles {
		if name == "" || p.WorkerContainer == "" || p.Port < 1 || p.Port > 65535 {
			return nil, errors.New("invalid execution profile")
		}
		s := sandbox.WorkspaceSpec{Namespace: p.Namespace, WorkspaceID: "validation", AllocationID: "validation", Storage: p.Storage, PodTemplate: p.PodTemplate}
		if err := s.Validate(); err != nil {
			return nil, err
		}
		found := false
		for _, container := range p.PodTemplate.Spec.Containers {
			if container.Name == p.WorkerContainer {
				found = true
				mounted := false
				for _, mount := range container.VolumeMounts {
					if mount.Name == "workspace" && mount.MountPath != "" && !mount.ReadOnly {
						mounted = true
					}
				}
				if !mounted {
					return nil, errors.New("worker container requires a writable workspace volume mount")
				}
			}
		}
		if !found {
			return nil, errors.New("worker container missing from profile")
		}
	}
	namespaces := map[string]bool{}
	for _, p := range profiles {
		namespaces[p.Namespace] = true
	}
	registry := ""
	if len(registryNamespaces) > 1 {
		return nil, errors.New("one workspace registry namespace required")
	}
	if len(registryNamespaces) == 1 {
		registry = registryNamespaces[0]
	}
	if registry == "" && len(namespaces) == 1 {
		for namespace := range namespaces {
			registry = namespace
		}
	}
	if !namespaces[registry] {
		return nil, errors.New("multiple profile namespaces require an explicit workspace registry namespace from those profiles")
	}
	return &Controller{RegistryNamespace: registry, Control: control, Profiles: profiles, SecretKey: append([]byte(nil), key...), Worker: HTTPWorker{Client: &http.Client{Timeout: 15e9}}}, nil
}
