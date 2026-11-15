package event

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	// ErrBusStopped is returned when attempting to publish to a stopped event bus.
	ErrBusStopped = errors.New("event bus is stopped")
	// ErrPublishTimeout is returned when publishing times out due to channel congestion.
	ErrPublishTimeout = errors.New("event bus publish timed out: channel buffer full")
)

// EventHandler is a callback invoked when a subscribed event arrives.
type EventHandler func(ctx context.Context, event TenantLifecycleEvent) error

// EventBus provides in-process, thread-safe, buffered asynchronous event dispatching.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[EventType]map[uint64]EventHandler
	subIDGen    uint64
	ch          chan TenantLifecycleEvent
	capacity    int
	closed      bool
	stopOnce    sync.Once
	wg          sync.WaitGroup
	handlersWg  sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
}

var (
	defaultBusMu sync.RWMutex
	defaultBus   *EventBus
)

func init() {
	defaultBus = NewEventBus(1024)
	defaultBus.Start(context.Background())
}

// GetDefaultBus returns the singleton global EventBus instance.
func GetDefaultBus() *EventBus {
	defaultBusMu.RLock()
	defer defaultBusMu.RUnlock()
	return defaultBus
}

// SetDefaultBus replaces the singleton global EventBus instance (primarily for tests).
func SetDefaultBus(bus *EventBus) {
	defaultBusMu.Lock()
	defer defaultBusMu.Unlock()
	defaultBus = bus
}

// NewEventBus instantiates a new EventBus with the specified channel capacity.
func NewEventBus(capacity int) *EventBus {
	if capacity <= 0 {
		capacity = 1024
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &EventBus{
		subscribers: make(map[EventType]map[uint64]EventHandler),
		ch:          make(chan TenantLifecycleEvent, capacity),
		capacity:    capacity,
		ctx:         ctx,
		cancel:      cancel,
	}
}

// Subscribe registers an event handler for a specific EventType.
// Returns an unsubscribe function that removes the registered handler.
func (b *EventBus) Subscribe(eventType EventType, handler EventHandler) func() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.subIDGen++
	id := b.subIDGen

	if b.subscribers[eventType] == nil {
		b.subscribers[eventType] = make(map[uint64]EventHandler)
	}
	b.subscribers[eventType][id] = handler

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if handlers, ok := b.subscribers[eventType]; ok {
			delete(handlers, id)
			if len(handlers) == 0 {
				delete(b.subscribers, eventType)
			}
		}
	}
}

// Publish enqueues an event into the bus buffer in a non-blocking manner.
// Returns true if successfully enqueued, false if the buffer is full or the bus is stopped.
func (b *EventBus) Publish(event TenantLifecycleEvent) (published bool) {
	defer func() {
		if r := recover(); r != nil {
			published = false
		}
	}()

	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return false
	}

	select {
	case b.ch <- event:
		return true
	default:
		return false
	}
}

// PublishWithTimeout attempts to publish an event within a timeout limit.
func (b *EventBus) PublishWithTimeout(ctx context.Context, event TenantLifecycleEvent, timeout time.Duration) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = ErrBusStopped
		}
	}()

	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return ErrBusStopped
	}
	b.mu.RUnlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.ctx.Done():
		return ErrBusStopped
	case <-timer.C:
		return ErrPublishTimeout
	case b.ch <- event:
		return nil
	}
}

// Start launches the background event consumption loop.
func (b *EventBus) Start(ctx context.Context) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for {
			select {
			case <-ctx.Done():
				b.drain()
				return
			case <-b.ctx.Done():
				b.drain()
				return
			case evt, ok := <-b.ch:
				if !ok {
					return
				}
				b.dispatch(evt)
			}
		}
	}()
}

// drain dispatches any accepted events remaining in the channel buffer before exit.
func (b *EventBus) drain() {
	for {
		select {
		case evt, ok := <-b.ch:
			if !ok {
				return
			}
			b.dispatch(evt)
		default:
			return
		}
	}
}

// dispatch routes an event to all registered handlers for its type.
func (b *EventBus) dispatch(event TenantLifecycleEvent) {
	b.mu.RLock()
	var allHandlers []EventHandler
	if m, ok := b.subscribers[event.Type]; ok {
		for _, h := range m {
			allHandlers = append(allHandlers, h)
		}
	}
	if m, ok := b.subscribers["*"]; ok {
		for _, h := range m {
			allHandlers = append(allHandlers, h)
		}
	}
	b.mu.RUnlock()

	for _, h := range allHandlers {
		if h != nil {
			b.handlersWg.Add(1)
			go func(handler EventHandler) {
				defer b.handlersWg.Done()
				// Prevent panics in handlers from crashing the bus
				defer func() { _ = recover() }()
				_ = handler(context.Background(), event)
			}(h)
		}
	}
}

// Stop gracefully shuts down the event bus, stopping reception and dispatching.
func (b *EventBus) Stop() {
	b.stopOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()

		b.cancel()
		close(b.ch)
		b.wg.Wait()
		b.handlersWg.Wait()
	})
}

// PendingCount returns the number of unconsumed events currently in the buffer.
func (b *EventBus) PendingCount() int {
	return len(b.ch)
}
