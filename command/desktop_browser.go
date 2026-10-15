package command

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// Closing the last browser window is normal desktop interaction. Keep the same
// profile and restart only the browser, without terminating running agent turns.
func runDesktopBrowser(ctx context.Context, command func() *exec.Cmd) error {
	delay := time.Second
	const maxDelay = 30 * time.Second
	for {
		started := time.Now()
		err := runChildUntilExit(ctx, command())
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exited *exec.ExitError
		if err != nil && !errors.As(err, &exited) {
			return err
		}
		if time.Since(started) >= maxDelay {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, maxDelay)
	}
}
