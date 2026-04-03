package elasticsearch

import (
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
