package lifecycle

import (
	"context"
	"errors"
)

// ProfileMigration changes execution software only after compute was released.
// Namespace and storage stay immutable; the retained PVC and epoch are preserved.
type ProfileMigration struct {
	WorkspaceID         string `json:"workspaceId"`
	FromProfile         string `json:"fromProfile"`
	ToProfile           string `json:"toProfile"`
	ExpectedProfileHash string `json:"expectedProfileHash"`
	ExpectedRevision    uint64 `json:"expectedRevision"`
	ExpectedEpoch       uint64 `json:"expectedEpoch"`
}

func (c *Controller) MigrateProfile(ctx context.Context, r ProfileMigration) (State, error) {
	if r.WorkspaceID == "" || len(r.WorkspaceID) > 512 || r.FromProfile == "" || r.ToProfile == "" || r.FromProfile == r.ToProfile || r.ExpectedRevision == 0 {
		return State{}, errors.New("workspace, distinct profiles and expected revision required")
	}
	from, fromOK := c.Profiles[r.FromProfile]
	to, toOK := c.Profiles[r.ToProfile]
	if !fromOK || !toOK || r.ExpectedProfileHash != digest(from) {
		return State{}, errors.New("source or target profile mismatch")
	}
	if from.Namespace != to.Namespace || digest(from.Storage) != digest(to.Storage) {
		return State{}, errors.New("profile migration cannot change workspace storage")
	}
	s, obj, err := c.load(ctx, r.FromProfile, r.WorkspaceID)
	if err != nil {
		// A retry after a successful compare-and-swap is safe while the exact
		// released revision remains current. Later execution fences old retries.
		s, obj, err = c.load(ctx, r.ToProfile, r.WorkspaceID)
		if err != nil {
			return State{}, err
		}
	}
	if s.Request.Revision != r.ExpectedRevision || s.Epoch != r.ExpectedEpoch || s.Phase != "released" || s.Handle != nil || s.Identity != nil || s.Endpoint != "" || s.CompletedOperation != s.Request.OperationID {
		return State{}, errors.New("profile migration requires the expected completed release")
	}
	c.invalidateGateway(r.FromProfile, r.WorkspaceID)
	defer c.invalidateGateway(r.FromProfile, r.WorkspaceID)
	s.Request.Profile = r.ToProfile
	s.ProfileHash = digest(to)
	if err = c.save(ctx, obj, s); err != nil {
		return State{}, err
	}
	return s, nil
}
