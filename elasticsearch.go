// Package elasticsearch provides an Elasticsearch client plugin for the Lynx framework.
// It wraps the official Go Elasticsearch client with production-grade connection management,
// index template bootstrapping, configurable retry/timeout policies, and Prometheus metrics
// for indexing, search, and bulk operations.
package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/go-lynx/lynx/log"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/go-kratos/kratos/v2/config"
	"github.com/go-lynx/lynx-elasticsearch/conf"
	"github.com/go-lynx/lynx/plugins"
	"google.golang.org/protobuf/types/known/durationpb"
)

// BulkPartialFailureError is returned when an Elasticsearch bulk request succeeds at the
// HTTP level (status 200) but one or more individual actions within the batch reported an
// error.  Callers that care about per-document success MUST inspect this error type and
// handle the failed items accordingly; ignoring it risks silent data loss.
//
// Note: the metricsRoundTripper cannot detect bulk partial failures because the response
// body is a streaming resource consumed by the caller.  Detection must be done at the
// application layer by parsing the bulk response and checking the top-level "errors" field.
type BulkPartialFailureError struct {
	// FailedCount is the number of individual bulk items that were rejected.
	FailedCount int
	// Errors contains the per-item error details, keyed by zero-based action index.
	Errors []BulkItemError
}

// Error implements the error interface.
func (e *BulkPartialFailureError) Error() string {
	return fmt.Sprintf("elasticsearch bulk partial failure: %d item(s) failed", e.FailedCount)
}

// BulkItemError captures the error details for a single failed bulk action.
type BulkItemError struct {
	// Index is the target index for the failed action.
	Index string `json:"_index"`
	// ID is the document ID for the failed action, if available.
	ID string `json:"_id"`
	// Status is the HTTP status code returned for this specific action.
	Status int `json:"status"`
	// ErrorType is the Elasticsearch error type string (e.g. "version_conflict_engine_exception").
	ErrorType string `json:"type"`
	// Reason is the human-readable error description.
	Reason string `json:"reason"`
}

// bulkResponseItem is the per-action element inside a bulk response body.
type bulkResponseItem struct {
	Index  *bulkActionResult `json:"index"`
	Create *bulkActionResult `json:"create"`
	Update *bulkActionResult `json:"update"`
	Delete *bulkActionResult `json:"delete"`
}

// bulkActionResult captures the result for one bulk action.
type bulkActionResult struct {
	Index  string `json:"_index"`
	ID     string `json:"_id"`
	Status int    `json:"status"`
	Error  *struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"error"`
}

// bulkResponse is the top-level structure of an Elasticsearch bulk API response.
type bulkResponse struct {
	Errors bool               `json:"errors"`
	Items  []bulkResponseItem `json:"items"`
}

// CheckBulkResponse parses an Elasticsearch bulk API response body and returns a
// BulkPartialFailureError if any individual actions failed.  The caller is responsible
// for closing body after this call.  A nil error means all actions succeeded.
//
// Usage:
//
//	res, err := client.Bulk(body)
//	if err != nil { ... }
//	defer res.Body.Close()
//	if err := elasticsearch.CheckBulkResponse(res.Body); err != nil { ... }
func CheckBulkResponse(body io.Reader) error {
	var br bulkResponse
	if err := json.NewDecoder(body).Decode(&br); err != nil {
		return fmt.Errorf("failed to decode bulk response: %w", err)
	}
	if !br.Errors {
		return nil
	}

	var failedItems []BulkItemError
	for _, item := range br.Items {
		for _, result := range []*bulkActionResult{item.Index, item.Create, item.Update, item.Delete} {
			if result != nil && result.Error != nil {
				failedItems = append(failedItems, BulkItemError{
					Index:     result.Index,
					ID:        result.ID,
					Status:    result.Status,
					ErrorType: result.Error.Type,
					Reason:    result.Error.Reason,
				})
			}
		}
	}

	if len(failedItems) == 0 {
		// Paranoia: errors flag set but no per-item errors found; treat as failure.
		return &BulkPartialFailureError{FailedCount: 1}
	}
	return &BulkPartialFailureError{FailedCount: len(failedItems), Errors: failedItems}
}

const (
	defaultConnectTimeout            = 30 * time.Second
	defaultHealthCheckInterval       = 30 * time.Second
	defaultMaxRetries          int32 = 3
	maxConnectTimeout                = 5 * time.Minute
	maxHealthCheckInterval           = 24 * time.Hour
	maxRetriesBound            int32 = 10
)

// InitializeResourcesContext is the context-aware resource-initialization hook.
// It parses configuration, builds the Elasticsearch client, and launches the
// background metrics/health goroutines. ctx is checked before any work and again
// before goroutines are launched, so a cancelled initialization leaves no workers.
func (p *PlugElasticsearch) InitializeResourcesContext(ctx context.Context, rt plugins.Runtime) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("elasticsearch initialize canceled before execution: %w", err)
	}
	if err := p.BasePlugin.InitializeResources(rt); err != nil {
		return err
	}

	// Get configuration from runtime
	cfg := rt.GetConfig()
	if cfg == nil {
		return fmt.Errorf("failed to get config from runtime")
	}

	// Parse configuration
	if err := p.parseConfig(cfg); err != nil {
		return fmt.Errorf("failed to parse elasticsearch config: %w", err)
	}

	// Create Elasticsearch client
	if err := p.createClient(); err != nil {
		return fmt.Errorf("failed to create elasticsearch client: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("elasticsearch initialize canceled before starting background tasks: %w", err)
	}

	// Start metrics collection
	if p.conf.EnableMetrics {
		p.startMetricsCollection()
	}

	// Start health check
	if p.conf.EnableHealthCheck {
		p.startHealthCheck()
	}

	log.Info("elasticsearch plugin initialized successfully")
	return nil
}

// InitializeResources is the legacy (non-context) resource-initialization hook.
func (p *PlugElasticsearch) InitializeResources(rt plugins.Runtime) error {
	return p.InitializeResourcesContext(context.Background(), rt)
}

// StartupTasksContext is the context-aware startup hook: it verifies
// connectivity with a ping that observes ctx. The core BasePlugin drives the
// lifecycle state machine (status transitions, events, health check).
func (p *PlugElasticsearch) StartupTasksContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("elasticsearch startup canceled before execution: %w", err)
	}
	if err := p.testConnectionContext(ctx); err != nil {
		return fmt.Errorf("failed to test elasticsearch connection: %w", err)
	}

	log.Info("elasticsearch plugin started successfully")
	return nil
}

// StartupTasks is the legacy (non-context) startup hook.
func (p *PlugElasticsearch) StartupTasks() error {
	return p.StartupTasksContext(context.Background())
}

// CleanupTasksContext is the context-aware cleanup hook: background tasks are
// signalled to stop and the wait for them is bounded by ctx.
func (p *PlugElasticsearch) CleanupTasksContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("elasticsearch cleanup canceled before execution: %w", err)
	}
	if err := p.stopBackgroundTasksContext(ctx); err != nil {
		return err
	}

	log.Info("elasticsearch plugin stopped successfully")
	return nil
}

// CleanupTasks is the legacy (non-context) cleanup hook.
func (p *PlugElasticsearch) CleanupTasks() error {
	return p.CleanupTasksContext(context.Background())
}

// parseConfig Parse configuration
func (p *PlugElasticsearch) parseConfig(cfg config.Config) error {
	// Read elasticsearch configuration from config
	var elasticsearchConf conf.Elasticsearch
	if err := cfg.Scan(&elasticsearchConf); err != nil {
		return err
	}
	p.conf = &elasticsearchConf

	// Set default values
	if len(p.conf.Addresses) == 0 {
		p.conf.Addresses = []string{"http://localhost:9200"}
	}
	if p.conf.MaxRetries == 0 {
		p.conf.MaxRetries = defaultMaxRetries
	}
	if p.conf.ConnectTimeout == nil {
		p.conf.ConnectTimeout = durationpb.New(defaultConnectTimeout)
	}
	if p.conf.HealthCheckInterval == nil {
		p.conf.HealthCheckInterval = durationpb.New(defaultHealthCheckInterval)
	}

	return validateElasticsearchConfig(p.conf)
}

// createClient builds and stores an Elasticsearch client from the current configuration.
// validateElasticsearchConfig must have been called before this method.
func (p *PlugElasticsearch) createClient() error {
	if err := validateElasticsearchConfig(p.conf); err != nil {
		return err
	}

	connectTimeout := defaultConnectTimeout
	if p.conf.ConnectTimeout != nil {
		connectTimeout = p.conf.ConnectTimeout.AsDuration()
	}

	// Create transport with ConnectTimeout
	dialer := &net.Dialer{Timeout: connectTimeout}
	baseTransport := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConnsPerHost:   10,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	var transport http.RoundTripper = baseTransport
	if p.conf.EnableMetrics {
		transport = newMetricsRoundTripper(baseTransport)
	}

	// Build client configuration
	clientConfig := elasticsearch.Config{
		Addresses:           p.conf.Addresses,
		MaxRetries:          int(p.conf.MaxRetries),
		CompressRequestBody: p.conf.CompressRequestBody,
		Transport:           transport,
	}

	// Set authentication information
	if p.conf.Username != "" && p.conf.Password != "" {
		clientConfig.Username = p.conf.Username
		clientConfig.Password = p.conf.Password
	}

	if p.conf.ApiKey != "" {
		clientConfig.APIKey = p.conf.ApiKey
	}

	if p.conf.ServiceToken != "" {
		clientConfig.ServiceToken = p.conf.ServiceToken
	}

	if p.conf.CertificateFingerprint != "" {
		clientConfig.CertificateFingerprint = p.conf.CertificateFingerprint
	}

	// Create client
	client, err := elasticsearch.NewClient(clientConfig)
	if err != nil {
		return err
	}

	p.client = client
	return nil
}

// testConnection pings Elasticsearch with the default 10s bound.
func (p *PlugElasticsearch) testConnection() error {
	return p.testConnectionContext(context.Background())
}

// testConnectionContext pings Elasticsearch. The request observes ctx and is
// additionally bounded by a 10s timeout.
func (p *PlugElasticsearch) testConnectionContext(ctx context.Context) error {
	if p.client == nil {
		return fmt.Errorf("elasticsearch client is not initialized")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Send ping request
	res, err := p.client.Ping(p.client.Ping.WithContext(ctx))
	if err != nil {
		return err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Error(err)
		}
	}(res.Body)

	if res.IsError() {
		return fmt.Errorf("elasticsearch ping failed with status: %d", res.StatusCode)
	}

	return nil
}

// startMetricsCollection launches a background goroutine that periodically calls
// collectMetrics.  The goroutine exits when statsQuit is closed and recovers from
// any internal panics so that a transient failure never brings down the process.
func (p *PlugElasticsearch) startMetricsCollection() {
	if p.statsQuit == nil {
		p.statsQuit = make(chan struct{})
	}
	p.statsWG.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("elasticsearch metrics collection goroutine panicked: %v", r)
			}
		}()

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				p.collectMetrics()
			case <-p.statsQuit:
				return
			}
		}
	})
}

// stopBackgroundTasks signals the shared quit channel and waits up to 10 s for all
// background goroutines (metrics collection, health check) to exit.  It is safe to call
// when no background tasks were started.
func (p *PlugElasticsearch) stopBackgroundTasks() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.stopBackgroundTasksContext(ctx); err != nil {
		log.Warnf("timeout waiting for elasticsearch background tasks to stop: %v", err)
	}
}

// stopBackgroundTasksContext signals the shared quit channel and waits for all
// background goroutines to exit, or until ctx is done. It returns ctx's error when
// the wait was abandoned; the goroutines still exit on their own once they observe
// the closed quit channel.
func (p *PlugElasticsearch) stopBackgroundTasksContext(ctx context.Context) error {
	if p.statsQuit == nil {
		// No background goroutines were ever launched.
		return nil
	}
	p.closeStatsQuitOnce()
	done := make(chan struct{})
	go func() {
		p.statsWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.Infof("elasticsearch background tasks stopped successfully")
		return nil
	case <-ctx.Done():
		select {
		case <-done:
			log.Infof("elasticsearch background tasks stopped successfully")
			return nil
		default:
			return fmt.Errorf("elasticsearch cleanup canceled while waiting for background tasks: %w", ctx.Err())
		}
	}
}

// clusterHealthResponse holds the fields we care about from the Cluster Health API.
type clusterHealthResponse struct {
	ClusterName   string `json:"cluster_name"`
	Status        string `json:"status"`
	NumberOfNodes int    `json:"number_of_nodes"`
}

// collectMetrics fetches cluster health via the Elasticsearch Cluster Health API and
// updates the Prometheus gauges.  It is intended to be called periodically from the
// background metrics-collection goroutine.
func (p *PlugElasticsearch) collectMetrics() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	healthRes, err := p.client.Cluster.Health(p.client.Cluster.Health.WithContext(ctx))
	if err != nil {
		log.Errorf("failed to get cluster health: %v", err)
		return
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(healthRes.Body)

	if healthRes.IsError() {
		log.Errorf("cluster health API returned error status %d", healthRes.StatusCode)
		return
	}

	var health clusterHealthResponse
	if err := json.NewDecoder(healthRes.Body).Decode(&health); err != nil {
		log.Errorf("failed to decode cluster health response: %v", err)
		return
	}

	clusterName := health.ClusterName
	if clusterName == "" {
		clusterName = "elasticsearch"
	}
	updateClusterMetrics(clusterName, health.NumberOfNodes, health.Status)
	log.Debug("elasticsearch metrics collected")
}

// startHealthCheck launches a background goroutine that calls checkHealth at the
// configured interval.  It is a no-op when the configured interval is <= 0.
// The goroutine recovers from panics to prevent a transient failure from crashing
// the process.
func (p *PlugElasticsearch) startHealthCheck() {
	interval := p.conf.HealthCheckInterval.AsDuration()
	if interval <= 0 {
		log.Warnf("skipping elasticsearch health check: invalid interval %s", interval)
		return
	}

	// Ensure the shared quit channel exists before launching.
	if p.statsQuit == nil {
		p.statsQuit = make(chan struct{})
	}

	p.statsWG.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("elasticsearch health check goroutine panicked: %v", r)
			}
		}()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := p.checkHealth(); err != nil {
					log.Errorf("elasticsearch health check failed: %v", err)
				}
			case <-p.statsQuit:
				return
			}
		}
	})
}

// closeStatsQuitOnce closes statsQuit only once in a thread-safe way
func (p *PlugElasticsearch) closeStatsQuitOnce() {
	p.statsMu.Lock()
	defer p.statsMu.Unlock()
	if !p.statsClosed && p.statsQuit != nil {
		close(p.statsQuit)
		p.statsClosed = true
	}
}

// checkHealth performs a cluster health check using the Elasticsearch Cluster Health API.
// Unlike a simple ping, this also captures the cluster status (green/yellow/red) and
// updates the cluster_status Prometheus gauge so operators are alerted when the cluster
// is degraded.  A "red" cluster status is treated as an error because it signals data
// unavailability or shard allocation failures.
func (p *PlugElasticsearch) checkHealth() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	res, err := p.client.Cluster.Health(p.client.Cluster.Health.WithContext(ctx))
	if err != nil {
		if p.conf.EnableMetrics {
			healthCheckTotal.WithLabelValues("failure").Inc()
		}
		return fmt.Errorf("cluster health request failed: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(res.Body)

	latency := time.Since(start)

	if res.IsError() {
		if p.conf.EnableMetrics {
			healthCheckTotal.WithLabelValues("failure").Inc()
		}
		return fmt.Errorf("cluster health check failed: status=%d, latency=%s", res.StatusCode, latency)
	}

	var health clusterHealthResponse
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		if p.conf.EnableMetrics {
			healthCheckTotal.WithLabelValues("failure").Inc()
		}
		return fmt.Errorf("failed to decode cluster health response: %w", err)
	}

	if p.conf.EnableMetrics {
		clusterName := health.ClusterName
		if clusterName == "" {
			clusterName = "elasticsearch"
		}
		updateClusterMetrics(clusterName, health.NumberOfNodes, health.Status)
		if health.Status == "red" {
			healthCheckTotal.WithLabelValues("failure").Inc()
		} else {
			healthCheckTotal.WithLabelValues("success").Inc()
		}
	}

	log.Debugf("elasticsearch cluster health: status=%s, nodes=%d, latency=%s",
		health.Status, health.NumberOfNodes, latency)

	if health.Status == "red" {
		return fmt.Errorf("cluster health is red (data unavailable), latency=%s", latency)
	}
	return nil
}

// GetClient Get Elasticsearch client
func (p *PlugElasticsearch) GetClient() *elasticsearch.Client {
	return p.client
}

// GetConnectionStats Get connection statistics
func (p *PlugElasticsearch) GetConnectionStats() map[string]any {
	stats := make(map[string]any)

	if p.client != nil {
		// Get client statistics
		stats["client_initialized"] = true
		stats["addresses"] = p.conf.Addresses
		stats["max_retries"] = p.conf.MaxRetries
		stats["compression_enabled"] = p.conf.CompressRequestBody
	} else {
		stats["client_initialized"] = false
	}

	return stats
}

// GetIndexName returns the index name with configured prefix applied.
// If index_prefix is set (e.g. "myapp"), GetIndexName("documents") returns "myapp_documents".
// If index_prefix is empty, returns the name as-is.
func (p *PlugElasticsearch) GetIndexName(name string) string {
	if p.conf == nil || p.conf.IndexPrefix == "" {
		return name
	}
	return p.conf.IndexPrefix + "_" + name
}

// GetIndexPrefix returns the configured index prefix, or empty string if not set.
func (p *PlugElasticsearch) GetIndexPrefix() string {
	if p.conf == nil {
		return ""
	}
	return p.conf.IndexPrefix
}

// validateElasticsearchConfig checks that the supplied configuration is self-consistent
// and within safe operating bounds.  It returns a descriptive error on the first
// violation found.
func validateElasticsearchConfig(cfg *conf.Elasticsearch) error {
	if cfg == nil {
		return fmt.Errorf("elasticsearch config is required")
	}

	if cfg.ConnectTimeout != nil {
		timeout := cfg.ConnectTimeout.AsDuration()
		if timeout <= 0 {
			return fmt.Errorf("connect_timeout must be greater than 0")
		}
		if timeout > maxConnectTimeout {
			return fmt.Errorf("connect_timeout must not exceed %s", maxConnectTimeout)
		}
	}

	if cfg.MaxRetries < 0 {
		return fmt.Errorf("max_retries must be greater than or equal to 0")
	}
	if cfg.MaxRetries > maxRetriesBound {
		return fmt.Errorf("max_retries must not exceed %d", maxRetriesBound)
	}

	if cfg.HealthCheckInterval != nil {
		interval := cfg.HealthCheckInterval.AsDuration()
		if interval <= 0 {
			return fmt.Errorf("health_check_interval must be greater than 0")
		}
		if interval > maxHealthCheckInterval {
			return fmt.Errorf("health_check_interval must not exceed %s", maxHealthCheckInterval)
		}
		if cfg.EnableHealthCheck && interval < time.Second {
			return fmt.Errorf("health_check_interval must be at least 1s when enable_health_check is true")
		}
	}

	return nil
}
