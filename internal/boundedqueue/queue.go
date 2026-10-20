// Package boundedqueue bounds retained work by both entry count and weight.
package boundedqueue

import (
	"errors"
	"sync"
)

type Rejection string

const (
	Accepted   Rejection = ""
	CountLimit Rejection = "count_limit"
	ByteLimit  Rejection = "byte_limit"
	Oversize   Rejection = "oversize"
)

type Snapshot struct {
	Depth         int   `json:"depth"`
	Capacity      int   `json:"capacity"`
	RetainedBytes int64 `json:"retained_bytes"`
	MaxBytes      int64 `json:"max_bytes"`
}

type Queue[T any] struct {
	mu          sync.Mutex
	items       chan *Item[T]
	used, limit int64
}

// Item transfers ownership to one consumer. Done clears its value before
// releasing the reservation. Weight remains charged while work is in flight.
type Item[T any] struct {
	Value   T
	once    sync.Once
	release func()
}

func (i *Item[T]) Done() {
	if i == nil {
		return
	}
	i.once.Do(func() { var zero T; i.Value = zero; i.release() })
}

func New[T any](capacity int, maxBytes int64) (*Queue[T], error) {
	if capacity <= 0 || maxBytes <= 0 {
		return nil, errors.New("queue capacity and byte limit must be positive")
	}
	return &Queue[T]{items: make(chan *Item[T], capacity), limit: maxBytes}, nil
}

func (q *Queue[T]) Items() <-chan *Item[T] {
	if q == nil {
		return nil
	}
	return q.items
}

func (q *Queue[T]) TryPush(value T, weight int64) Rejection {
	if q == nil {
		return CountLimit
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if weight < 0 || weight > q.limit {
		return Oversize
	}
	if weight > q.limit-q.used {
		return ByteLimit
	}
	q.used += weight
	item := &Item[T]{Value: value, release: func() { q.mu.Lock(); q.used -= weight; q.mu.Unlock() }}
	select {
	case q.items <- item:
		return Accepted
	default:
		q.used -= weight
		return CountLimit
	}
}

func (q *Queue[T]) SetMaxBytes(limit int64) error {
	if limit <= 0 {
		return errors.New("queue byte limit must be positive")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.limit = limit
	return nil
}

func (q *Queue[T]) Snapshot() Snapshot {
	if q == nil {
		return Snapshot{}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return Snapshot{Depth: len(q.items), Capacity: cap(q.items), RetainedBytes: q.used, MaxBytes: q.limit}
}
