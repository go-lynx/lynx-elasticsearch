# Validation

## Automated Baseline

Current workspace baseline:

```bash
go test ./...
```

Result summary:

```text
?   	github.com/go-lynx/lynx-elasticsearch       [no test files]
?   	github.com/go-lynx/lynx-elasticsearch/conf  [no test files]
```

## What This Means

- This module currently has no committed Go test files in the workspace.
- README examples document the public surface, but they are not covered by automated unit or integration tests yet.
- Any future changes around background metrics collection, health checks, or request retry behavior should add executable tests before the README claims stronger guarantees.

## Recommended Manual Checks

- Start against a reachable Elasticsearch cluster and verify `GetElasticsearch()` returns a non-nil client after plugin initialization.
- Enable `enable_health_check` and `enable_metrics`, then confirm background ping/cluster-health collection works without leaking goroutines on shutdown.
- Verify `index_prefix` through `GetIndexName("documents")` and exercise at least one create/index/search request with the sample config.
