package resilience

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"
)

// RetryConfig holds retry configuration
type RetryConfig struct {
	MaxAttempts     int           `yaml:"max_attempts" default:"5"`
	InitialDelay    time.Duration `yaml:"initial_delay" default:"1s"`
	MaxDelay        time.Duration `yaml:"max_delay" default:"60s"`
	BackoffFactor   float64       `yaml:"backoff_factor" default:"2.0"`
	Jitter          bool          `yaml:"jitter" default:"true"`
	RetryableErrors []error       `yaml:"-"`
}

// RetryableFunc is a function that can be retried
type RetryableFunc func() error

// RetryableFuncWithResult is a function that returns a result and can be retried
type RetryableFuncWithResult func() (interface{}, error)

// Retryer handles retry logic with exponential backoff
type Retryer struct {
	config RetryConfig
}

// NewRetryer creates a new retryer with the given configuration
func NewRetryer(config RetryConfig) *Retryer {
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 5
	}
	if config.InitialDelay <= 0 {
		config.InitialDelay = time.Second
	}
	if config.MaxDelay <= 0 {
		config.MaxDelay = 60 * time.Second
	}
	if config.BackoffFactor <= 0 {
		config.BackoffFactor = 2.0
	}

	return &Retryer{config: config}
}

// Execute executes a function with retry logic
func (r *Retryer) Execute(ctx context.Context, fn RetryableFunc) error {
	var lastErr error

	for attempt := 1; attempt <= r.config.MaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !r.isRetryable(err) {
			return fmt.Errorf("non-retryable error: %w", err)
		}

		// Don't sleep after the last attempt
		if attempt == r.config.MaxAttempts {
			break
		}

		delay := r.calculateDelay(attempt)
		
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}

	return fmt.Errorf("max retry attempts (%d) exceeded, last error: %w", r.config.MaxAttempts, lastErr)
}

// ExecuteWithResult executes a function with retry logic and returns a result
func (r *Retryer) ExecuteWithResult(ctx context.Context, fn func() (interface{}, error)) (interface{}, error) {
	var lastErr error
	var result interface{}

	for attempt := 1; attempt <= r.config.MaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		res, err := fn()
		if err == nil {
			return res, nil
		}

		lastErr = err

		// Check if error is retryable
		if !r.isRetryable(err) {
			return result, fmt.Errorf("non-retryable error: %w", err)
		}

		// Don't sleep after the last attempt
		if attempt == r.config.MaxAttempts {
			break
		}

		delay := r.calculateDelay(attempt)
		
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(delay):
		}
	}

	return result, fmt.Errorf("max retry attempts (%d) exceeded, last error: %w", r.config.MaxAttempts, lastErr)
}

// calculateDelay calculates the delay for the given attempt using exponential backoff
func (r *Retryer) calculateDelay(attempt int) time.Duration {
	delay := float64(r.config.InitialDelay) * math.Pow(r.config.BackoffFactor, float64(attempt-1))
	
	// Apply maximum delay limit
	if delay > float64(r.config.MaxDelay) {
		delay = float64(r.config.MaxDelay)
	}

	// Apply jitter to avoid thundering herd
	if r.config.Jitter {
		jitter := rand.Float64() * 0.1 * delay // 10% jitter
		delay += jitter
	}

	return time.Duration(delay)
}

// isRetryable checks if an error is retryable
func (r *Retryer) isRetryable(err error) bool {
	// If no specific retryable errors are configured, retry all errors
	if len(r.config.RetryableErrors) == 0 {
		return true
	}

	// Check if the error matches any of the configured retryable errors
	for _, retryableErr := range r.config.RetryableErrors {
		if errors.Is(err, retryableErr) {
			return true
		}
	}

	return false
}

// RetryWithCircuitBreaker combines retry logic with circuit breaker
func RetryWithCircuitBreaker(ctx context.Context, retryer *Retryer, cb *CircuitBreaker, fn RetryableFunc) error {
	return retryer.Execute(ctx, func() error {
		return cb.Execute(ctx, fn)
	})
}

// RetryWithCircuitBreakerAndResult combines retry logic with circuit breaker for functions with results
func RetryWithCircuitBreakerAndResult(ctx context.Context, retryer *Retryer, cb *CircuitBreaker, fn RetryableFuncWithResult) (interface{}, error) {
	return retryer.ExecuteWithResult(ctx, func() (interface{}, error) {
		var result interface{}
		err := cb.Execute(ctx, func() error {
			var err error
			result, err = fn()
			return err
		})
		return result, err
	})
}

// Common retryable errors for Google Maps scraping
var (
	ErrRateLimited     = errors.New("rate limited")
	ErrTimeout         = errors.New("timeout")
	ErrNetworkError    = errors.New("network error")
	ErrServiceUnavailable = errors.New("service unavailable")
	ErrDatabaseConnection = errors.New("database connection error")
)

// DefaultRetryableErrors returns a list of common retryable errors
func DefaultRetryableErrors() []error {
	return []error{
		ErrRateLimited,
		ErrTimeout,
		ErrNetworkError,
		ErrServiceUnavailable,
		ErrDatabaseConnection,
	}
}

// NewDefaultRetryer creates a retryer with sensible defaults for Google Maps scraping
func NewDefaultRetryer() *Retryer {
	return NewRetryer(RetryConfig{
		MaxAttempts:     5,
		InitialDelay:    time.Second,
		MaxDelay:        60 * time.Second,
		BackoffFactor:   2.0,
		Jitter:          true,
		RetryableErrors: DefaultRetryableErrors(),
	})
}

// NewEnterpriseRetryer creates a retryer optimized for enterprise workloads
func NewEnterpriseRetryer() *Retryer {
	return NewRetryer(RetryConfig{
		MaxAttempts:     10,
		InitialDelay:    500 * time.Millisecond,
		MaxDelay:        30 * time.Second,
		BackoffFactor:   1.5,
		Jitter:          true,
		RetryableErrors: DefaultRetryableErrors(),
	})
}