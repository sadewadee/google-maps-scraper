package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// DashboardServer provides a web-based monitoring dashboard
type DashboardServer struct {
	metricsCollector *MetricsCollector
	alertManager     *AlertManager
	server          *http.Server
	port            int
}

// DashboardData represents data for the dashboard template
type DashboardData struct {
	Timestamp    string            `json:"timestamp"`
	Metrics      map[string]Metric `json:"metrics"`
	ActiveAlerts []*Alert          `json:"active_alerts"`
	SystemInfo   SystemInfo        `json:"system_info"`
	Performance  PerformanceStats  `json:"performance"`
}

// SystemInfo represents system information
type SystemInfo struct {
	Uptime          string  `json:"uptime"`
	Version         string  `json:"version"`
	GoVersion       string  `json:"go_version"`
	TotalJobs       int64   `json:"total_jobs"`
	SuccessfulJobs  int64   `json:"successful_jobs"`
	FailedJobs      int64   `json:"failed_jobs"`
	SuccessRate     float64 `json:"success_rate"`
}

// PerformanceStats represents performance statistics
type PerformanceStats struct {
	JobsPerMinute     float64 `json:"jobs_per_minute"`
	DataPointsPerHour int64   `json:"data_points_per_hour"`
	AvgResponseTime   float64 `json:"avg_response_time"`
	P95ResponseTime   float64 `json:"p95_response_time"`
	P99ResponseTime   float64 `json:"p99_response_time"`
}

// NewDashboardServer creates a new dashboard server
func NewDashboardServer(metricsCollector *MetricsCollector, alertManager *AlertManager, port int) *DashboardServer {
	return &DashboardServer{
		metricsCollector: metricsCollector,
		alertManager:     alertManager,
		port:            port,
	}
}

// Start starts the dashboard server
func (ds *DashboardServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	
	// Dashboard routes
	mux.HandleFunc("/", ds.handleDashboard)
	mux.HandleFunc("/api/metrics", ds.handleMetricsAPI)
	mux.HandleFunc("/api/alerts", ds.handleAlertsAPI)
	mux.HandleFunc("/api/health", ds.handleHealthAPI)
	mux.HandleFunc("/static/", ds.handleStatic)
	
	ds.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", ds.port),
		Handler: mux,
	}
	
	go func() {
		if err := ds.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Dashboard server error: %v\n", err)
		}
	}()
	
	fmt.Printf("Dashboard server started on http://localhost:%d\n", ds.port)
	return nil
}

// Stop stops the dashboard server
func (ds *DashboardServer) Stop(ctx context.Context) error {
	if ds.server != nil {
		return ds.server.Shutdown(ctx)
	}
	return nil
}

// handleDashboard serves the main dashboard page
func (ds *DashboardServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := ds.getDashboardData()
	
	tmpl := `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Google Maps Scraper - Enterprise Dashboard</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #f5f5f5; }
        .header { background: #2c3e50; color: white; padding: 1rem 2rem; }
        .header h1 { font-size: 1.5rem; }
        .container { max-width: 1200px; margin: 0 auto; padding: 2rem; }
        .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 1.5rem; }
        .card { background: white; border-radius: 8px; padding: 1.5rem; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
        .card h2 { color: #2c3e50; margin-bottom: 1rem; font-size: 1.2rem; }
        .metric { display: flex; justify-content: space-between; padding: 0.5rem 0; border-bottom: 1px solid #eee; }
        .metric:last-child { border-bottom: none; }
        .metric-value { font-weight: bold; color: #27ae60; }
        .alert { padding: 0.75rem; margin: 0.5rem 0; border-radius: 4px; }
        .alert-critical { background: #e74c3c; color: white; }
        .alert-warning { background: #f39c12; color: white; }
        .alert-info { background: #3498db; color: white; }
        .status-good { color: #27ae60; }
        .status-warning { color: #f39c12; }
        .status-critical { color: #e74c3c; }
        .refresh-info { text-align: center; color: #7f8c8d; margin-top: 1rem; }
        .performance-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 1rem; }
        .perf-stat { text-align: center; padding: 1rem; background: #ecf0f1; border-radius: 4px; }
        .perf-value { font-size: 1.5rem; font-weight: bold; color: #2c3e50; }
        .perf-label { font-size: 0.9rem; color: #7f8c8d; margin-top: 0.5rem; }
    </style>
    <script>
        function refreshData() {
            fetch('/api/metrics')
                .then(response => response.json())
                .then(data => {
                    document.getElementById('last-update').textContent = 'Last updated: ' + new Date().toLocaleTimeString();
                })
                .catch(error => console.error('Error:', error));
        }
        
        setInterval(refreshData, 30000); // Refresh every 30 seconds
        
        window.onload = function() {
            document.getElementById('last-update').textContent = 'Last updated: ' + new Date().toLocaleTimeString();
        };
    </script>
</head>
<body>
    <div class="header">
        <h1>🗺️ Google Maps Scraper - Enterprise Dashboard</h1>
    </div>
    
    <div class="container">
        <div class="grid">
            <!-- System Overview -->
            <div class="card">
                <h2>📊 System Overview</h2>
                <div class="metric">
                    <span>Uptime</span>
                    <span class="metric-value">{{.SystemInfo.Uptime}}</span>
                </div>
                <div class="metric">
                    <span>Total Jobs</span>
                    <span class="metric-value">{{.SystemInfo.TotalJobs}}</span>
                </div>
                <div class="metric">
                    <span>Success Rate</span>
                    <span class="metric-value">{{printf "%.2f%%" .SystemInfo.SuccessRate}}</span>
                </div>
                <div class="metric">
                    <span>Failed Jobs</span>
                    <span class="metric-value">{{.SystemInfo.FailedJobs}}</span>
                </div>
            </div>
            
            <!-- Performance Stats -->
            <div class="card">
                <h2>⚡ Performance</h2>
                <div class="performance-grid">
                    <div class="perf-stat">
                        <div class="perf-value">{{printf "%.0f" .Performance.JobsPerMinute}}</div>
                        <div class="perf-label">Jobs/Min</div>
                    </div>
                    <div class="perf-stat">
                        <div class="perf-value">{{.Performance.DataPointsPerHour}}</div>
                        <div class="perf-label">Data Points/Hour</div>
                    </div>
                    <div class="perf-stat">
                        <div class="perf-value">{{printf "%.0fms" .Performance.AvgResponseTime}}</div>
                        <div class="perf-label">Avg Response</div>
                    </div>
                    <div class="perf-stat">
                        <div class="perf-value">{{printf "%.0fms" .Performance.P95ResponseTime}}</div>
                        <div class="perf-label">P95 Response</div>
                    </div>
                </div>
            </div>
            
            <!-- Active Alerts -->
            <div class="card">
                <h2>🚨 Active Alerts ({{len .ActiveAlerts}})</h2>
                {{if .ActiveAlerts}}
                    {{range .ActiveAlerts}}
                    <div class="alert alert-{{if eq .Rule.Severity 2}}critical{{else if eq .Rule.Severity 1}}warning{{else}}info{{end}}">
                        <strong>{{.Rule.Name}}</strong><br>
                        {{.Message}}<br>
                        <small>Started: {{.StartTime.Format "15:04:05"}}</small>
                    </div>
                    {{end}}
                {{else}}
                    <div style="text-align: center; color: #27ae60; padding: 2rem;">
                        ✅ No active alerts
                    </div>
                {{end}}
            </div>
            
            <!-- Resource Usage -->
            <div class="card">
                <h2>💾 Resource Usage</h2>
                {{with index .Metrics "memory_usage_mb"}}
                <div class="metric">
                    <span>Memory Usage</span>
                    <span class="metric-value">{{printf "%.0f MB" .Value}}</span>
                </div>
                {{end}}
                {{with index .Metrics "cpu_usage_percent"}}
                <div class="metric">
                    <span>CPU Usage</span>
                    <span class="metric-value">{{printf "%.1f%%" .Value}}</span>
                </div>
                {{end}}
                {{with index .Metrics "disk_usage_percent"}}
                <div class="metric">
                    <span>Disk Usage</span>
                    <span class="metric-value">{{printf "%.1f%%" .Value}}</span>
                </div>
                {{end}}
            </div>
            
            <!-- Database Stats -->
            <div class="card">
                <h2>🗄️ Database</h2>
                {{with index .Metrics "db_connections"}}
                <div class="metric">
                    <span>Active Connections</span>
                    <span class="metric-value">{{printf "%.0f" .Value}}</span>
                </div>
                {{end}}
                {{with index .Metrics "db_query_time_ms"}}
                <div class="metric">
                    <span>Avg Query Time</span>
                    <span class="metric-value">{{printf "%.0f ms" .Value}}</span>
                </div>
                {{end}}
                {{with index .Metrics "db_errors"}}
                <div class="metric">
                    <span>DB Errors</span>
                    <span class="metric-value">{{printf "%.0f" .Value}}</span>
                </div>
                {{end}}
            </div>
            
            <!-- Circuit Breaker Status -->
            <div class="card">
                <h2>🔌 Circuit Breakers</h2>
                {{with index .Metrics "circuit_breaker_open"}}
                <div class="metric">
                    <span>Open Breakers</span>
                    <span class="metric-value">{{printf "%.0f" .Value}}</span>
                </div>
                {{end}}
                {{with index .Metrics "circuit_breaker_half_open"}}
                <div class="metric">
                    <span>Half-Open Breakers</span>
                    <span class="metric-value">{{printf "%.0f" .Value}}</span>
                </div>
                {{end}}
            </div>
        </div>
        
        <div class="refresh-info">
            <span id="last-update">Loading...</span> | Auto-refresh every 30 seconds
        </div>
    </div>
</body>
</html>
`
	
	t, err := template.New("dashboard").Parse(tmpl)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "text/html")
	if err := t.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleMetricsAPI serves metrics data as JSON
func (ds *DashboardServer) handleMetricsAPI(w http.ResponseWriter, r *http.Request) {
	data := ds.getDashboardData()
	
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleAlertsAPI serves alerts data as JSON
func (ds *DashboardServer) handleAlertsAPI(w http.ResponseWriter, r *http.Request) {
	alerts := ds.alertManager.GetActiveAlerts()
	
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(alerts); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleHealthAPI serves health check endpoint
func (ds *DashboardServer) handleHealthAPI(w http.ResponseWriter, r *http.Request) {
	metrics := ds.metricsCollector.GetMetrics()
	alerts := ds.alertManager.GetActiveAlerts()
	
	// Count critical alerts
	criticalAlerts := 0
	for _, alert := range alerts {
		if alert.Rule.Severity == AlertSeverityCritical && alert.Status == "firing" {
			criticalAlerts++
		}
	}
	
	status := "healthy"
	if criticalAlerts > 0 {
		status = "unhealthy"
	}
	
	health := map[string]interface{}{
		"status":          status,
		"timestamp":       time.Now().Format(time.RFC3339),
		"critical_alerts": criticalAlerts,
		"total_alerts":    len(alerts),
		"metrics_count":   len(metrics),
	}
	
	w.Header().Set("Content-Type", "application/json")
	if criticalAlerts > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	
	json.NewEncoder(w).Encode(health)
}

// handleStatic serves static files (placeholder)
func (ds *DashboardServer) handleStatic(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

// convertMetricsMap converts map[string]*Metric to map[string]Metric
func convertMetricsMap(metrics map[string]*Metric) map[string]Metric {
	result := make(map[string]Metric)
	for k, v := range metrics {
		if v != nil {
			result[k] = *v
		}
	}
	return result
}

// getDashboardData collects all data for the dashboard
func (ds *DashboardServer) getDashboardData() *DashboardData {
	metrics := ds.metricsCollector.GetMetrics()
	alerts := ds.alertManager.GetActiveAlerts()
	
	// Calculate system info
	systemInfo := ds.calculateSystemInfo(metrics)
	
	// Calculate performance stats
	performance := ds.calculatePerformanceStats(metrics)
	
	return &DashboardData{
		Timestamp:    time.Now().Format(time.RFC3339),
		Metrics:      convertMetricsMap(metrics),
		ActiveAlerts: alerts,
		SystemInfo:   systemInfo,
		Performance:  performance,
	}
}

// calculateSystemInfo calculates system information from metrics
func (ds *DashboardServer) calculateSystemInfo(metrics map[string]*Metric) SystemInfo {
	var totalJobs, successfulJobs, failedJobs int64
	var successRate float64
	
	if metric, exists := metrics["jobs_processed"]; exists {
		totalJobs = int64(metric.Value)
	}
	
	if metric, exists := metrics["jobs_succeeded"]; exists {
		successfulJobs = int64(metric.Value)
	}
	
	if metric, exists := metrics["jobs_failed"]; exists {
		failedJobs = int64(metric.Value)
	}
	
	if totalJobs > 0 {
		successRate = float64(successfulJobs) / float64(totalJobs) * 100
	}
	
	// Calculate uptime (placeholder - would need actual start time)
	uptime := "24h 30m" // This would be calculated from actual start time
	
	return SystemInfo{
		Uptime:         uptime,
		Version:        "1.0.0",
		GoVersion:      "1.21",
		TotalJobs:      totalJobs,
		SuccessfulJobs: successfulJobs,
		FailedJobs:     failedJobs,
		SuccessRate:    successRate,
	}
}

// calculatePerformanceStats calculates performance statistics from metrics
func (ds *DashboardServer) calculatePerformanceStats(metrics map[string]*Metric) PerformanceStats {
	var jobsPerMinute, avgResponseTime, p95ResponseTime, p99ResponseTime float64
	var dataPointsPerHour int64
	
	if metric, exists := metrics["jobs_per_minute"]; exists {
		jobsPerMinute = metric.Value
	}
	
	if metric, exists := metrics["data_points_scraped"]; exists {
		// Estimate data points per hour based on current rate
		dataPointsPerHour = int64(metric.Value * 60) // Assuming metric is per minute
	}
	
	if metric, exists := metrics["response_time_ms"]; exists {
		avgResponseTime = metric.Value
	}
	
	if metric, exists := metrics["response_time_p95_ms"]; exists {
		p95ResponseTime = metric.Value
	}
	
	if metric, exists := metrics["response_time_p99_ms"]; exists {
		p99ResponseTime = metric.Value
	}
	
	return PerformanceStats{
		JobsPerMinute:     jobsPerMinute,
		DataPointsPerHour: dataPointsPerHour,
		AvgResponseTime:   avgResponseTime,
		P95ResponseTime:   p95ResponseTime,
		P99ResponseTime:   p99ResponseTime,
	}
}

// PrometheusExporter exports metrics in Prometheus format
type PrometheusExporter struct {
	metricsCollector *MetricsCollector
}

// NewPrometheusExporter creates a new Prometheus exporter
func NewPrometheusExporter(metricsCollector *MetricsCollector) *PrometheusExporter {
	return &PrometheusExporter{
		metricsCollector: metricsCollector,
	}
}

// ServeHTTP serves metrics in Prometheus format
func (pe *PrometheusExporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	metrics := pe.metricsCollector.GetMetrics()
	
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	
	// Sort metrics for consistent output
	var names []string
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	
	for _, name := range names {
		metric := metrics[name]
		
		// Write metric help
		fmt.Fprintf(w, "# HELP %s %s\n", name, metric.Description)
		
		// Write metric type
		metricType := "gauge"
		switch metric.Type {
		case MetricTypeCounter:
			metricType = "counter"
		case MetricTypeHistogram:
			metricType = "histogram"
		case MetricTypeSummary:
			metricType = "summary"
		}
		fmt.Fprintf(w, "# TYPE %s %s\n", name, metricType)
		
		// Write metric value
		fmt.Fprintf(w, "%s %s %d\n", name, formatFloat(metric.Value), metric.Timestamp.Unix()*1000)
	}
}

// formatFloat formats a float64 for Prometheus output
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}