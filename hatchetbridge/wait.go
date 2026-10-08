package hatchetbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

type durableWaitRuntime interface {
	DurableTaskListener() *client.DurableTaskListener
	DurableEvictionSupported() bool
}

func interactionWait(ctx hatchet.DurableContext, sessionID string) func(context.Context, string) error {
	index := 0
	return func(waitCtx context.Context, _ string) error {
		index++
		return waitForInteraction(waitCtx, ctx, sessionID, index)
	}
}

func waitForInteraction(waitCtx context.Context, ctx hatchet.DurableContext, sessionID string, index int) error {
	if err := waitCtx.Err(); err != nil {
		return err
	}
	runtime, ok := ctx.(durableWaitRuntime)
	if !ok || runtime.DurableTaskListener() == nil || !runtime.DurableEvictionSupported() {
		// Hatchet v0.109.10's legacy WaitFor leaves an unbuffered callback
		// registered on cancellation. Poll without registering that callback.
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-timer.C:
			return waitCtx.Err()
		}
	}

	// Keep the same durable context and replay counters. Both Now and WaitFor
	// use its stored context, so bind the run deadline before either call.
	original := ctx.GetContext()
	ctx.SetContext(waitCtx)
	defer ctx.SetContext(original)
	now, err := ctx.Now()
	if err != nil {
		return err
	}
	if err := waitCtx.Err(); err != nil {
		return err
	}
	// Hatchet hashes the sleep duration into the wait's replay identity. Measure
	// wall time once per wait, then reuse that duration on replay. The bound
	// context still enforces the deadline when replaying a longer cached sleep.
	raw, err := ctx.Memo(fmt.Sprintf("agent:interaction:sleep:%d", index), func() (any, error) {
		sleep := 30 * time.Second
		if deadline, ok := waitCtx.Deadline(); ok {
			sleep = min(sleep, time.Until(deadline))
			if sleep <= 0 {
				return nil, context.DeadlineExceeded
			}
		}
		return sleep, nil
	})
	if err != nil {
		return err
	}
	var sleep time.Duration
	if err := json.Unmarshal(raw, &sleep); err != nil {
		return err
	}
	if err := waitCtx.Err(); err != nil {
		return err
	}
	_, err = ctx.WaitFor(hatchet.OrCondition(
		hatchet.UserEventCondition("agent:interaction", "",
			hatchet.WithEventScope(sessionID),
			hatchet.WithConsiderEventsSince(now.Add(-time.Minute))),
		hatchet.SleepCondition(sleep),
	))
	if waitErr := waitCtx.Err(); waitErr != nil {
		return waitErr
	}
	return err
}
