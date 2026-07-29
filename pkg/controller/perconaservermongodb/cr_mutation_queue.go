package perconaservermongodb

import (
	"context"
	stderrors "errors"
	"sort"
	"sync"
)

// crMutation represents a deferred mutation to the in-memory CR (and any
// associated Kubernetes API calls, e.g. a Patch). Mutations are enqueued by
// workers during parallel replset reconciliation and applied serially after the
// worker pool drains.
type crMutation struct {
	// name is a stable key used for logging and for deterministic ordering
	// when goroutine arrival order makes enqueue order nondeterministic.
	// Format: "<replset>/<operation>/<resource>" e.g. "rs0/triggerResize/mongod-data-rs0-0".
	name string
	// apply executes the mutation. It runs in the serial drain phase, so it
	// may safely read/write the CR and issue Kubernetes API calls.
	apply func(ctx context.Context) error
}

// crMutationQueue collects CR mutations from concurrent workers and applies
// them serially in a deterministic order after the worker pool drains.
//
// Ordering: mutations are sorted by name before applying, so the outcome is
// deterministic regardless of goroutine scheduling. The name field must be
// chosen to be unique and stable (e.g. include the replset name and PVC name).
type crMutationQueue struct {
	mu  sync.Mutex
	ops []crMutation
}

// Enqueue adds a mutation to the queue. Safe for concurrent use.
func (q *crMutationQueue) Enqueue(m crMutation) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ops = append(q.ops, m)
}

// Drain applies all enqueued mutations in sorted-by-name order and returns a
// joined error of any that failed. The queue is emptied after draining.
func (q *crMutationQueue) Drain(ctx context.Context) error {
	q.mu.Lock()
	ops := q.ops
	q.ops = nil
	q.mu.Unlock()

	if len(ops) == 0 {
		return nil
	}

	// Sort by name to ensure deterministic application order regardless of
	// goroutine arrival order.
	sort.Slice(ops, func(i, j int) bool {
		return ops[i].name < ops[j].name
	})

	var errs []error
	for _, op := range ops {
		if err := op.apply(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	return stderrors.Join(errs...)
}

// crMutationQueueKey is the unexported context key for the mutation queue.
type crMutationQueueKey struct{}

// withCRMutationQueue returns a child context carrying the given mutation queue.
func withCRMutationQueue(ctx context.Context, q *crMutationQueue) context.Context {
	return context.WithValue(ctx, crMutationQueueKey{}, q)
}

// crMutationQueueFrom retrieves the mutation queue from ctx. Returns nil if no
// queue is present (e.g. callers outside the parallel pool, or existing unit
// tests that do not set up a queue).
func crMutationQueueFrom(ctx context.Context) *crMutationQueue {
	q, _ := ctx.Value(crMutationQueueKey{}).(*crMutationQueue)
	return q
}
