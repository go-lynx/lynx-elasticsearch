# Validation

## Automated Baseline

Current workspace baseline:

```bash
go test ./...
go vet ./...
```

## What This Means

- Unit tests cover defaults, index helpers, path parsing, and the production guardrails that reject invalid `connect_timeout`, `max_retries`, and `health_check_interval` values before background health checks can panic.
- `go vet ./...` is part of the local release gate and must stay green alongside `go test ./...`.
- README examples still document the public surface, but they are not a substitute for a live Elasticsearch smoke test.

## Recommended Manual Checks

- Start against a reachable Elasticsearch cluster and verify `GetElasticsearch()` returns a non-nil client after plugin initialization.
- Confirm invalid values such as `connect_timeout: "0s"` or `health_check_interval: "0s"` fail fast during initialization instead of reaching runtime panic paths.
- Enable `enable_health_check` and `enable_metrics`, then confirm background ping/cluster-health collection works without leaking goroutines on shutdown.
- Verify `index_prefix` through `GetIndexName("documents")` and exercise at least one create/index/search request with the sample config.
