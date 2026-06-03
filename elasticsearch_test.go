package elasticsearch

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	kconfig "github.com/go-kratos/kratos/v2/config"
	"github.com/go-lynx/lynx-elasticsearch/conf"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

type stubConfig struct {
	cfg *conf.Elasticsearch
}

func (s *stubConfig) Load() error { return nil }

func (s *stubConfig) Scan(v any) error {
	target, ok := v.(*conf.Elasticsearch)
	if !ok {
		return nil
	}
	proto.Reset(target)
	if s.cfg != nil {
		proto.Merge(target, s.cfg)
	}
	return nil
}

func (s *stubConfig) Value(string) kconfig.Value { return nil }

func (s *stubConfig) Watch(string, kconfig.Observer) error { return nil }

func (s *stubConfig) Close() error { return nil }

func TestElasticsearchOptionsAndIndexHelpers(t *testing.T) {
	plugin := NewElasticsearchClient()

	WithAddresses([]string{"http://es-1:9200", "http://es-2:9200"})(plugin)
	WithCredentials("elastic", "secret")(plugin)
	WithAPIKey("api-key")(plugin)
	WithServiceToken("service-token")(plugin)
	WithCertificateFingerprint("fingerprint")(plugin)
	WithCompression(true)(plugin)
	WithConnectTimeout(5 * time.Second)(plugin)
	WithMaxRetries(7)(plugin)
	WithMetrics(true)(plugin)
	WithHealthCheck(true, 45*time.Second)(plugin)
	WithIndexPrefix("myapp")(plugin)
	WithLogLevel("debug")(plugin)

	if plugin.conf == nil {
		t.Fatal("expected options to initialize config")
	}
	if got := len(plugin.conf.Addresses); got != 2 {
		t.Fatalf("expected 2 addresses, got %d", got)
	}
	if plugin.conf.Username != "elastic" || plugin.conf.Password != "secret" {
		t.Fatalf("unexpected credentials: %q/%q", plugin.conf.Username, plugin.conf.Password)
	}
	if plugin.conf.ApiKey != "api-key" {
		t.Fatalf("unexpected API key: %q", plugin.conf.ApiKey)
	}
	if plugin.conf.ServiceToken != "service-token" {
		t.Fatalf("unexpected service token: %q", plugin.conf.ServiceToken)
	}
	if plugin.conf.CertificateFingerprint != "fingerprint" {
		t.Fatalf("unexpected certificate fingerprint: %q", plugin.conf.CertificateFingerprint)
	}
	if !plugin.conf.CompressRequestBody {
		t.Fatal("expected compression to be enabled")
	}
	if plugin.conf.ConnectTimeout.AsDuration() != 5*time.Second {
		t.Fatalf("unexpected connect timeout: %s", plugin.conf.ConnectTimeout.AsDuration())
	}
	if plugin.conf.MaxRetries != 7 {
		t.Fatalf("unexpected max retries: %d", plugin.conf.MaxRetries)
	}
	if !plugin.conf.EnableMetrics || !plugin.conf.EnableHealthCheck {
		t.Fatal("expected metrics and health checks to be enabled")
	}
	if plugin.conf.HealthCheckInterval.AsDuration() != 45*time.Second {
		t.Fatalf("unexpected health check interval: %s", plugin.conf.HealthCheckInterval.AsDuration())
	}
	if plugin.GetIndexName("documents") != "myapp_documents" {
		t.Fatalf("unexpected index name: %q", plugin.GetIndexName("documents"))
	}
	if plugin.GetIndexPrefix() != "myapp" {
		t.Fatalf("unexpected index prefix: %q", plugin.GetIndexPrefix())
	}
	if stats := plugin.GetConnectionStats(); stats["client_initialized"] != false {
		t.Fatalf("expected uninitialized client stats, got %#v", stats["client_initialized"])
	}
}

func TestParseConfigDefaultsAndCreateClient(t *testing.T) {
	plugin := NewElasticsearchClient()
	if err := plugin.parseConfig(&stubConfig{cfg: &conf.Elasticsearch{}}); err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}

	if got := plugin.conf.Addresses; len(got) != 1 || got[0] != "http://localhost:9200" {
		t.Fatalf("unexpected default addresses: %#v", got)
	}
	if plugin.conf.MaxRetries != 3 {
		t.Fatalf("unexpected default max retries: %d", plugin.conf.MaxRetries)
	}
	if plugin.conf.ConnectTimeout.AsDuration() != 30*time.Second {
		t.Fatalf("unexpected default connect timeout: %s", plugin.conf.ConnectTimeout.AsDuration())
	}
	if plugin.conf.HealthCheckInterval.AsDuration() != 30*time.Second {
		t.Fatalf("unexpected default health check interval: %s", plugin.conf.HealthCheckInterval.AsDuration())
	}

	plugin.conf = &conf.Elasticsearch{
		Addresses:           []string{"http://localhost:9200"},
		MaxRetries:          5,
		CompressRequestBody: true,
		EnableMetrics:       true,
		ConnectTimeout:      durationpb.New(2 * time.Second),
		HealthCheckInterval: durationpb.New(10 * time.Second),
		IndexPrefix:         "logs",
	}
	if err := plugin.createClient(); err != nil {
		t.Fatalf("createClient returned error: %v", err)
	}
	if plugin.client == nil {
		t.Fatal("expected client to be initialized")
	}

	stats := plugin.GetConnectionStats()
	if stats["client_initialized"] != true {
		t.Fatalf("expected initialized client stats, got %#v", stats["client_initialized"])
	}
	if stats["max_retries"] != int32(5) {
		t.Fatalf("unexpected max_retries stat: %#v", stats["max_retries"])
	}
	if stats["compression_enabled"] != true {
		t.Fatalf("unexpected compression_enabled stat: %#v", stats["compression_enabled"])
	}
	if plugin.GetIndexName("documents") != "logs_documents" {
		t.Fatalf("unexpected prefixed index name after createClient: %q", plugin.GetIndexName("documents"))
	}
}

func TestParseESPathAndCloseStatsQuitOnce(t *testing.T) {
	tests := []struct {
		path      string
		wantOp    string
		wantIndex string
	}{
		{path: "/myindex/_search", wantOp: "search", wantIndex: "myindex"},
		{path: "/myindex/_doc/1", wantOp: "index", wantIndex: "myindex"},
		{path: "/myindex/_bulk", wantOp: "bulk", wantIndex: "myindex"},
		{path: "/_bulk", wantOp: "bulk", wantIndex: ""},
		{path: "/_search", wantOp: "search", wantIndex: ""},
		{path: "/_cluster/health", wantOp: "_cluster", wantIndex: ""},
		{path: "/plain-index", wantOp: "unknown", wantIndex: "plain-index"},
	}

	for _, tc := range tests {
		gotOp, gotIndex := parseESPath(tc.path)
		if gotOp != tc.wantOp || gotIndex != tc.wantIndex {
			t.Fatalf("parseESPath(%q) = (%q, %q), want (%q, %q)", tc.path, gotOp, gotIndex, tc.wantOp, tc.wantIndex)
		}
	}

	plugin := NewElasticsearchClient()
	plugin.statsQuit = make(chan struct{})
	plugin.closeStatsQuitOnce()
	select {
	case <-plugin.statsQuit:
	default:
		t.Fatal("expected statsQuit to be closed")
	}
	plugin.closeStatsQuitOnce()
}

func TestParseConfigRejectsInvalidBoundaries(t *testing.T) {
	tests := []struct {
		name string
		cfg  *conf.Elasticsearch
		want string
	}{
		{
			name: "non-positive connect timeout",
			cfg: &conf.Elasticsearch{
				ConnectTimeout: durationpb.New(0),
			},
			want: "connect_timeout must be greater than 0",
		},
		{
			name: "negative max retries",
			cfg: &conf.Elasticsearch{
				MaxRetries: -1,
			},
			want: "max_retries must be greater than or equal to 0",
		},
		{
			name: "health check interval too small when enabled",
			cfg: &conf.Elasticsearch{
				EnableHealthCheck:   true,
				HealthCheckInterval: durationpb.New(500 * time.Millisecond),
			},
			want: "health_check_interval must be at least 1s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := NewElasticsearchClient()
			err := plugin.parseConfig(&stubConfig{cfg: tt.cfg})
			if err == nil {
				t.Fatalf("expected parseConfig to fail for %s", tt.name)
			}
			if got := err.Error(); got == "" || !strings.Contains(got, tt.want) {
				t.Fatalf("expected error containing %q, got %q", tt.want, got)
			}
		})
	}
}

func TestCreateClientRejectsInvalidConnectTimeout(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		Addresses:      []string{"http://localhost:9200"},
		ConnectTimeout: durationpb.New(0),
	}

	if err := plugin.createClient(); err == nil {
		t.Fatal("expected createClient to reject invalid connect_timeout")
	}
}

func TestStartHealthCheckWithInvalidIntervalDoesNotPanic(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		EnableHealthCheck:   true,
		HealthCheckInterval: durationpb.New(0),
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("startHealthCheck should not panic on invalid interval: %v", r)
		}
	}()

	plugin.startHealthCheck()
}

// TestCheckBulkResponseSuccess verifies that CheckBulkResponse returns nil when
// the bulk response reports no errors.
func TestCheckBulkResponseSuccess(t *testing.T) {
	body := strings.NewReader(`{
		"errors": false,
		"items": [
			{"index": {"_index": "test", "_id": "1", "status": 201}},
			{"index": {"_index": "test", "_id": "2", "status": 201}}
		]
	}`)
	if err := CheckBulkResponse(body); err != nil {
		t.Fatalf("expected no error for successful bulk response, got: %v", err)
	}
}

// TestCheckBulkResponsePartialFailure verifies that CheckBulkResponse detects when
// some bulk items fail even though the HTTP response itself was 200 OK.
func TestCheckBulkResponsePartialFailure(t *testing.T) {
	body := strings.NewReader(`{
		"errors": true,
		"items": [
			{"index": {"_index": "test", "_id": "1", "status": 201}},
			{"index": {
				"_index": "test",
				"_id": "2",
				"status": 409,
				"error": {
					"type": "version_conflict_engine_exception",
					"reason": "document already exists"
				}
			}},
			{"create": {
				"_index": "test",
				"_id": "3",
				"status": 400,
				"error": {
					"type": "mapper_parsing_exception",
					"reason": "failed to parse field"
				}
			}}
		]
	}`)

	err := CheckBulkResponse(body)
	if err == nil {
		t.Fatal("expected BulkPartialFailureError, got nil")
	}

	var bpf *BulkPartialFailureError
	ok := false
	if bpfErr, isBPF := err.(*BulkPartialFailureError); isBPF {
		bpf = bpfErr
		ok = true
	}
	if !ok {
		t.Fatalf("expected *BulkPartialFailureError, got %T: %v", err, err)
	}
	if bpf.FailedCount != 2 {
		t.Fatalf("expected FailedCount=2, got %d", bpf.FailedCount)
	}
	if len(bpf.Errors) != 2 {
		t.Fatalf("expected 2 error items, got %d", len(bpf.Errors))
	}
	// Check first error
	if bpf.Errors[0].ErrorType != "version_conflict_engine_exception" {
		t.Fatalf("unexpected first error type: %q", bpf.Errors[0].ErrorType)
	}
	if bpf.Errors[0].Status != 409 {
		t.Fatalf("unexpected first error status: %d", bpf.Errors[0].Status)
	}
	// Check second error
	if bpf.Errors[1].ErrorType != "mapper_parsing_exception" {
		t.Fatalf("unexpected second error type: %q", bpf.Errors[1].ErrorType)
	}
}

// TestCheckBulkResponseAllFailed verifies that CheckBulkResponse handles the case
// where errors=true but no per-item error details are present (paranoia path).
func TestCheckBulkResponseAllFailed(t *testing.T) {
	// errors=true but items list has no per-item errors
	body := strings.NewReader(`{"errors": true, "items": []}`)
	err := CheckBulkResponse(body)
	if err == nil {
		t.Fatal("expected error when errors=true with empty items")
	}
	bpf, ok := err.(*BulkPartialFailureError)
	if !ok {
		t.Fatalf("expected *BulkPartialFailureError, got %T", err)
	}
	if bpf.FailedCount < 1 {
		t.Fatalf("expected FailedCount >= 1, got %d", bpf.FailedCount)
	}
}

// TestCheckBulkResponseInvalidJSON verifies that CheckBulkResponse returns an error
// on malformed JSON rather than silently succeeding.
func TestCheckBulkResponseInvalidJSON(t *testing.T) {
	body := strings.NewReader(`not valid json`)
	if err := CheckBulkResponse(body); err == nil {
		t.Fatal("expected error for invalid JSON bulk response")
	}
}

// TestBulkPartialFailureErrorMessage verifies the error string is human-readable.
func TestBulkPartialFailureErrorMessage(t *testing.T) {
	err := &BulkPartialFailureError{FailedCount: 3}
	if !strings.Contains(err.Error(), "3") {
		t.Fatalf("expected error message to contain count, got: %q", err.Error())
	}
}

// TestStopBackgroundTasksNoTasks verifies that stopBackgroundTasks is safe to call
// when no background goroutines were started (statsQuit is nil).
func TestStopBackgroundTasksNoTasks(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		EnableMetrics:     false,
		EnableHealthCheck: false,
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("stopBackgroundTasks panicked: %v", r)
		}
	}()
	plugin.stopBackgroundTasks()
}

// TestStopBackgroundTasksWithRunningGoroutine verifies that stopBackgroundTasks
// correctly waits for a running goroutine to exit.
func TestStopBackgroundTasksWithRunningGoroutine(t *testing.T) {
	plugin := NewElasticsearchClient()
	plugin.conf = &conf.Elasticsearch{
		EnableMetrics: true,
	}
	plugin.statsQuit = make(chan struct{})

	var started, stopped atomic.Int32
	plugin.statsWG.Go(func() {
		started.Store(1)
		<-plugin.statsQuit
		stopped.Store(1)
	})

	// Give the goroutine a moment to start.
	for i := 0; i < 100 && started.Load() == 0; i++ {
		time.Sleep(time.Millisecond)
	}

	plugin.stopBackgroundTasks()

	if stopped.Load() != 1 {
		t.Fatal("expected background goroutine to have exited after stopBackgroundTasks")
	}
}

// TestMetricsRoundTripperLabels verifies that metricsRoundTripper correctly
// parses operation/index labels without panicking on various path patterns.
func TestMetricsRoundTripperLabels(t *testing.T) {
	paths := []string{
		"/myindex/_search",
		"/_bulk",
		"/myindex/_doc/1",
		"/myindex/_update/1",
		"/_cluster/health",
		"/",
		"",
	}
	rt := newMetricsRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(nil)),
		}, nil
	}))

	for _, path := range paths {
		req := &http.Request{
			Method: http.MethodGet,
			URL:    &url.URL{Path: path},
			Header: make(http.Header),
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip(%q) returned unexpected error: %v", path, err)
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
}

// TestUpdateClusterMetrics verifies that updateClusterMetrics does not panic and
// uses a fallback cluster name when the provided name is empty.
func TestUpdateClusterMetrics(t *testing.T) {
	// Should not panic with any combination of inputs.
	updateClusterMetrics("", 3, "green")
	updateClusterMetrics("mycluster", 0, "yellow")
	updateClusterMetrics("mycluster", 5, "red")
	updateClusterMetrics("mycluster", 5, "unknown")
}

// roundTripFunc is a helper that adapts a function to the http.RoundTripper interface.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
