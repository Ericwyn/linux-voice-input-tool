package stats

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

var ErrTrackerClosed = errors.New("statistics tracker is closed")

type commandKind uint8

const (
	commandRecord commandKind = iota
	commandFlush
	commandClose
)

type trackerCommand struct {
	kind  commandKind
	event Event
	reply chan error
}

type trackedDay struct {
	date     time.Time
	document DailyDocument
	dirty    bool
	blocked  error
}

// Tracker serializes daily-file updates on a background goroutine. Record only
// queues numeric data; disk reads, JSON encoding, fsync and rename happen away
// from the recording/transcription call path.
type Tracker struct {
	root string

	mu      sync.RWMutex
	enabled bool
	closed  bool

	commands  chan trackerCommand
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func NewTracker(root string, enabled bool) *Tracker {
	tracker := &Tracker{
		root:     root,
		enabled:  enabled,
		commands: make(chan trackerCommand, 64),
		done:     make(chan struct{}),
	}
	go tracker.run()
	return tracker
}

func (tracker *Tracker) Root() string { return tracker.root }

func (tracker *Tracker) Enabled() bool {
	if tracker == nil {
		return false
	}
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	return tracker.enabled && !tracker.closed
}

// SetEnabled pauses or resumes new event collection without deleting history.
func (tracker *Tracker) SetEnabled(enabled bool) {
	if tracker == nil {
		return
	}
	tracker.mu.Lock()
	if !tracker.closed {
		tracker.enabled = enabled
	}
	tracker.mu.Unlock()
}

// Record queues one successful paste. The buffered send may briefly wait only
// if more than 64 writes are already pending; it never waits for disk I/O.
func (tracker *Tracker) Record(event Event) {
	if tracker == nil {
		return
	}
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	if !tracker.enabled || tracker.closed {
		return
	}
	tracker.commands <- trackerCommand{kind: commandRecord, event: event}
}

// Flush waits until all events queued before this call have been applied and
// retries any dirty daily documents. It is used before interactive reads and on
// orderly shutdown, never on the normal voice-input success path.
func (tracker *Tracker) Flush() error {
	if tracker == nil {
		return nil
	}
	tracker.mu.RLock()
	defer tracker.mu.RUnlock()
	if tracker.closed {
		return ErrTrackerClosed
	}
	reply := make(chan error, 1)
	tracker.commands <- trackerCommand{kind: commandFlush, reply: reply}
	return <-reply
}

// Close flushes queued events and stops the writer. Concurrent calls share the
// same result and are safe.
func (tracker *Tracker) Close() error {
	if tracker == nil {
		return nil
	}
	tracker.closeOnce.Do(func() {
		tracker.mu.Lock()
		tracker.closed = true
		reply := make(chan error, 1)
		tracker.commands <- trackerCommand{kind: commandClose, reply: reply}
		tracker.mu.Unlock()

		tracker.closeErr = <-reply
		<-tracker.done
	})
	return tracker.closeErr
}

func (tracker *Tracker) run() {
	defer close(tracker.done)
	days := make(map[string]*trackedDay)
	for command := range tracker.commands {
		switch command.kind {
		case commandRecord:
			tracker.record(days, command.event)
		case commandFlush:
			command.reply <- tracker.flush(days)
		case commandClose:
			command.reply <- tracker.flush(days)
			return
		}
	}
}

func (tracker *Tracker) record(days map[string]*trackedDay, event Event) {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	date := dayStart(event.OccurredAt)
	key := date.Format(time.DateOnly)
	day, exists := days[key]
	if !exists {
		document, _, err := readDay(tracker.root, date)
		day = &trackedDay{date: date, document: document, blocked: err}
		days[key] = day
		if err != nil {
			log.Printf("Statistics file unavailable for %s: %v", key, err)
			return
		}
	}
	if day.blocked != nil {
		return
	}

	day.document.apply(event)
	day.dirty = true
	if err := tracker.persist(day); err != nil {
		log.Printf("Statistics write failed for %s: %v", key, err)
	}
}

func (tracker *Tracker) persist(day *trackedDay) error {
	if day == nil || day.blocked != nil || !day.dirty {
		return dayError(day)
	}
	if err := writeDay(tracker.root, day.date, day.document); err != nil {
		return err
	}
	day.dirty = false
	return nil
}

func (tracker *Tracker) flush(days map[string]*trackedDay) error {
	keys := make([]string, 0, len(days))
	for key := range days {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var failures []error
	for _, key := range keys {
		day := days[key]
		if day.blocked != nil {
			failures = append(failures, fmt.Errorf("statistics %s: %w", key, day.blocked))
			continue
		}
		if !day.dirty {
			continue
		}
		if err := tracker.persist(day); err != nil {
			failures = append(failures, fmt.Errorf("statistics %s: %w", key, err))
		}
	}
	return errors.Join(failures...)
}

func dayError(day *trackedDay) error {
	if day == nil {
		return nil
	}
	return day.blocked
}
