package resilience

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// HealthStatus represents the health status of a component
type HealthStatus int

const (
	HealthStatusHealthy HealthStatus = iota
	HealthStatusUnhealthy
	HealthStatusUnknown
)

// HealthCheck represents a single health check
type HealthCheck struct {
	Name        string
	CheckFunc   func(ctx context.Context) error
	Interval    time.Duration
	Timeout     time.Duration
	Threshold   int // Number of consecutive failures before marking unhealthy
	Critical    bool // Whether this check is critical for overall health
}

// HealthResult represents the result of a health check
type HealthResult struct {
	Name           string        `json:"name"`
	Status         HealthStatus  `json:"status"`
	Message        string        `json:"message"`
	LastChecked    time.Time     `json:"last_checked"`
	Duration       time.Duration `json:"duration"`
	FailureCount   int           `json:"failure_count"`
	ConsecutiveFails int         `json:"consecutive_fails"`
}

// HealthChecker manages multiple health checks
type HealthChecker struct {
	mu          sync.RWMutex
	checks      map[string]*HealthCheck
	results     map[string]*HealthResult
	running     bool
	stopCh      chan struct{}
	callbacks   []func(name string, result *HealthResult)
}

// NewHealthChecker creates a new health checker
func NewHealthChecker() *HealthChecker {
	return &HealthChecker{
		checks:  make(map[string]*HealthCheck),
		results: make(map[string]*HealthResult),
		stopCh:  make(chan struct{}),
	}
}

// AddCheck adds a health check
func (hc *HealthChecker) AddCheck(check *HealthCheck) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if check.Interval <= 0 {
		check.Interval = 30 * time.Second
	}
	if check.Timeout <= 0 {
		check.Timeout = 5 * time.Second
	}
	if check.Threshold <= 0 {
		check.Threshold = 3
	}

	hc.checks[check.Name] = check
	hc.results[check.Name] = &HealthResult{
		Name:   check.Name,
		Status: HealthStatusUnknown,
	}
}

// RemoveCheck removes a health check
func (hc *HealthChecker) RemoveCheck(name string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	delete(hc.checks, name)
	delete(hc.results, name)
}

// Start starts the health checker
func (hc *HealthChecker) Start(ctx context.Context) error {
	hc.mu.Lock()
	if hc.running {
		hc.mu.Unlock()
		return fmt.Errorf("health checker is already running")
	}
	hc.running = true
	hc.mu.Unlock()

	// Start health checks for each registered check
	for name, check := range hc.checks {
		go hc.runHealthCheck(ctx, name, check)
	}

	return nil
}

// Stop stops the health checker
func (hc *HealthChecker) Stop() {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if !hc.running {
		return
	}

	close(hc.stopCh)
	hc.running = false
	hc.stopCh = make(chan struct{})
}

// runHealthCheck runs a single health check in a loop
func (hc *HealthChecker) runHealthCheck(ctx context.Context, name string, check *HealthCheck) {
	ticker := time.NewTicker(check.Interval)
	defer ticker.Stop()

	// Run initial check
	hc.executeCheck(ctx, name, check)

	for {
		select {
		case <-ctx.Done():
			return
		case <-hc.stopCh:
			return
		case <-ticker.C:
			hc.executeCheck(ctx, name, check)
		}
	}
}

// executeCheck executes a single health check
func (hc *HealthChecker) executeCheck(ctx context.Context, name string, check *HealthCheck) {
	start := time.Now()
	
	// Create timeout context
	checkCtx, cancel := context.WithTimeout(ctx, check.Timeout)
	defer cancel()

	// Execute the check
	err := check.CheckFunc(checkCtx)
	duration := time.Since(start)

	// Update result
	hc.mu.Lock()
	result := hc.results[name]
	result.LastChecked = start
	result.Duration = duration

	if err != nil {
		result.FailureCount++
		result.ConsecutiveFails++
		result.Message = err.Error()
		
		if result.ConsecutiveFails >= check.Threshold {
			result.Status = HealthStatusUnhealthy
		}
	} else {
		result.ConsecutiveFails = 0
		result.Status = HealthStatusHealthy
		result.Message = "OK"
	}
	hc.mu.Unlock()

	// Call callbacks
	for _, callback := range hc.callbacks {
		go callback(name, result)
	}
}

// GetResult returns the result of a specific health check
func (hc *HealthChecker) GetResult(name string) (*HealthResult, bool) {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	result, exists := hc.results[name]
	if !exists {
		return nil, false
	}

	// Return a copy to avoid race conditions
	resultCopy := *result
	return &resultCopy, true
}

// GetAllResults returns all health check results
func (hc *HealthChecker) GetAllResults() map[string]*HealthResult {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	results := make(map[string]*HealthResult)
	for name, result := range hc.results {
		resultCopy := *result
		results[name] = &resultCopy
	}

	return results
}

// IsHealthy returns true if all critical checks are healthy
func (hc *HealthChecker) IsHealthy() bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	for name, check := range hc.checks {
		if !check.Critical {
			continue
		}

		result, exists := hc.results[name]
		if !exists || result.Status != HealthStatusHealthy {
			return false
		}
	}

	return true
}

// GetOverallStatus returns the overall health status
func (hc *HealthChecker) GetOverallStatus() HealthStatus {
	if hc.IsHealthy() {
		return HealthStatusHealthy
	}

	hc.mu.RLock()
	defer hc.mu.RUnlock()

	// Check if any critical check is unhealthy
	for name, check := range hc.checks {
		if !check.Critical {
			continue
		}

		result, exists := hc.results[name]
		if exists && result.Status == HealthStatusUnhealthy {
			return HealthStatusUnhealthy
		}
	}

	return HealthStatusUnknown
}

// AddCallback adds a callback function that will be called when health status changes
func (hc *HealthChecker) AddCallback(callback func(name string, result *HealthResult)) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.callbacks = append(hc.callbacks, callback)
}

// String returns a string representation of the health status
func (hs HealthStatus) String() string {
	switch hs {
	case HealthStatusHealthy:
		return "HEALTHY"
	case HealthStatusUnhealthy:
		return "UNHEALTHY"
	case HealthStatusUnknown:
		return "UNKNOWN"
	default:
		return "INVALID"
	}
}

// Common health checks for Google Maps scraper

// DatabaseHealthCheck creates a health check for database connectivity
func DatabaseHealthCheck(db interface{ Ping() error }) *HealthCheck {
	return &HealthCheck{
		Name:     "database",
		Interval: 30 * time.Second,
		Timeout:  5 * time.Second,
		Critical: true,
		CheckFunc: func(ctx context.Context) error {
			return db.Ping()
		},
	}
}

// MemoryHealthCheck creates a health check for memory usage
func MemoryHealthCheck(maxMemoryMB int64) *HealthCheck {
	return &HealthCheck{
		Name:     "memory",
		Interval: 10 * time.Second,
		Timeout:  1 * time.Second,
		Critical: false,
		CheckFunc: func(ctx context.Context) error {
			// This would need to be implemented with actual memory monitoring
			// For now, it's a placeholder
			return nil
		},
	}
}

// DiskSpaceHealthCheck creates a health check for disk space
func DiskSpaceHealthCheck(path string, minFreeGB int64) *HealthCheck {
	return &HealthCheck{
		Name:     "disk_space",
		Interval: 60 * time.Second,
		Timeout:  5 * time.Second,
		Critical: false,
		CheckFunc: func(ctx context.Context) error {
			// This would need to be implemented with actual disk space monitoring
			// For now, it's a placeholder
			return nil
		},
	}
}