// Package worklimit bounds resource use across concurrent channel workers.
// A context carries invocation-scoped limits into the source, model and encoder
// adapters. Commands without limits retain their existing behavior.
package worklimit

import "context"

type Resource int

const (
	YouTube Resource = iota
	Model
	Encode
)

type key struct{}
type limits struct {
	gates [3]chan struct{}
	stop  context.CancelCauseFunc
}

func With(ctx context.Context, youtube, model, encode int) (context.Context, context.CancelFunc) {
	ctx, stop := context.WithCancelCause(ctx)
	l := &limits{stop: stop}
	for i, n := range []int{youtube, model, encode} {
		l.gates[i] = make(chan struct{}, max(1, n))
	}
	return context.WithValue(ctx, key{}, l), func() { stop(context.Canceled) }
}

// Acquire waits without holding an adapter's own locks. Cancellation is checked
// again after admission so queued work cannot dispatch after the run stops.
func Acquire(ctx context.Context, resource Resource) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, ok := ctx.Value(key{}).(*limits)
	if !ok {
		return func() {}, nil
	}
	gate := l.gates[resource]
	select {
	case gate <- struct{}{}:
		release := func() { <-gate }
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Stop cancels sibling work immediately when a durable usage checkpoint fails.
func Stop(ctx context.Context, cause error) {
	if l, ok := ctx.Value(key{}).(*limits); ok {
		l.stop(cause)
	}
}
