package resilience

import (
	"context"
	"errors"
	"sync"
	"time"
)

// CircuitBreakerState represents the state of the circuit breaker
type CircuitBreakerState int

const (
	StateClosed CircuitBreakerState = iota
	StateOpen
	StateHalfOpen
)

// CircuitBreaker implements the circuit breaker pattern for fault tolerance
type CircuitBreaker struct {
	mu                sync.RWMutex
	state             CircuitBreakerState
	failureCount      int
	successCount      int
	lastFailureTime   time.Time
	lastSuccessTime   time.Time
	
	// Configuration
	maxFailures       int
	timeout           time.Duration
	resetTimeout      time.Duration
	halfOpenMaxCalls  int
	
	// Callbacks
	onStateChange     func(from, to CircuitBreakerState)
}

// Config holds circuit breaker configuration
type Config struct {
	MaxFailures      int           `yaml:"max_failures" default:"10"`
	Timeout          time.Duration `yaml:"timeout" default:"60s"`
	ResetTimeout     time.Duration `yaml:"reset_timeout" default:"300s"`
	HalfOpenMaxCalls int           `yaml:"half_open_max_calls" default:"5"`
	OnStateChange    func(from, to CircuitBreakerState)
}

// NewCircuitBreaker creates a new circuit breaker with the given configuration
func NewCircuitBreaker(config Config) *CircuitBreaker {
	if config.MaxFailures <= 0 {
		config.MaxFailures = 10
	}
	if config.Timeout <= 0 {
		config.Timeout = 60 * time.Second
	}
	if config.ResetTimeout <= 0 {
		config.ResetTimeout = 300 * time.Second
	}
	if config.HalfOpenMaxCalls <= 0 {
		config.HalfOpenMaxCalls = 5
	}

	return &CircuitBreaker{
		state:            StateClosed,
		maxFailures:      config.MaxFailures,
		timeout:          config.Timeout,
		resetTimeout:     config.ResetTimeout,
		halfOpenMaxCalls: config.HalfOpenMaxCalls,
		onStateChange:    config.OnStateChange,
	}
}

// Execute runs the given function with circuit breaker protection
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func() error) error {
	if !cb.canExecute() {
		return ErrCircuitBreakerOpen
	}

	err := fn()
	cb.recordResult(err == nil)
	return err
}

// ExecuteWithFallback runs the function with circuit breaker and fallback
func (cb *CircuitBreaker) ExecuteWithFallback(ctx context.Context, fn func() error, fallback func() error) error {
	err := cb.Execute(ctx, fn)
	if errors.Is(err, ErrCircuitBreakerOpen) && fallback != nil {
		return fallback()
	}
	return err
}

// canExecute checks if the circuit breaker allows execution
func (cb *CircuitBreaker) canExecute() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if now.Sub(cb.lastFailureTime) >= cb.resetTimeout {
			cb.setState(StateHalfOpen)
			cb.successCount = 0
			return true
		}
		return false
	case StateHalfOpen:
		return cb.successCount < cb.halfOpenMaxCalls
	}

	return false
}

// recordResult records the result of an execution
func (cb *CircuitBreaker) recordResult(success bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()

	if success {
		cb.successCount++
		cb.lastSuccessTime = now

		switch cb.state {
		case StateHalfOpen:
			if cb.successCount >= cb.halfOpenMaxCalls {
				cb.setState(StateClosed)
				cb.failureCount = 0
			}
		case StateClosed:
			cb.failureCount = 0
		}
	} else {
		cb.failureCount++
		cb.lastFailureTime = now

		switch cb.state {
		case StateClosed:
			if cb.failureCount >= cb.maxFailures {
				cb.setState(StateOpen)
			}
		case StateHalfOpen:
			cb.setState(StateOpen)
		}
	}
}

// setState changes the circuit breaker state and calls the callback
func (cb *CircuitBreaker) setState(newState CircuitBreakerState) {
	oldState := cb.state
	cb.state = newState

	if cb.onStateChange != nil && oldState != newState {
		go cb.onStateChange(oldState, newState)
	}
}

// State returns the current state of the circuit breaker
func (cb *CircuitBreaker) State() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Stats returns statistics about the circuit breaker
func (cb *CircuitBreaker) Stats() Stats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return Stats{
		State:           cb.state,
		FailureCount:    cb.failureCount,
		SuccessCount:    cb.successCount,
		LastFailureTime: cb.lastFailureTime,
		LastSuccessTime: cb.lastSuccessTime,
	}
}

// Reset resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.setState(StateClosed)
	cb.failureCount = 0
	cb.successCount = 0
}

// Stats holds circuit breaker statistics
type Stats struct {
	State           CircuitBreakerState
	FailureCount    int
	SuccessCount    int
	LastFailureTime time.Time
	LastSuccessTime time.Time
}

// String returns a string representation of the state
func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}

// Errors
var (
	ErrCircuitBreakerOpen = errors.New("circuit breaker is open")
)