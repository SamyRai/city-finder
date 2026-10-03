package builder

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settleGoroutines polls until the goroutine count is back at base, so a
// leaked step shows up as a failure rather than as flakiness elsewhere.
func settleGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines outlived the group: %d > %d\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGroup_AllSucceed(t *testing.T) {
	var ran atomic.Int32
	g := newGroup(context.Background())
	for range 3 {
		g.Go(func(context.Context) error { ran.Add(1); return nil })
	}
	require.NoError(t, g.Wait())
	assert.EqualValues(t, 3, ran.Load())
}

// TestGroup_FailureDoesNotCancelSiblingsAndWaitIsComplete: a failing step
// does not stop its siblings, Wait returns only after they have finished, and
// the failure is what it reports.
func TestGroup_FailureDoesNotCancelSiblingsAndWaitIsComplete(t *testing.T) {
	base := runtime.NumGoroutine()
	boom := errors.New("boom")
	var siblingOK atomic.Bool

	g := newGroup(context.Background())
	g.Go(func(ctx context.Context) error {
		time.Sleep(30 * time.Millisecond) // outlasts the failing step
		siblingOK.Store(ctx.Err() == nil)
		return nil
	})
	g.Go(func(context.Context) error { return boom })

	require.ErrorIs(t, g.Wait(), boom)
	assert.True(t, siblingOK.Load(), "the sibling must run to completion under a live context")
	settleGoroutines(t, base)
}

// TestGroup_CancelledParentSkipsSteps: steps never start under a cancelled
// context.
func TestGroup_CancelledParentSkipsSteps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran atomic.Bool
	g := newGroup(ctx)
	g.Go(func(context.Context) error { ran.Store(true); return nil })
	require.ErrorIs(t, g.Wait(), context.Canceled)
	assert.False(t, ran.Load())
}
