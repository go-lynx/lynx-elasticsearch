package elasticsearch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-lynx/lynx-elasticsearch/conf"
	"google.golang.org/protobuf/types/known/durationpb"
)

// esProductHeader adds the header that satisfies the Elasticsearch client's
// product check so that the httptest server is treated as a real ES node.
func esProductHeader(w http.ResponseWriter) {
	w.Header().Set("X-Elastic-Product", "Elasticsearch")
}

// ---------------------------------------------------------------------------
// testConnection – covered via httptest server returning 200 / non-200
// ---------------------------------------------------------------------------

func TestTestConnection_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esProductHeader(w)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{srv.URL},
		MaxRetries:     3,
		ConnectTimeout: durationpb.New(5 * time.Second),
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	// testConnection sends a Ping – any non-error 2xx response is acceptable.
	if err := plugin.testConnection(); err != nil {
		t.Fatalf("testConnection expected success, got %v", err)
	}
}

func TestTestConnection_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esProductHeader(w)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{srv.URL},
		MaxRetries:     0,
		ConnectTimeout: durationpb.New(5 * time.Second),
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	err := plugin.testConnection()
	if err == nil {
		t.Fatal("expected testConnection to fail on non-2xx response")
	}
}

// ---------------------------------------------------------------------------
// checkHealth – covered via httptest server
// ---------------------------------------------------------------------------

func makeHealthResponse(status string, nodes int, enableMetrics bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		esProductHeader(w)
		body := map[string]any{
			"cluster_name":    "test-cluster",
			"status":          status,
			"number_of_nodes": nodes,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(body)
	}
}

func TestCheckHealth_GreenStatus(t *testing.T) {
	srv := httptest.NewServer(makeHealthResponse("green", 3, true))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{srv.URL},
		MaxRetries:     0,
		ConnectTimeout: durationpb.New(5 * time.Second),
		EnableMetrics:  true,
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	if err := plugin.checkHealth(); err != nil {
		t.Fatalf("expected green cluster to pass health check, got %v", err)
	}
}

func TestCheckHealth_YellowStatus(t *testing.T) {
	srv := httptest.NewServer(makeHealthResponse("yellow", 1, true))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{srv.URL},
		MaxRetries:     0,
		ConnectTimeout: durationpb.New(5 * time.Second),
		EnableMetrics:  true,
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	if err := plugin.checkHealth(); err != nil {
		t.Fatalf("expected yellow cluster to pass health check, got %v", err)
	}
}

func TestCheckHealth_RedStatus(t *testing.T) {
	srv := httptest.NewServer(makeHealthResponse("red", 2, true))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{srv.URL},
		MaxRetries:     0,
		ConnectTimeout: durationpb.New(5 * time.Second),
		EnableMetrics:  true,
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	if err := plugin.checkHealth(); err == nil {
		t.Fatal("expected red cluster to fail health check")
	}
}

func TestCheckHealth_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esProductHeader(w)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{srv.URL},
		MaxRetries:     0,
		ConnectTimeout: durationpb.New(5 * time.Second),
		EnableMetrics:  true,
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	if err := plugin.checkHealth(); err == nil {
		t.Fatal("expected health check to fail on HTTP error status")
	}
}

func TestCheckHealth_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esProductHeader(w)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{srv.URL},
		MaxRetries:     0,
		ConnectTimeout: durationpb.New(5 * time.Second),
		EnableMetrics:  true,
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	if err := plugin.checkHealth(); err == nil {
		t.Fatal("expected health check to fail on invalid JSON response")
	}
}

func TestCheckHealth_EmptyClusterName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esProductHeader(w)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// empty cluster_name triggers the fallback
		_, _ = fmt.Fprint(w, `{"cluster_name":"","status":"green","number_of_nodes":1}`)
	}))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{srv.URL},
		MaxRetries:     0,
		ConnectTimeout: durationpb.New(5 * time.Second),
		EnableMetrics:  true,
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	if err := plugin.checkHealth(); err != nil {
		t.Fatalf("expected success even with empty cluster name, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// collectMetrics – via httptest server
// ---------------------------------------------------------------------------

func TestCollectMetrics_Success(t *testing.T) {
	srv := httptest.NewServer(makeHealthResponse("green", 3, true))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{srv.URL},
		MaxRetries:     0,
		ConnectTimeout: durationpb.New(5 * time.Second),
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	// Should not panic.
	plugin.collectMetrics()
}

func TestCollectMetrics_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esProductHeader(w)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{srv.URL},
		MaxRetries:     0,
		ConnectTimeout: durationpb.New(5 * time.Second),
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	plugin.collectMetrics() // must not panic
}

func TestCollectMetrics_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esProductHeader(w)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{srv.URL},
		MaxRetries:     0,
		ConnectTimeout: durationpb.New(5 * time.Second),
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	plugin.collectMetrics() // must not panic
}

// ---------------------------------------------------------------------------
// startMetricsCollection – starts background goroutine, stopBackgroundTasks stops it
// ---------------------------------------------------------------------------

func TestStartMetricsCollection_StartsAndStops(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		EnableMetrics: true,
	}
	plugin.statsQuit = make(chan struct{})
	plugin.startMetricsCollection()

	// statsQuit channel should exist.
	if plugin.statsQuit == nil {
		t.Fatal("expected statsQuit to be initialized")
	}

	plugin.stopBackgroundTasks()
}

// ---------------------------------------------------------------------------
// startHealthCheck – valid interval starts goroutine then stops cleanly
// ---------------------------------------------------------------------------

func TestStartHealthCheck_ValidInterval(t *testing.T) {
	// The health check goroutine calls checkHealth which calls Cluster.Health.
	// The ES client verifies the X-Elastic-Product header.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esProductHeader(w)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"cluster_name":"test","status":"green","number_of_nodes":1}`)
	}))
	defer srv.Close()

	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:           []string{srv.URL},
		MaxRetries:          0,
		ConnectTimeout:      durationpb.New(5 * time.Second),
		EnableHealthCheck:   true,
		HealthCheckInterval: durationpb.New(1 * time.Second),
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}

	// Call startHealthCheck directly (bypasses validation in createClient).
	// We set a short interval on the conf that has already been validated.
	// Inject a 50ms interval via a fresh conf to test the goroutine path.
	plugin.conf.HealthCheckInterval = durationpb.New(50 * time.Millisecond)
	plugin.startHealthCheck()

	// Let the ticker fire at least once.
	time.Sleep(120 * time.Millisecond)

	plugin.stopBackgroundTasks()
}

// ---------------------------------------------------------------------------
// GetClient / GetConnectionStats (initialized client)
// ---------------------------------------------------------------------------

func TestGetClient_AfterCreateClient(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{"http://localhost:9200"},
		MaxRetries:     3,
		ConnectTimeout: durationpb.New(5 * time.Second),
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient: %v", err)
	}
	if plugin.GetClient() == nil {
		t.Fatal("expected GetClient to return non-nil after createClient")
	}
}

func TestGetConnectionStats_NilClient(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{}
	stats := plugin.GetConnectionStats()
	if stats["client_initialized"] != false {
		t.Fatalf("expected false for uninitialized client, got %v", stats["client_initialized"])
	}
}

// ---------------------------------------------------------------------------
// GetIndexName / GetIndexPrefix – nil conf edge case
// ---------------------------------------------------------------------------

func TestGetIndexName_NilConf(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = nil
	if got := plugin.GetIndexName("docs"); got != "docs" {
		t.Fatalf("expected 'docs', got %q", got)
	}
}

func TestGetIndexPrefix_NilConf(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = nil
	if got := plugin.GetIndexPrefix(); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// validateElasticsearchConfig – remaining boundary cases
// ---------------------------------------------------------------------------

func TestValidateElasticsearchConfig_ConnectTimeoutTooLarge(t *testing.T) {
	plugin := NewElasticsearchClient()
	err := plugin.parseConfig(&stubConfig{cfg: &conf.Elasticsearch{
		ConnectTimeout: durationpb.New(6 * time.Minute),
	}})
	if err == nil {
		t.Fatal("expected error for connect_timeout exceeding max")
	}
	if !strings.Contains(err.Error(), "connect_timeout must not exceed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateElasticsearchConfig_MaxRetriesTooLarge(t *testing.T) {
	plugin := NewElasticsearchClient()
	err := plugin.parseConfig(&stubConfig{cfg: &conf.Elasticsearch{
		MaxRetries: 11,
	}})
	if err == nil {
		t.Fatal("expected error for max_retries exceeding bound")
	}
	if !strings.Contains(err.Error(), "max_retries must not exceed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateElasticsearchConfig_HealthCheckIntervalTooLarge(t *testing.T) {
	plugin := NewElasticsearchClient()
	err := plugin.parseConfig(&stubConfig{cfg: &conf.Elasticsearch{
		HealthCheckInterval: durationpb.New(25 * time.Hour),
	}})
	if err == nil {
		t.Fatal("expected error for health_check_interval exceeding max")
	}
	if !strings.Contains(err.Error(), "health_check_interval must not exceed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateElasticsearchConfig_HealthCheckIntervalZero(t *testing.T) {
	plugin := NewElasticsearchClient()
	err := plugin.parseConfig(&stubConfig{cfg: &conf.Elasticsearch{
		HealthCheckInterval: durationpb.New(0),
	}})
	if err == nil {
		t.Fatal("expected error for zero health_check_interval")
	}
}

func TestValidateElasticsearchConfig_NilConfig(t *testing.T) {
	if err := validateElasticsearchConfig(nil); err == nil {
		t.Fatal("expected error for nil config")
	}
}

// ---------------------------------------------------------------------------
// createClient – additional auth paths (API key, service token, fingerprint)
// ---------------------------------------------------------------------------

func TestCreateClient_WithAPIKey(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{"http://localhost:9200"},
		MaxRetries:     3,
		ConnectTimeout: durationpb.New(5 * time.Second),
		ApiKey:         "my-api-key",
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient with API key: %v", err)
	}
}

func TestCreateClient_WithServiceToken(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{"http://localhost:9200"},
		MaxRetries:     3,
		ConnectTimeout: durationpb.New(5 * time.Second),
		ServiceToken:   "my-service-token",
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient with service token: %v", err)
	}
}

func TestCreateClient_WithCertificateFingerprint(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:              []string{"http://localhost:9200"},
		MaxRetries:             3,
		ConnectTimeout:         durationpb.New(5 * time.Second),
		CertificateFingerprint: "abc123",
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient with fingerprint: %v", err)
	}
}

// ---------------------------------------------------------------------------
// metricsRoundTripper – error path
// ---------------------------------------------------------------------------

func TestMetricsRoundTripper_ErrorFromNextTransport(t *testing.T) {
	rt := newMetricsRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("transport error")
	}))

	req, _ := http.NewRequest(http.MethodGet, "http://localhost:9200/myindex/_search", nil)
	_, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error from inner transport")
	}
}

func TestMetricsRoundTripper_NilNext(t *testing.T) {
	// newMetricsRoundTripper(nil) should use http.DefaultTransport without panic.
	rt := newMetricsRoundTripper(nil)
	if rt == nil {
		t.Fatal("expected non-nil round tripper")
	}
}

func TestMetricsRoundTripper_BulkPath(t *testing.T) {
	rt := newMetricsRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(nil)),
		}, nil
	}))

	req, _ := http.NewRequest(http.MethodPost, "http://localhost:9200/_bulk", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
}

func TestMetricsRoundTripper_ErrorStatus(t *testing.T) {
	rt := newMetricsRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 503,
			Body:       io.NopCloser(bytes.NewReader(nil)),
		}, nil
	}))

	req, _ := http.NewRequest(http.MethodGet, "http://localhost:9200/myindex/_doc/1", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// parseESPath – additional paths
// ---------------------------------------------------------------------------

func TestParseESPath_UpdateAndDelete(t *testing.T) {
	op, index := parseESPath("/myindex/_update/1")
	if op != "update" || index != "myindex" {
		t.Fatalf("expected (update, myindex), got (%s, %s)", op, index)
	}

	op, index = parseESPath("/myindex/_delete/1")
	if op != "delete" || index != "myindex" {
		t.Fatalf("expected (delete, myindex), got (%s, %s)", op, index)
	}
}

func TestParseESPath_MultiSearch(t *testing.T) {
	op, index := parseESPath("/myindex/_msearch")
	if op != "msearch" || index != "myindex" {
		t.Fatalf("expected (msearch, myindex), got (%s, %s)", op, index)
	}
}

func TestParseESPath_SearchSystem(t *testing.T) {
	op, index := parseESPath("/_search")
	if op != "search" || index != "" {
		t.Fatalf("expected (search, ''), got (%s, %s)", op, index)
	}
}

func TestParseESPath_BulkIndexLevel(t *testing.T) {
	op, index := parseESPath("/myindex/_bulk")
	if op != "bulk" || index != "myindex" {
		t.Fatalf("expected (bulk, myindex), got (%s, %s)", op, index)
	}
}

// ---------------------------------------------------------------------------
// options.go – nil conf guard paths (when plugin.conf is already nil)
// ---------------------------------------------------------------------------

func TestWithCredentials_NilConf(t *testing.T) {
	p := &PlugElasticsearch{}
	p.conf = nil
	WithCredentials("u", "p")(p)
	if p.conf == nil || p.conf.Username != "u" {
		t.Fatal("expected WithCredentials to init conf when nil")
	}
}

func TestWithAPIKey_NilConf(t *testing.T) {
	p := &PlugElasticsearch{}
	p.conf = nil
	WithAPIKey("k")(p)
	if p.conf == nil || p.conf.ApiKey != "k" {
		t.Fatal("expected WithAPIKey to init conf when nil")
	}
}

func TestWithServiceToken_NilConf(t *testing.T) {
	p := &PlugElasticsearch{}
	p.conf = nil
	WithServiceToken("t")(p)
	if p.conf == nil || p.conf.ServiceToken != "t" {
		t.Fatal("expected WithServiceToken to init conf when nil")
	}
}

func TestWithCertificateFingerprint_NilConf(t *testing.T) {
	p := &PlugElasticsearch{}
	p.conf = nil
	WithCertificateFingerprint("fp")(p)
	if p.conf == nil || p.conf.CertificateFingerprint != "fp" {
		t.Fatal("expected WithCertificateFingerprint to init conf when nil")
	}
}

func TestWithCompression_NilConf(t *testing.T) {
	p := &PlugElasticsearch{}
	p.conf = nil
	WithCompression(true)(p)
	if p.conf == nil || !p.conf.CompressRequestBody {
		t.Fatal("expected WithCompression to init conf when nil")
	}
}

func TestWithConnectTimeout_NilConf(t *testing.T) {
	p := &PlugElasticsearch{}
	p.conf = nil
	WithConnectTimeout(3 * time.Second)(p)
	if p.conf == nil || p.conf.ConnectTimeout.AsDuration() != 3*time.Second {
		t.Fatal("expected WithConnectTimeout to init conf when nil")
	}
}

func TestWithMaxRetries_NilConf(t *testing.T) {
	p := &PlugElasticsearch{}
	p.conf = nil
	WithMaxRetries(5)(p)
	if p.conf == nil || p.conf.MaxRetries != 5 {
		t.Fatal("expected WithMaxRetries to init conf when nil")
	}
}

func TestWithMetrics_NilConf(t *testing.T) {
	p := &PlugElasticsearch{}
	p.conf = nil
	WithMetrics(true)(p)
	if p.conf == nil || !p.conf.EnableMetrics {
		t.Fatal("expected WithMetrics to init conf when nil")
	}
}

func TestWithHealthCheck_NilConf(t *testing.T) {
	p := &PlugElasticsearch{}
	p.conf = nil
	WithHealthCheck(true, 30*time.Second)(p)
	if p.conf == nil || !p.conf.EnableHealthCheck {
		t.Fatal("expected WithHealthCheck to init conf when nil")
	}
}

func TestWithIndexPrefix_NilConf(t *testing.T) {
	p := &PlugElasticsearch{}
	p.conf = nil
	WithIndexPrefix("pref")(p)
	if p.conf == nil || p.conf.IndexPrefix != "pref" {
		t.Fatal("expected WithIndexPrefix to init conf when nil")
	}
}

func TestWithLogLevel_NilConf(t *testing.T) {
	p := &PlugElasticsearch{}
	p.conf = nil
	WithLogLevel("info")(p)
	if p.conf == nil || p.conf.LogLevel != "info" {
		t.Fatal("expected WithLogLevel to init conf when nil")
	}
}

// ---------------------------------------------------------------------------
// GetIndexName on plugin with no prefix vs with prefix
// ---------------------------------------------------------------------------

func TestPluginGetIndexName_WithAndWithoutPrefix(t *testing.T) {
	p := NewElasticsearchClient()
	p.conf = &conf.Elasticsearch{IndexPrefix: ""}
	if got := p.GetIndexName("docs"); got != "docs" {
		t.Fatalf("expected 'docs' without prefix, got %q", got)
	}

	p.conf.IndexPrefix = "prod"
	if got := p.GetIndexName("docs"); got != "prod_docs" {
		t.Fatalf("expected 'prod_docs', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// RoundTrip – operation with empty path (edge case)
// ---------------------------------------------------------------------------

func TestMetricsRoundTripper_EmptyPath(t *testing.T) {
	rt := newMetricsRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(nil)),
		}, nil
	}))

	req, _ := http.NewRequest(http.MethodGet, "http://localhost:9200", nil)
	req.URL.Path = ""
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error for empty path: %v", err)
	}
	resp.Body.Close()
}
