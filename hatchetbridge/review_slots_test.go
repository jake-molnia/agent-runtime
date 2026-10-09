package hatchetbridge

import (
	"context"
	"errors"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"testing"
)

type parentSlot struct {
	held *int
	err  error
}

func (s parentSlot) ReleaseSlot() error {
	if s.err != nil {
		return s.err
	}
	*s.held--
	return nil
}

func TestNestedReviewParentsLeaveCapacityForAgentWork(t *testing.T) {
	held := 2
	for i := 0; i < 2; i++ {
		if err := releaseReviewParentSlot(parentSlot{held: &held}); err != nil {
			t.Fatal(err)
		}
	}
	if held != 0 {
		t.Fatalf("nested parents retained %d of two worker slots", held)
	}
}

type cancelledReviewParent struct {
	hatchet.DurableContext
	released bool
}

func (c *cancelledReviewParent) ReleaseSlot() error { c.released = true; return context.Canceled }
func TestParentSlotFailurePreventsChildSubmissionAndRetainsCancellation(t *testing.T) {
	ctx := &cancelledReviewParent{}
	// Nil child/client would panic if a child were submitted before releasing capacity.
	_, err := runReviewChild(ctx, nil, nil, nil, nil)
	if !ctx.released || !errors.Is(err, context.Canceled) {
		t.Fatal("parent cancellation lost before child submission")
	}
}
