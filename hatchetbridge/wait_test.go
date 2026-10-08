package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client"
	"github.com/hatchet-dev/hatchet/pkg/worker"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

type waitContext struct {
	memos map[string]json.RawMessage
	hatchet.DurableContext
	ctx  context.Context
	now  func() (time.Time, error)
	wait func(hatchet.Condition) (*worker.WaitResult, error)
	memo func(string, func() (any, error)) (json.RawMessage, error)
}

func (c *waitContext) GetContext() context.Context    { return c.ctx }
func (c *waitContext) SetContext(ctx context.Context) { c.ctx = ctx }
func (c *waitContext) Now() (time.Time, error)        { return c.now() }
func (c *waitContext) WaitFor(condition hatchet.Condition) (*worker.WaitResult, error) {
	return c.wait(condition)
}

func TestWaitForInteractionAlreadyDone(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "canceled"
		if deadline {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			cancel()
			original := context.Background()
			durable := &waitContext{ctx: original,
				now: func() (time.Time, error) { t.Error("Now called after run ended"); return time.Now(), nil },
				wait: func(hatchet.Condition) (*worker.WaitResult, error) {
					t.Error("WaitFor called after run ended")
					return nil, nil
				},
			}
			if err := waitForInteraction(ctx, durable, "session", 1); !errors.Is(err, ctx.Err()) {
				t.Fatalf("got %v, want %v", err, ctx.Err())
			}
			if durable.GetContext() != original {
				t.Fatal("task context was not restored")
			}
		})
	}
}

// These methods mirror the capability check used by the pinned SDK.
func (c *waitContext) DurableTaskListener() *client.DurableTaskListener {
	return &client.DurableTaskListener{}
}
func (c *waitContext) DurableEvictionSupported() bool { return true }

func TestWaitForInteractionCancellation(t *testing.T) {
	for _, stage := range []string{"memo", "duration", "wait"} {
		for _, deadline := range []bool{false, true} {
			name := stage + "/cancel"
			if deadline {
				name = stage + "/deadline"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					original := context.Background()
					ctx, cancel := context.WithCancel(original)
					if deadline {
						cancel()
						ctx, cancel = context.WithTimeout(original, time.Second)
					}
					defer cancel()
					durable := &waitContext{ctx: original}
					block := func() error {
						if durable.GetContext() != ctx {
							t.Fatal("SDK operation did not receive run context")
						}
						if !deadline {
							go func() { time.Sleep(time.Second); cancel() }()
						}
						<-durable.GetContext().Done()
						return durable.GetContext().Err()
					}
					durable.now = func() (time.Time, error) {
						if stage == "memo" {
							return time.Time{}, block()
						}
						return time.Now().Add(-time.Hour), nil
					}
					if stage == "duration" {
						durable.memo = func(string, func() (any, error)) (json.RawMessage, error) { return nil, block() }
					}
					durable.wait = func(condition hatchet.Condition) (*worker.WaitResult, error) {
						if stage != "wait" {
							t.Fatal("wait called after memo cancellation")
						}
						pb := condition.ToPB(0)
						sleep, err := time.ParseDuration(pb.SleepConditions[0].SleepFor)
						if err != nil {
							t.Fatal(err)
						}
						want := 30 * time.Second
						if deadline {
							want = time.Second
						}
						if sleep != want {
							t.Fatalf("sleep = %v, want %v", sleep, want)
						}
						return nil, block()
					}
					start := time.Now()
					err := waitForInteraction(ctx, durable, "session", 1)
					if !errors.Is(err, ctx.Err()) {
						t.Fatalf("got %v, want %v", err, ctx.Err())
					}
					if time.Since(start) != time.Second {
						t.Fatalf("wait took %v", time.Since(start))
					}
					if durable.GetContext() != original {
						t.Fatal("task context was not restored")
					}
				})
			})
		}
	}
}

func TestWaitForInteractionDurableWakes(t *testing.T) {
	for _, wake := range []string{"event", "sleep"} {
		t.Run(wake, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				original := context.Background()
				ctx, cancel := context.WithTimeout(original, 45*time.Second)
				defer cancel()
				memoized := time.Now().Add(-time.Hour)
				durable := &waitContext{ctx: original}
				calls := 0
				durable.now = func() (time.Time, error) {
					if durable.GetContext() != ctx {
						t.Fatal("Now did not receive run context")
					}
					return memoized, nil
				}
				durable.wait = func(condition hatchet.Condition) (*worker.WaitResult, error) {
					if durable.GetContext() != ctx {
						t.Fatal("WaitFor did not receive run context")
					}
					calls++
					pb := condition.ToPB(0)
					if len(pb.SleepConditions) != 1 || len(pb.UserEventConditions) != 1 {
						t.Fatalf("unexpected conditions: %+v", pb)
					}
					event := pb.UserEventConditions[0]
					if event.UserEventKey != "agent:interaction" || event.GetEventScope() != "session" ||
						!event.ConsiderEventsSince.AsTime().Equal(memoized.Add(-time.Minute)) {
						t.Fatalf("event changed: %v", event)
					}
					if event.Base.OrGroupId != pb.SleepConditions[0].Base.OrGroupId {
						t.Fatal("event and sleep are not alternatives")
					}
					sleep, err := time.ParseDuration(pb.SleepConditions[0].SleepFor)
					if err != nil {
						t.Fatal(err)
					}
					want := 30 * time.Second
					if calls == 2 && wake == "sleep" {
						want = 15 * time.Second
					}
					if sleep != want {
						t.Fatalf("sleep = %v, want %v", sleep, want)
					}
					if wake == "sleep" {
						time.Sleep(sleep)
						synctest.Wait()
					}
					return nil, nil
				}
				wait := interactionWait(durable, "session")
				for i := 0; i < 2; i++ {
					err := wait(ctx, "permission")
					if wake == "sleep" && i == 1 {
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("got %v, want deadline", err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					if durable.GetContext() != original {
						t.Fatal("task context was not restored")
					}
				}
				if calls != 2 {
					t.Fatalf("got %d calls, want two on the same durable context", calls)
				}
			})
		})
	}
}

func TestWaitForInteractionRestoresContextOnError(t *testing.T) {
	for _, stage := range []string{"memo", "duration", "wait", "cancelAfterMemo", "cancelAfterWait", "panic"} {
		t.Run(stage, func(t *testing.T) {
			original := context.Background()
			ctx, cancel := context.WithCancel(original)
			defer cancel()
			failure := errors.New("SDK failure")
			durable := &waitContext{ctx: original}
			durable.now = func() (time.Time, error) {
				if stage == "memo" {
					return time.Time{}, failure
				}
				if stage == "cancelAfterMemo" {
					cancel()
				}
				return time.Now(), nil
			}
			if stage == "duration" {
				durable.memo = func(string, func() (any, error)) (json.RawMessage, error) { return nil, failure }
			}
			durable.wait = func(hatchet.Condition) (*worker.WaitResult, error) {
				if stage == "cancelAfterMemo" {
					t.Fatal("wait called after run canceled")
				}
				if stage == "cancelAfterWait" {
					cancel()
					return nil, nil
				}
				if stage == "panic" {
					panic(failure)
				}
				return nil, failure
			}
			defer func() {
				recovered := recover()
				if stage == "panic" && recovered != failure {
					t.Errorf("panic = %v", recovered)
				}
				if stage != "panic" && recovered != nil {
					panic(recovered)
				}
				if durable.GetContext() != original {
					t.Error("task context was not restored")
				}
			}()
			err := waitForInteraction(ctx, durable, "session", 1)
			want := failure
			if stage == "cancelAfterMemo" || stage == "cancelAfterWait" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
		})
	}
}

// Hiding the runtime capabilities forces the legacy path without constructing
// the SDK's cancellation-unsafe listener.
type legacyWaitContext struct{ hatchet.DurableContext }

func TestWaitForInteractionLegacy(t *testing.T) {
	for _, mode := range []string{"poll", "cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				if mode == "deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), time.Second)
				}
				defer cancel()
				if mode == "cancel" {
					go func() { time.Sleep(time.Second); cancel() }()
				}
				start := time.Now()
				err := waitForInteraction(ctx, &legacyWaitContext{}, "session", 1)
				if mode == "poll" {
					if err != nil {
						t.Fatal(err)
					}
					if time.Since(start) != 30*time.Second {
						t.Fatalf("poll took %v", time.Since(start))
					}
				} else {
					if !errors.Is(err, ctx.Err()) {
						t.Fatalf("got %v, want %v", err, ctx.Err())
					}
					if time.Since(start) != time.Second {
						t.Fatalf("wait took %v", time.Since(start))
					}
				}
			})
		})
	}
}

func (c *waitContext) Memo(key string, fn func() (any, error)) (json.RawMessage, error) {
	if c.memo != nil {
		return c.memo(key, fn)
	}
	if raw, ok := c.memos[key]; ok {
		return raw, nil
	}
	value, err := fn()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if c.memos == nil {
		c.memos = make(map[string]json.RawMessage)
	}
	c.memos[key] = raw
	return raw, nil
}

func TestWaitForInteractionReplayPreservesConditions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := context.Background()
		ctx, cancel := context.WithTimeout(original, 5*time.Second)
		defer cancel()
		memoized := time.Now().Add(-time.Hour)
		durable := &waitContext{ctx: original}
		durable.now = func() (time.Time, error) {
			// A memo lookup takes time and returns the value from a prior invocation.
			time.Sleep(time.Second)
			return memoized, nil
		}
		var sleeps []string
		durable.wait = func(condition hatchet.Condition) (*worker.WaitResult, error) {
			sleeps = append(sleeps, condition.ToPB(0).SleepConditions[0].SleepFor)
			return nil, nil
		}
		for invocation := 0; invocation < 2; invocation++ {
			// Recreate the adapter on replay, retaining the server's memoized values.
			wait := interactionWait(durable, "session")
			for i := 0; i < 2; i++ {
				if err := wait(ctx, "form"); err != nil {
					t.Fatal(err)
				}
				if durable.GetContext() != original {
					t.Fatal("task context was not restored")
				}
			}
		}
		want := []string{"4000ms", "3000ms", "4000ms", "3000ms"}
		if !reflect.DeepEqual(sleeps, want) {
			t.Fatalf("sleeps = %v, want %v", sleeps, want)
		}
		// A replayed pending wait still exits at the real deadline, even though
		// its cached duration exceeds the time left.
		durable.now = func() (time.Time, error) { return memoized, nil }
		durable.wait = func(hatchet.Condition) (*worker.WaitResult, error) {
			<-durable.GetContext().Done()
			return nil, durable.GetContext().Err()
		}
		if err := interactionWait(durable, "session")(ctx, "form"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v, want deadline", err)
		}
		if durable.GetContext() != original {
			t.Fatal("task context was not restored")
		}
	})
}
