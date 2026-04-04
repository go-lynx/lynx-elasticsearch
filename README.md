# Elasticsearch Plugin for Lynx Framework

The Elasticsearch plugin provides complete Elasticsearch integration support for the Lynx framework, including search, indexing, aggregation, and other functionalities.

## Features

- ✅ **Complete Elasticsearch client support**
- ✅ **Multiple authentication methods**: username/password, API Key, service tokens
- ✅ **TLS/SSL secure connections**
- ✅ **Connection pool management**
- ✅ **Automatic retry mechanisms**
- ✅ **Health checks**
- ✅ **Metrics monitoring**

## Quick Start

### 1. Configuration

Add Elasticsearch configuration in `config.yml`:

```yaml
lynx:
  elasticsearch:
    addresses:
      - "http://localhost:9200"
    username: "elastic"
    password: "changeme"
    api_key: ""
    service_token: ""
    certificate_fingerprint: ""
    compress_request_body: true
    connect_timeout: "30s"
    max_retries: 3
    enable_metrics: true
    enable_health_check: true
    health_check_interval: "30s"
    index_prefix: "myapp"
    log_level: "info"
```

Complete example: [conf/example_config.yml](./conf/example_config.yml).

### Configuration Reference

| Field | Proto Type | Default Value | Example | Notes |
|-------|------------|---------------|---------|-------|
| `addresses` | `repeated string` | `["http://localhost:9200"]` | `["http://es-1:9200", "http://es-2:9200"]` | List of Elasticsearch node addresses. |
| `username` | `string` | `""` | `"elastic"` | Basic-auth username. |
| `password` | `string` | `""` | `"changeme"` | Basic-auth password. |
| `api_key` | `string` | `""` | `"base64-api-key"` | API key authentication. Set instead of username/password when appropriate. |
| `service_token` | `string` | `""` | `"AAEAA..."` | Service token authentication. |
| `certificate_fingerprint` | `string` | `""` | `"3A:8B:..."` | Server certificate fingerprint pinning. |
| `compress_request_body` | `bool` | `false` | `true` | Enables compressed HTTP request bodies. |
| `connect_timeout` | `google.protobuf.Duration` | `"30s"` | `"30s"` | TCP connect timeout for Elasticsearch HTTP transport. |
| `max_retries` | `int32` | `3` | `5` | Maximum HTTP retry count used by the Elasticsearch client. |
| `enable_metrics` | `bool` | `false` | `true` | Enables Prometheus-style request metrics. |
| `enable_health_check` | `bool` | `false` | `true` | Starts background health checks. |
| `health_check_interval` | `google.protobuf.Duration` | `"30s"` | `"30s"` | Interval used by background health checks. |
| `index_prefix` | `string` | `""` | `"myapp"` | Prefix used by `GetIndexName`, producing names such as `myapp_documents`. |
| `log_level` | `string` | `""` | `"info"` | Reserved field in the current implementation; the value is stored in config but does not currently reconfigure logging. |

Validation boundaries applied by the runtime:

- `connect_timeout` must be greater than `0` and not exceed `5m`.
- `max_retries` must stay within `0` to `10`.
- `health_check_interval` must be greater than `0`, and when `enable_health_check=true` it must be at least `1s`.

### 2. Usage

```go
package main

import (
    "context"
    "encoding/json"
    "log"
    "strings"
    "time"

    es8 "github.com/elastic/go-elasticsearch/v8"
    "github.com/elastic/go-elasticsearch/v8/esapi"
    "github.com/go-lynx/lynx/app/boot"
    esplugin "github.com/go-lynx/lynx-elasticsearch"
)

func main() {
    // Start Lynx application
    boot.LynxApplication(wireApp).Run()
    
    // Get Elasticsearch client
    client := esplugin.GetElasticsearch()
    if client == nil {
        log.Fatal("failed to get elasticsearch client")
    }
    
    // Create index
    createIndex(client)
    
    // Index document
    indexDocument(client)
    
    // Search documents
    searchDocuments(client)
}

func createIndex(client *es8.Client) {
    ctx := context.Background()
    
    // Create index mapping
    mapping := `{
        "mappings": {
            "properties": {
                "title": {"type": "text"},
                "content": {"type": "text"},
                "created_at": {"type": "date"}
            }
        }
    }`
    
    req := esapi.IndicesCreateRequest{
        Index: "myapp_documents",
        Body:  strings.NewReader(mapping),
    }
    
    res, err := req.Do(ctx, client)
    if err != nil {
        log.Printf("failed to create index: %v", err)
        return
    }
    defer res.Body.Close()
    
    if res.IsError() {
        log.Printf("failed to create index: %s", res.String())
        return
    }
    
    log.Println("index created successfully")
}

func indexDocument(client *es8.Client) {
    ctx := context.Background()
    
    // Document data
    doc := map[string]interface{}{
        "title":     "Sample Document",
        "content":   "This is the content of a sample document",
        "created_at": time.Now(),
    }
    
    req := esapi.IndexRequest{
        Index:      "myapp_documents",
        DocumentID: "1",
        Body:       strings.NewReader(mustEncodeJSON(doc)),
        Refresh:    "true",
    }
    
    res, err := req.Do(ctx, client)
    if err != nil {
        log.Printf("failed to index document: %v", err)
        return
    }
    defer res.Body.Close()
    
    if res.IsError() {
        log.Printf("failed to index document: %s", res.String())
        return
    }
    
    log.Println("document indexed successfully")
}

func searchDocuments(client *es8.Client) {
    ctx := context.Background()
    
    // Search query
    query := `{
        "query": {
            "match": {
                "content": "sample"
            }
        }
    }`
    
    req := esapi.SearchRequest{
        Index: []string{"myapp_documents"},
        Body:  strings.NewReader(query),
    }
    
    res, err := req.Do(ctx, client)
    if err != nil {
        log.Printf("failed to search documents: %v", err)
        return
    }
    defer res.Body.Close()
    
    if res.IsError() {
        log.Printf("failed to search documents: %s", res.String())
        return
    }
    
    // Parse search results
    var result map[string]interface{}
    if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
        log.Printf("failed to decode response: %v", err)
        return
    }
    
    hits := result["hits"].(map[string]interface{})
    total := hits["total"].(map[string]interface{})
    log.Printf("found %v documents", total["value"])
}

func mustEncodeJSON(v interface{}) string {
    data, err := json.Marshal(v)
    if err != nil {
        panic(err)
    }
    return string(data)
}
```

## Configuration Options

See the proto-aligned table in the Quick Start section above for the complete field list, defaults, and examples sourced from `conf/elasticsearch.proto`.

## API Reference

### Get Client

```go
// Get Elasticsearch client
client := esplugin.GetElasticsearch()

// Get plugin instance
plugin := esplugin.GetElasticsearchPlugin()

// Get connection statistics
stats := plugin.GetConnectionStats()

// Get index name with prefix (e.g. index_prefix: "myapp" -> GetIndexName("documents") = "myapp_documents")
indexName := esplugin.GetIndexName("documents")
// Or via plugin:
indexName := plugin.GetIndexName("documents")
```

### Plugin Options

```go
// Configure plugin using option pattern
plugin := esplugin.NewElasticsearchClient(
    esplugin.WithAddresses([]string{"http://localhost:9200"}),
    esplugin.WithCredentials("elastic", "changeme"),
    esplugin.WithMaxRetries(5),
    esplugin.WithMetrics(true),
    esplugin.WithHealthCheck(true, 30*time.Second),
)
```

## Monitoring and Metrics

The plugin provides the following monitoring capabilities:

- **HTTP operation counters and latency histograms**
- **Cluster health status**
- **Approximate active node count**
- **Health-check success/failure counters**
- **Request error counters**

## Health Checks

The plugin supports automatic health checks and can monitor:

- Ping reachability to configured nodes
- Health-check latency
- Success/failure counts for background checks

## Error Handling

The plugin provides comprehensive error handling mechanisms:

- Automatic retry on connection failures
- Network timeout handling
- Authentication failure handling
- Cluster failover

## Best Practices

1. **Connection Pool Configuration**: Adjust connection pool size based on load
2. **Timeout Settings**: Reasonably set connection and query timeout times
3. **Retry Strategy**: Configure appropriate retry count and intervals
4. **Monitoring Alerts**: Enable metrics collection and health checks
5. **Security Configuration**: Use TLS and appropriate authentication methods

## Troubleshooting

### Common Issues

1. **Connection Failures**
   - Check server address and port
   - Verify network connectivity
   - Confirm firewall settings

2. **Authentication Failures**
   - Check username and password
   - Verify API Key or service token
   - Confirm authentication database configuration

3. **Performance Issues**
   - Adjust connection pool size
   - Optimize query statements
   - Check index configuration

### Log Debugging

The `log_level` field is currently stored in config but does not reconfigure plugin logging by itself. Use your application-level logger and the Prometheus metrics/health endpoints for troubleshooting.

## Validation

Current automated baseline in this workspace is `go test ./...` plus `go vet ./...`. See [VALIDATION.md](./VALIDATION.md) for the release-gate scope and the recommended manual smoke checks.

## License

This project is licensed under the Apache License 2.0.
