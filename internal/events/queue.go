package events

import (
	"sync"
	"time"
)

// QueueSize is how many events wait for the writer at most; an event that
// finds the queue full is dropped (logged once until the queue drains).
const QueueSize = 256

// Queue hands events to a Log on its own goroutine, in order. The
// regulation loop records through it: an event never waits for the file
// lock or a slow disk, and a write failure cannot reach the loop. Reads go
// to the Log directly.
type Queue struct {
	log    *Log
	logger Logger
	now    func() time.Time
	ch     chan queued
	done   chan struct{}

	mu     sync.Mutex
	closed bool
	warned bool
}

// queued is an event, or (flush != nil) a marker the writer closes once
// everything before it is written.
type queued struct {
	e     Event
	flush chan struct{}
}

// NewQueue starts the writer for l. logger nil = silent.
func NewQueue(l *Log, logger Logger) *Queue {
	if logger == nil {
		logger = nopLogger{}
	}
	q := &Queue{log: l, logger: logger, now: time.Now, ch: make(chan queued, QueueSize), done: make(chan struct{})}
	go q.run()
	return q
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

func (q *Queue) run() {
	defer close(q.done)
	for it := range q.ch {
		if it.flush != nil {
			close(it.flush)
			continue
		}
		q.write(it.e)
	}
}

// write stores one event; a panic in the store costs the event, not the
// writer.
func (q *Queue) write(e Event) {
	defer func() {
		if r := recover(); r != nil {
			q.logger.Printf("event log: %v (event dropped)", r)
		}
	}()
	q.log.Add(e)
}

// Add queues e with its time taken now (a zero TS). It never blocks.
func (q *Queue) Add(e Event) {
	if e.TS == 0 {
		e.TS = q.now().Unix()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	select {
	case q.ch <- queued{e: e}:
		q.warned = false
	default:
		if !q.warned {
			q.warned = true
			q.logger.Printf("event log: queue full, %s event dropped", e.Kind)
		}
	}
}

// Flush waits until every event queued before the call is written, or
// until wait passed. Tests and the CLI use it.
func (q *Queue) Flush(wait time.Duration) {
	f := make(chan struct{})
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	select {
	case q.ch <- queued{flush: f}:
	default:
		q.mu.Unlock()
		return
	}
	q.mu.Unlock()
	select {
	case <-f:
	case <-time.After(wait):
	}
}

// Close stops taking events and waits up to wait for the queued ones to be
// written. Safe to call more than once.
func (q *Queue) Close(wait time.Duration) {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
	q.mu.Unlock()
	select {
	case <-q.done:
	case <-time.After(wait):
	}
}

// Log is the log the queue writes to.
func (q *Queue) Log() *Log { return q.log }
