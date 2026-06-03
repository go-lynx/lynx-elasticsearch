package elasticsearch

import (
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const metricsSubsystem = "elasticsearch"

var (
	operationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "lynx",
			Subsystem: metricsSubsystem,
			Name:      "operations_total",
			Help:      "Total number of Elasticsearch operations by type and index",
		},
		[]string{"operation", "index"},
	)
	queryDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "lynx",
			Subsystem: metricsSubsystem,
			Name:      "query_duration_seconds",
			Help:      "Elasticsearch query duration in seconds",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.3, 0.5, 0.75, 1, 1.5, 2, 3, 5},
		},
		[]string{"operation", "index"},
	)
	bulkOperationsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "lynx",
			Subsystem: metricsSubsystem,
			Name:      "bulk_operations_total",
			Help:      "Total number of Elasticsearch bulk operations",
		},
	)
	errorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "lynx",
			Subsystem: metricsSubsystem,
			Name:      "errors_total",
			Help:      "Total number of Elasticsearch operation errors",
		},
		[]string{"operation", "index"},
	)
	activeConnections = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "lynx",
			Subsystem: metricsSubsystem,
			Name:      "active_connections",
			Help:      "Number of nodes in Elasticsearch cluster (approximation for connection health)",
		},
	)
	healthCheckTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "lynx",
			Subsystem: metricsSubsystem,
			Name:      "health_check_total",
			Help:      "Total number of health checks performed",
		},
		[]string{"status"},
	)
	clusterStatus = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "lynx",
			Subsystem: metricsSubsystem,
			Name:      "cluster_status",
			Help:      "Cluster health status (0=red, 1=yellow, 2=green)",
		},
		[]string{"cluster"},
	)
)

// metricsRoundTripper is an http.RoundTripper decorator that records per-operation
// Prometheus counters and histograms for every Elasticsearch HTTP request.
//
// Limitation: bulk partial failures (HTTP 200 with "errors":true in the JSON body)
// cannot be detected at this layer because the response body is a streaming resource
// consumed by the caller.  Use CheckBulkResponse to detect partial failures after
// receiving the response.
type metricsRoundTripper struct {
	next http.RoundTripper
}

// newMetricsRoundTripper wraps next with metric instrumentation.  If next is nil,
// http.DefaultTransport is used.
func newMetricsRoundTripper(next http.RoundTripper) *metricsRoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &metricsRoundTripper{next: next}
}

func (m *metricsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	op, index := parseESPath(req.URL.Path)
	isBulk := strings.Contains(req.URL.Path, "/_bulk") || strings.HasSuffix(req.URL.Path, "_bulk")

	resp, err := m.next.RoundTrip(req)
	duration := time.Since(start).Seconds()

	if op != "" {
		operationsTotal.WithLabelValues(op, index).Inc()
		queryDurationSeconds.WithLabelValues(op, index).Observe(duration)
		if resp != nil && resp.StatusCode >= 400 {
			errorsTotal.WithLabelValues(op, index).Inc()
		}
	}
	if isBulk {
		bulkOperationsTotal.Inc()
	}
	if err != nil && op != "" {
		errorsTotal.WithLabelValues(op, index).Inc()
	}

	return resp, err
}

// parseESPath extracts an operation label and an index name from an Elasticsearch
// HTTP request path for use as Prometheus metric labels.
//
// Examples:
//
//	/myindex/_search  → ("search", "myindex")
//	/myindex/_doc/1   → ("index",  "myindex")
//	/_bulk            → ("bulk",   "")
//	/_cluster/health  → ("_cluster", "")
func parseESPath(path string) (op, index string) {
	path = strings.TrimPrefix(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return "unknown", ""
	}
	// First part is index name (or _cluster, _nodes, etc.)
	first := parts[0]
	if strings.HasPrefix(first, "_") {
		// System API
		if first == "_bulk" || strings.HasSuffix(path, "_bulk") {
			return "bulk", ""
		}
		if first == "_search" || strings.Contains(path, "/_search") {
			return "search", ""
		}
		return first, ""
	}
	index = first
	if len(parts) >= 2 {
		switch parts[1] {
		case "_search":
			return "search", index
		case "_doc", "_create":
			return "index", index
		case "_update":
			return "update", index
		case "_delete":
			return "delete", index
		case "_bulk":
			return "bulk", index
		case "_mget", "_msearch":
			return "msearch", index
		}
	}
	return "unknown", index
}

// updateClusterMetrics updates cluster-level Prometheus gauges from a Cluster Health
// response.  clusterName is the name reported by Elasticsearch; nodeCount is the
// current number of nodes in the cluster; status is the cluster health colour
// ("green", "yellow", or "red").
//
// The clusterStatus gauge encodes health colour as a numeric value:
//
//	2 = green  (all shards assigned, no issues)
//	1 = yellow (primary shards assigned, some replicas unassigned)
//	0 = red    (some primary shards unassigned — data may be unavailable)
func updateClusterMetrics(clusterName string, nodeCount int, status string) {
	if clusterName == "" {
		clusterName = "elasticsearch"
	}
	if nodeCount > 0 {
		activeConnections.Set(float64(nodeCount))
	}
	statusVal := 0.0
	switch status {
	case "green":
		statusVal = 2
	case "yellow":
		statusVal = 1
	case "red":
		statusVal = 0
	}
	clusterStatus.WithLabelValues(clusterName).Set(statusVal)
}
