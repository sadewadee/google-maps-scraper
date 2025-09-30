package monitoring

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// AlertSeverity represents the severity level of an alert
type AlertSeverity int

const (
	AlertSeverityInfo AlertSeverity = iota
	AlertSeverityWarning
	AlertSeverityCritical
)

// AlertRule defines a rule for triggering alerts
type AlertRule struct {
	Name        string        `json:"name"`
	MetricName  string        `json:"metric_name"`
	Condition   string        `json:"condition"` // "gt", "lt", "eq", "gte", "lte"
	Threshold   float64       `json:"threshold"`
	Duration    time.Duration `json:"duration"` // How long condition must be true
	Severity    AlertSeverity `json:"severity"`
	Description string        `json:"description"`
	Enabled     bool          `json:"enabled"`
}

// Alert represents an active alert
type Alert struct {
	ID          string        `json:"id"`
	Rule        *AlertRule    `json:"rule"`
	Value       float64       `json:"value"`
	StartTime   time.Time     `json:"start_time"`
	LastUpdate  time.Time     `json:"last_update"`
	Status      string        `json:"status"` // "firing", "resolved"
	Message     string        `json:"message"`
}

// NotificationChannel represents a notification channel
type NotificationChannel interface {
	Send(ctx context.Context, alert *Alert) error
	Name() string
}

// AlertManager manages alerts and notifications
type AlertManager struct {
	mu                  sync.RWMutex
	rules               map[string]*AlertRule
	activeAlerts        map[string]*Alert
	notificationChannels []NotificationChannel
	metricsCollector    *MetricsCollector
	
	// Alert state tracking
	ruleStates          map[string]*ruleState
	
	// Configuration
	evaluationInterval  time.Duration
	running            bool
	stopCh             chan struct{}
}

// ruleState tracks the state of an alert rule
type ruleState struct {
	conditionStartTime time.Time
	conditionMet       bool
	lastValue          float64
}

// NewAlertManager creates a new alert manager
func NewAlertManager(metricsCollector *MetricsCollector) *AlertManager {
	return &AlertManager{
		rules:               make(map[string]*AlertRule),
		activeAlerts:        make(map[string]*Alert),
		ruleStates:          make(map[string]*ruleState),
		metricsCollector:    metricsCollector,
		evaluationInterval:  30 * time.Second,
		stopCh:             make(chan struct{}),
	}
}

// AddRule adds an alert rule
func (am *AlertManager) AddRule(rule *AlertRule) {
	am.mu.Lock()
	defer am.mu.Unlock()
	
	am.rules[rule.Name] = rule
	am.ruleStates[rule.Name] = &ruleState{}
}

// RemoveRule removes an alert rule
func (am *AlertManager) RemoveRule(name string) {
	am.mu.Lock()
	defer am.mu.Unlock()
	
	delete(am.rules, name)
	delete(am.ruleStates, name)
	
	// Resolve any active alerts for this rule
	for alertID, alert := range am.activeAlerts {
		if alert.Rule.Name == name {
			alert.Status = "resolved"
			alert.LastUpdate = time.Now()
			delete(am.activeAlerts, alertID)
		}
	}
}

// AddNotificationChannel adds a notification channel
func (am *AlertManager) AddNotificationChannel(channel NotificationChannel) {
	am.mu.Lock()
	defer am.mu.Unlock()
	
	am.notificationChannels = append(am.notificationChannels, channel)
}

// Start starts the alert manager
func (am *AlertManager) Start(ctx context.Context) error {
	am.mu.Lock()
	if am.running {
		am.mu.Unlock()
		return fmt.Errorf("alert manager is already running")
	}
	am.running = true
	am.mu.Unlock()
	
	go am.evaluationLoop(ctx)
	return nil
}

// Stop stops the alert manager
func (am *AlertManager) Stop() {
	am.mu.Lock()
	defer am.mu.Unlock()
	
	if !am.running {
		return
	}
	
	close(am.stopCh)
	am.running = false
	am.stopCh = make(chan struct{})
}

// evaluationLoop runs the alert evaluation loop
func (am *AlertManager) evaluationLoop(ctx context.Context) {
	ticker := time.NewTicker(am.evaluationInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return
		case <-am.stopCh:
			return
		case <-ticker.C:
			am.evaluateRules(ctx)
		}
	}
}

// evaluateRules evaluates all alert rules
func (am *AlertManager) evaluateRules(ctx context.Context) {
	am.mu.RLock()
	rules := make(map[string]*AlertRule)
	for k, v := range am.rules {
		rules[k] = v
	}
	am.mu.RUnlock()
	
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		
		am.evaluateRule(ctx, rule)
	}
}

// evaluateRule evaluates a single alert rule
func (am *AlertManager) evaluateRule(ctx context.Context, rule *AlertRule) {
	// Get current metric value
	metrics := am.metricsCollector.GetMetrics()
	metric, exists := metrics[rule.MetricName]
	if !exists {
		return
	}
	
	// Check if condition is met
	conditionMet := am.checkCondition(rule.Condition, metric.Value, rule.Threshold)
	
	am.mu.Lock()
	state := am.ruleStates[rule.Name]
	now := time.Now()
	
	if conditionMet {
		if !state.conditionMet {
			// Condition just became true
			state.conditionMet = true
			state.conditionStartTime = now
		}
		
		// Check if condition has been true for the required duration
		if now.Sub(state.conditionStartTime) >= rule.Duration {
			am.fireAlert(ctx, rule, metric.Value)
		}
	} else {
		if state.conditionMet {
			// Condition just became false
			state.conditionMet = false
			am.resolveAlert(ctx, rule)
		}
	}
	
	state.lastValue = metric.Value
	am.mu.Unlock()
}

// checkCondition checks if a condition is met
func (am *AlertManager) checkCondition(condition string, value, threshold float64) bool {
	switch condition {
	case "gt":
		return value > threshold
	case "gte":
		return value >= threshold
	case "lt":
		return value < threshold
	case "lte":
		return value <= threshold
	case "eq":
		return value == threshold
	default:
		return false
	}
}

// fireAlert fires an alert
func (am *AlertManager) fireAlert(ctx context.Context, rule *AlertRule, value float64) {
	alertID := fmt.Sprintf("%s_%d", rule.Name, time.Now().Unix())
	
	// Check if alert is already active
	for _, alert := range am.activeAlerts {
		if alert.Rule.Name == rule.Name && alert.Status == "firing" {
			// Update existing alert
			alert.Value = value
			alert.LastUpdate = time.Now()
			return
		}
	}
	
	// Create new alert
	alert := &Alert{
		ID:         alertID,
		Rule:       rule,
		Value:      value,
		StartTime:  time.Now(),
		LastUpdate: time.Now(),
		Status:     "firing",
		Message:    fmt.Sprintf("%s: %s %.2f (threshold: %.2f)", rule.Name, rule.Condition, value, rule.Threshold),
	}
	
	am.activeAlerts[alertID] = alert
	
	// Send notifications
	go am.sendNotifications(ctx, alert)
}

// resolveAlert resolves an alert
func (am *AlertManager) resolveAlert(ctx context.Context, rule *AlertRule) {
	for alertID, alert := range am.activeAlerts {
		if alert.Rule.Name == rule.Name && alert.Status == "firing" {
			alert.Status = "resolved"
			alert.LastUpdate = time.Now()
			
			// Send resolution notification
			go am.sendNotifications(ctx, alert)
			
			// Remove from active alerts after a delay
			go func(id string) {
				time.Sleep(5 * time.Minute)
				am.mu.Lock()
				delete(am.activeAlerts, id)
				am.mu.Unlock()
			}(alertID)
			
			break
		}
	}
}

// sendNotifications sends notifications for an alert
func (am *AlertManager) sendNotifications(ctx context.Context, alert *Alert) {
	am.mu.RLock()
	channels := make([]NotificationChannel, len(am.notificationChannels))
	copy(channels, am.notificationChannels)
	am.mu.RUnlock()
	
	for _, channel := range channels {
		if err := channel.Send(ctx, alert); err != nil {
			log.Printf("Failed to send notification via %s: %v", channel.Name(), err)
		}
	}
}

// GetActiveAlerts returns all active alerts
func (am *AlertManager) GetActiveAlerts() []*Alert {
	am.mu.RLock()
	defer am.mu.RUnlock()
	
	alerts := make([]*Alert, 0, len(am.activeAlerts))
	for _, alert := range am.activeAlerts {
		alertCopy := *alert
		alerts = append(alerts, &alertCopy)
	}
	
	return alerts
}

// GetRules returns all alert rules
func (am *AlertManager) GetRules() []*AlertRule {
	am.mu.RLock()
	defer am.mu.RUnlock()
	
	rules := make([]*AlertRule, 0, len(am.rules))
	for _, rule := range am.rules {
		ruleCopy := *rule
		rules = append(rules, &ruleCopy)
	}
	
	return rules
}

// String returns a string representation of alert severity
func (as AlertSeverity) String() string {
	switch as {
	case AlertSeverityInfo:
		return "INFO"
	case AlertSeverityWarning:
		return "WARNING"
	case AlertSeverityCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// LogNotificationChannel implements a simple log-based notification channel
type LogNotificationChannel struct{}

// NewLogNotificationChannel creates a new log notification channel
func NewLogNotificationChannel() *LogNotificationChannel {
	return &LogNotificationChannel{}
}

// Send sends a notification via log
func (lnc *LogNotificationChannel) Send(ctx context.Context, alert *Alert) error {
	log.Printf("[ALERT] %s - %s: %s", alert.Rule.Severity.String(), alert.Status, alert.Message)
	return nil
}

// Name returns the channel name
func (lnc *LogNotificationChannel) Name() string {
	return "log"
}

// Default alert rules for Google Maps scraper enterprise deployment

// CreateDefaultAlertRules creates default alert rules for enterprise deployment
func CreateDefaultAlertRules() []*AlertRule {
	return []*AlertRule{
		{
			Name:        "high_error_rate",
			MetricName:  "error_rate",
			Condition:   "gt",
			Threshold:   5.0, // 5% error rate
			Duration:    2 * time.Minute,
			Severity:    AlertSeverityCritical,
			Description: "Error rate is above 5%",
			Enabled:     true,
		},
		{
			Name:        "low_jobs_per_minute",
			MetricName:  "jobs_per_minute",
			Condition:   "lt",
			Threshold:   5000, // Below 5000 jobs/minute
			Duration:    5 * time.Minute,
			Severity:    AlertSeverityWarning,
			Description: "Job processing rate is below expected threshold",
			Enabled:     true,
		},
		{
			Name:        "high_memory_usage",
			MetricName:  "memory_usage_mb",
			Condition:   "gt",
			Threshold:   3500, // 3.5GB
			Duration:    3 * time.Minute,
			Severity:    AlertSeverityWarning,
			Description: "Memory usage is above 3.5GB",
			Enabled:     true,
		},
		{
			Name:        "high_cpu_usage",
			MetricName:  "cpu_usage_percent",
			Condition:   "gt",
			Threshold:   90, // 90% CPU
			Duration:    2 * time.Minute,
			Severity:    AlertSeverityWarning,
			Description: "CPU usage is above 90%",
			Enabled:     true,
		},
		{
			Name:        "database_connection_high",
			MetricName:  "db_connections",
			Condition:   "gt",
			Threshold:   80, // 80% of max connections
			Duration:    1 * time.Minute,
			Severity:    AlertSeverityWarning,
			Description: "Database connections are above 80% of limit",
			Enabled:     true,
		},
		{
			Name:        "slow_database_queries",
			MetricName:  "db_query_time_ms",
			Condition:   "gt",
			Threshold:   1000, // 1 second
			Duration:    3 * time.Minute,
			Severity:    AlertSeverityWarning,
			Description: "Database queries are taking longer than 1 second",
			Enabled:     true,
		},
		{
			Name:        "health_check_failure",
			MetricName:  "health_percentage",
			Condition:   "lt",
			Threshold:   90, // Below 90% healthy
			Duration:    1 * time.Minute,
			Severity:    AlertSeverityCritical,
			Description: "Health check percentage is below 90%",
			Enabled:     true,
		},
	}
}