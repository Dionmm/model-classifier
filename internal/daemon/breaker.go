package daemon

import (
	"fmt"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
)

type breakerState string

const (
	breakerClosed   breakerState = "closed"
	breakerOpen     breakerState = "open"
	breakerHalfOpen breakerState = "half_open"
)

type circuitBreaker struct {
	mu            sync.Mutex
	state         breakerState
	failures      int
	openedUntil   time.Time
	probeInFlight bool
	clock         func() time.Time
}

func newBreaker(clock func() time.Time) *circuitBreaker {
	if clock == nil {
		clock = time.Now
	}
	return &circuitBreaker{state: breakerClosed, clock: clock}
}

func (b *circuitBreaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.state)
}

func (b *circuitBreaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	if b.state == breakerOpen {
		if now.Before(b.openedUntil) || b.probeInFlight {
			return false
		}
		b.state = breakerHalfOpen
		b.probeInFlight = true
		return true
	}
	if b.state == breakerHalfOpen {
		if b.probeInFlight {
			return false
		}
		b.probeInFlight = true
		return true
	}
	return true
}

func (b *circuitBreaker) Success(span trace.Span) {
	b.mu.Lock()
	defer b.mu.Unlock()
	old := b.state
	b.failures = 0
	b.state = breakerClosed
	b.probeInFlight = false
	if old != breakerClosed {
		logBreaker(old, b.state, span)
	}
}

func (b *circuitBreaker) Failure(span trace.Span) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.state == breakerHalfOpen || b.failures >= 3 {
		old := b.state
		b.state = breakerOpen
		b.openedUntil = b.clock().Add(30 * time.Second)
		b.probeInFlight = false
		if old != breakerOpen {
			logBreaker(old, b.state, span)
		}
	}
}

func (b *circuitBreaker) Ignore() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == breakerHalfOpen {
		b.probeInFlight = false
	}
}

func logBreaker(old, next breakerState, span trace.Span) {
	msg := fmt.Sprintf("model-routerd: circuit breaker %s -> %s\n", old, next)
	_, _ = os.Stderr.WriteString(msg)
	if span != nil {
		span.AddEvent("circuit_breaker", trace.WithAttributes())
	}
}
