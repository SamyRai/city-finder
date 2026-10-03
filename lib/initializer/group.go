package initializer

import (
	"context"
	"sync"
)

// group runs a small fixed set of steps concurrently and reports the first
// failure, like golang.org/x/sync/errgroup (not a dependency of this module,
// and the needs here are small). It differs from errgroup on purpose: a
// failing step does not cancel its siblings. Every step the initializer runs
// this way is either an atomic file write or a file parse that cannot be
// interrupted, and letting siblings finish keeps the outcome deterministic
// (a failed boot leaves the sibling indexes written, so only the failed one
// is redone next time). Two properties matter:
//
//   - a step starts only while ctx is live, so cancelling the boot skips
//     steps that have not begun;
//   - Wait returns after every started step has returned, so no goroutine
//     outlives a failed boot and the caller can safely touch shared state (or
//     retry) afterwards.
type group struct {
	ctx  context.Context
	wg   sync.WaitGroup
	once sync.Once
	err  error
}

// newGroup returns a group whose steps run under ctx.
func newGroup(ctx context.Context) *group { return &group{ctx: ctx} }

// Go starts step in its own goroutine. A step whose context is already
// cancelled is not run and counts as failing with the context's error.
func (g *group) Go(step func(ctx context.Context) error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		err := g.ctx.Err()
		if err == nil {
			err = step(g.ctx)
		}
		if err != nil {
			g.once.Do(func() { g.err = err })
		}
	}()
}

// Wait blocks until every step has returned and yields the first error in
// the order the failures happened.
func (g *group) Wait() error {
	g.wg.Wait()
	return g.err
}
