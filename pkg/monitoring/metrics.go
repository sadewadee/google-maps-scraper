package monitoring

import (
	"sync"
	"time"
)

// MetricType represents the type of metric
type MetricType int

const (
	MetricTypeCounter MetricType = iota
	MetricTypeGauge
	MetricTypeHistogram
	MetricTypeSummary
)

// Metric represents a single metric
type Metric struct {
	Name        string            `json:"name"`
	Type        MetricType        `json:"type"`
	Value       float64           `json:"value"`
	Labels      map[string]string `json:"labels"`
	Timestamp   time.Time         `json:"timestamp"`
	Description string            `json:"description"`
}

// MetricsCollector collects and manages metrics
type MetricsCollector struct {
	mu      sync.RWMutex
	metrics map[string]*Metric
	
	// Performance counters
	jobsProcessed    int64
	jobsSucceeded    int64
	jobsFailed       int64
	totalDataPoints  int64
	
	// Timing metrics
	avgResponseTime  float64
	maxResponseTime  float64
	minResponseTime  float64
	
	// Resource metrics
	memoryUsage      float64
	cpuUsage         float64
	diskUsage        float64
	
	// Database metrics
	dbConnections    int64
	dbQueryTime      float64
	dbErrors         int64
	
	// Circuit breaker metrics
	circuitBreakerState map[string]string
	
	// Health metrics
	healthyServices  int64
	totalServices    int64
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		metrics:             make(map[string]*Metric),
		circuitBreakerState: make(map[string]string),
		minResponseTime:     float64(^uint(0) >> 1), // Max float64
	}
}

// IncrementCounter increments a counter metric
func (mc *MetricsCollector) IncrementCounter(name string, labels map[string]string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	key := mc.getMetricKey(name, labels)
	metric, exists := mc.metrics[key]
	if !exists {
		metric = &Metric{
			Name:   name,
			Type:   MetricTypeCounter,
			Labels: labels,
		}
		mc.metrics[key] = metric
	}
	
	metric.Value++
	metric.Timestamp = time.Now()
}

// SetGauge sets a gauge metric value
func (mc *MetricsCollector) SetGauge(name string, value float64, labels map[string]string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	key := mc.getMetricKey(name, labels)
	metric := &Metric{
		Name:      name,
		Type:      MetricTypeGauge,
		Value:     value,
		Labels:    labels,
		Timestamp: time.Now(),
	}
	
	mc.metrics[key] = metric
}

// RecordHistogram records a histogram metric
func (mc *MetricsCollector) RecordHistogram(name string, value float64, labels map[string]string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	key := mc.getMetricKey(name, labels)
	metric, exists := mc.metrics[key]
	if !exists {
		metric = &Metric{
			Name:   name,
			Type:   MetricTypeHistogram,
			Labels: labels,
		}
		mc.metrics[key] = metric
	}
	
	// Simple histogram implementation - in production, use proper buckets
	metric.Value = value
	metric.Timestamp = time.Now()
}

// getMetricKey generates a unique key for a metric
func (mc *MetricsCollector) getMetricKey(name string, labels map[string]string) string {
	key := name
	for k, v := range labels {
		key += "_" + k + "_" + v
	}
	return key
}

// GetMetrics returns all collected metrics
func (mc *MetricsCollector) GetMetrics() map[string]*Metric {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	
	result := make(map[string]*Metric)
	for k, v := range mc.metrics {
		metricCopy := *v
		result[k] = &metricCopy
	}
	
	return result
}

// Performance tracking methods

// RecordJobProcessed records a processed job
func (mc *MetricsCollector) RecordJobProcessed(success bool, duration time.Duration, dataPoints int) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	mc.jobsProcessed++
	mc.totalDataPoints += int64(dataPoints)
	
	if success {
		mc.jobsSucceeded++
	} else {
		mc.jobsFailed++
	}
	
	// Update response time metrics
	durationMs := float64(duration.Milliseconds())
	if durationMs > mc.maxResponseTime {
		mc.maxResponseTime = durationMs
	}
	if durationMs < mc.minResponseTime {
		mc.minResponseTime = durationMs
	}
	
	// Simple moving average for response time
	if mc.avgResponseTime == 0 {
		mc.avgResponseTime = durationMs
	} else {
		mc.avgResponseTime = (mc.avgResponseTime + durationMs) / 2
	}
	
	// Update metrics
	mc.updatePerformanceMetrics()
}

// updatePerformanceMetrics updates performance-related metrics
func (mc *MetricsCollector) updatePerformanceMetrics() {
	now := time.Now()
	
	// Jobs per minute calculation
	if mc.jobsProcessed > 0 {
		mc.metrics["jobs_per_minute"] = &Metric{
			Name:      "jobs_per_minute",
			Type:      MetricTypeGauge,
			Value:     float64(mc.jobsProcessed), // Simplified - should be calculated over time window
			Timestamp: now,
		}
	}
	
	// Success rate
	if mc.jobsProcessed > 0 {
		successRate := float64(mc.jobsSucceeded) / float64(mc.jobsProcessed) * 100
		mc.metrics["success_rate"] = &Metric{
			Name:      "success_rate",
			Type:      MetricTypeGauge,
			Value:     successRate,
			Timestamp: now,
		}
	}
	
	// Error rate
	if mc.jobsProcessed > 0 {
		errorRate := float64(mc.jobsFailed) / float64(mc.jobsProcessed) * 100
		mc.metrics["error_rate"] = &Metric{
			Name:      "error_rate",
			Type:      MetricTypeGauge,
			Value:     errorRate,
			Timestamp: now,
		}
	}
	
	// Response time metrics
	mc.metrics["avg_response_time"] = &Metric{
		Name:      "avg_response_time",
		Type:      MetricTypeGauge,
		Value:     mc.avgResponseTime,
		Timestamp: now,
	}
	
	mc.metrics["max_response_time"] = &Metric{
		Name:      "max_response_time",
		Type:      MetricTypeGauge,
		Value:     mc.maxResponseTime,
		Timestamp: now,
	}
	
	mc.metrics["min_response_time"] = &Metric{
		Name:      "min_response_time",
		Type:      MetricTypeGauge,
		Value:     mc.minResponseTime,
		Timestamp: now,
	}
	
	// Total data points
	mc.metrics["total_data_points"] = &Metric{
		Name:      "total_data_points",
		Type:      MetricTypeCounter,
		Value:     float64(mc.totalDataPoints),
		Timestamp: now,
	}
}

// Resource monitoring methods

// UpdateResourceUsage updates resource usage metrics
func (mc *MetricsCollector) UpdateResourceUsage(memoryMB, cpuPercent, diskPercent float64) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	mc.memoryUsage = memoryMB
	mc.cpuUsage = cpuPercent
	mc.diskUsage = diskPercent
	
	now := time.Now()
	
	mc.metrics["memory_usage_mb"] = &Metric{
		Name:      "memory_usage_mb",
		Type:      MetricTypeGauge,
		Value:     memoryMB,
		Timestamp: now,
	}
	
	mc.metrics["cpu_usage_percent"] = &Metric{
		Name:      "cpu_usage_percent",
		Type:      MetricTypeGauge,
		Value:     cpuPercent,
		Timestamp: now,
	}
	
	mc.metrics["disk_usage_percent"] = &Metric{
		Name:      "disk_usage_percent",
		Type:      MetricTypeGauge,
		Value:     diskPercent,
		Timestamp: now,
	}
}

// Database monitoring methods

// RecordDatabaseOperation records a database operation
func (mc *MetricsCollector) RecordDatabaseOperation(duration time.Duration, success bool) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	durationMs := float64(duration.Milliseconds())
	mc.dbQueryTime = (mc.dbQueryTime + durationMs) / 2 // Simple moving average
	
	if !success {
		mc.dbErrors++
	}
	
	now := time.Now()
	
	mc.metrics["db_query_time_ms"] = &Metric{
		Name:      "db_query_time_ms",
		Type:      MetricTypeGauge,
		Value:     mc.dbQueryTime,
		Timestamp: now,
	}
	
	mc.metrics["db_errors"] = &Metric{
		Name:      "db_errors",
		Type:      MetricTypeCounter,
		Value:     float64(mc.dbErrors),
		Timestamp: now,
	}
}

// UpdateDatabaseConnections updates database connection count
func (mc *MetricsCollector) UpdateDatabaseConnections(count int64) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	mc.dbConnections = count
	
	mc.metrics["db_connections"] = &Metric{
		Name:      "db_connections",
		Type:      MetricTypeGauge,
		Value:     float64(count),
		Timestamp: time.Now(),
	}
}

// Circuit breaker monitoring

// UpdateCircuitBreakerState updates circuit breaker state
func (mc *MetricsCollector) UpdateCircuitBreakerState(name, state string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	mc.circuitBreakerState[name] = state
	
	// Convert state to numeric value for metrics
	var stateValue float64
	switch state {
	case "CLOSED":
		stateValue = 0
	case "OPEN":
		stateValue = 1
	case "HALF_OPEN":
		stateValue = 0.5
	}
	
	mc.metrics["circuit_breaker_"+name] = &Metric{
		Name:      "circuit_breaker_" + name,
		Type:      MetricTypeGauge,
		Value:     stateValue,
		Labels:    map[string]string{"circuit": name, "state": state},
		Timestamp: time.Now(),
	}
}

// Health monitoring

// UpdateHealthStatus updates health status metrics
func (mc *MetricsCollector) UpdateHealthStatus(healthy, total int64) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	mc.healthyServices = healthy
	mc.totalServices = total
	
	now := time.Now()
	
	mc.metrics["healthy_services"] = &Metric{
		Name:      "healthy_services",
		Type:      MetricTypeGauge,
		Value:     float64(healthy),
		Timestamp: now,
	}
	
	mc.metrics["total_services"] = &Metric{
		Name:      "total_services",
		Type:      MetricTypeGauge,
		Value:     float64(total),
		Timestamp: now,
	}
	
	// Health percentage
	if total > 0 {
		healthPercent := float64(healthy) / float64(total) * 100
		mc.metrics["health_percentage"] = &Metric{
			Name:      "health_percentage",
			Type:      MetricTypeGauge,
			Value:     healthPercent,
			Timestamp: now,
		}
	}
}

// GetPerformanceStats returns current performance statistics
func (mc *MetricsCollector) GetPerformanceStats() map[string]interface{} {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	
	return map[string]interface{}{
		"jobs_processed":     mc.jobsProcessed,
		"jobs_succeeded":     mc.jobsSucceeded,
		"jobs_failed":        mc.jobsFailed,
		"total_data_points":  mc.totalDataPoints,
		"avg_response_time":  mc.avgResponseTime,
		"max_response_time":  mc.maxResponseTime,
		"min_response_time":  mc.minResponseTime,
		"memory_usage_mb":    mc.memoryUsage,
		"cpu_usage_percent":  mc.cpuUsage,
		"disk_usage_percent": mc.diskUsage,
		"db_connections":     mc.dbConnections,
		"db_query_time_ms":   mc.dbQueryTime,
		"db_errors":          mc.dbErrors,
		"healthy_services":   mc.healthyServices,
		"total_services":     mc.totalServices,
	}
}

// Reset resets all metrics
func (mc *MetricsCollector) Reset() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	mc.metrics = make(map[string]*Metric)
	mc.jobsProcessed = 0
	mc.jobsSucceeded = 0
	mc.jobsFailed = 0
	mc.totalDataPoints = 0
	mc.avgResponseTime = 0
	mc.maxResponseTime = 0
	mc.minResponseTime = float64(^uint(0) >> 1)
	mc.memoryUsage = 0
	mc.cpuUsage = 0
	mc.diskUsage = 0
	mc.dbConnections = 0
	mc.dbQueryTime = 0
	mc.dbErrors = 0
	mc.circuitBreakerState = make(map[string]string)
	mc.healthyServices = 0
	mc.totalServices = 0
}