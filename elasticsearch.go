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

// Initialize Elasticsearch plugin
func (p *PlugElasticsearch) Initialize(plugin plugins.Plugin, rt plugins.Runtime) error {
	err := p.BasePlugin.Initialize(plugin, rt)
	if err != nil {
		log.Error(err)
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

// Start Elasticsearch plugin
func (p *PlugElasticsearch) Start(plugin plugins.Plugin) error {
	err := p.BasePlugin.Start(plugin)
	if err != nil {
		log.Error(err)
		return err
	}

	// Test connection
	if err := p.testConnection(); err != nil {
		return fmt.Errorf("failed to test elasticsearch connection: %w", err)
	}

	log.Info("elasticsearch plugin started successfully")
	return nil
}

// Stop Elasticsearch plugin
func (p *PlugElasticsearch) Stop(plugin plugins.Plugin) error {
	err := p.BasePlugin.Stop(plugin)
	if err != nil {
		log.Error(err)
		return err
	}

	// Stop background tasks (metrics + health check) with unified timeout
	p.stopBackgroundTasks()

	log.Info("elasticsearch plugin stopped successfully")
	return nil
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
		p.conf.MaxRetries = 3
	}
	if p.conf.ConnectTimeout == nil {
		p.conf.ConnectTimeout = durationpb.New(30 * time.Second)
	}
	if p.conf.HealthCheckInterval == nil {
		p.conf.HealthCheckInterval = durationpb.New(30 * time.Second)
	}

	return nil
}

// createClient Create Elasticsearch client
func (p *PlugElasticsearch) createClient() error {
	connectTimeout := 30 * time.Second
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

// testConnection Test connection
func (p *PlugElasticsearch) testConnection() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

// startMetricsCollection Start metrics collection
func (p *PlugElasticsearch) startMetricsCollection() {
	if p.statsQuit == nil {
		p.statsQuit = make(chan struct{})
	}
	p.statsWG.Add(1)

	go func() {
		defer p.statsWG.Done()
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
	}()
}

// stopBackgroundTasks stops metrics collection and health check goroutines with timeout
func (p *PlugElasticsearch) stopBackgroundTasks() {
	if p.statsQuit == nil {
		return
	}
	if !p.conf.EnableMetrics && !p.conf.EnableHealthCheck {
		// No background tasks were started, don't create or close channel
		return
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
	case <-time.After(10 * time.Second):
		log.Warnf("timeout waiting for elasticsearch background tasks to stop")
	}
}

// clusterHealthResponse parses Cluster Health API response
type clusterHealthResponse struct {
	Status        string `json:"status"`
	NumberOfNodes int    `json:"number_of_nodes"`
	ClusterName   string `json:"cluster_name"`
}

// collectMetrics Collect metrics
func (p *PlugElasticsearch) collectMetrics() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get cluster health status and export to Prometheus
	healthRes, err := p.client.Cluster.Health(p.client.Cluster.Health.WithContext(ctx))
	if err != nil {
		log.Errorf("failed to get cluster health: %v", err)
		return
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(healthRes.Body)

	if !healthRes.IsError() {
		var health clusterHealthResponse
		if err := json.NewDecoder(healthRes.Body).Decode(&health); err == nil {
			updateClusterMetrics(health.NumberOfNodes, health.Status)
		}
	}

	// Cluster.Stats is optional, health gives us the key metrics
	statsRes, err := p.client.Cluster.Stats(p.client.Cluster.Stats.WithContext(ctx))
	if err != nil {
		log.Debugf("cluster stats skipped: %v", err)
		return
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(statsRes.Body)

	log.Debug("elasticsearch metrics collected")
}

// startHealthCheck Start health check
func (p *PlugElasticsearch) startHealthCheck() {
	interval := p.conf.HealthCheckInterval.AsDuration()

	// Ensure quit channel exists
	if p.statsQuit == nil {
		p.statsQuit = make(chan struct{})
	}

	p.statsWG.Add(1)
	go func() {
		defer p.statsWG.Done()
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
	}()
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

// checkHealth Perform health check
func (p *PlugElasticsearch) checkHealth() error {
	// Use lightweight Ping for health check to avoid higher overhead of Cluster Health
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	res, err := p.client.Ping(p.client.Ping.WithContext(ctx))
	if err != nil {
		if p.conf.EnableMetrics {
			healthCheckTotal.WithLabelValues("failure").Inc()
		}
		return err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(res.Body)

	if p.conf.EnableMetrics {
		if res.IsError() {
			healthCheckTotal.WithLabelValues("failure").Inc()
		} else {
			healthCheckTotal.WithLabelValues("success").Inc()
		}
	}

	if res.IsError() {
		return fmt.Errorf("ping health check failed: status=%d, latency=%s", res.StatusCode, time.Since(start))
	}

	log.Debugf("elasticsearch ping ok: status=%d, latency=%s", res.StatusCode, time.Since(start))
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
