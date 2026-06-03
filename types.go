package elasticsearch

import (
	"sync"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/go-lynx/lynx-elasticsearch/conf"
	"github.com/go-lynx/lynx/plugins"
)

// PlugElasticsearch is the Lynx plugin that manages the lifecycle of an Elasticsearch
// client.  It initialises the client from YAML configuration, bootstraps background
// goroutines for metrics collection and health checks, and exposes the underlying
// *elasticsearch.Client for use by application code via GetClient or the package-level
// GetElasticsearch helper.
type PlugElasticsearch struct {
	// BasePlugin provides plugin identity, configuration prefix, and lifecycle helpers.
	*plugins.BasePlugin
	// conf holds the parsed plugin configuration.
	conf *conf.Elasticsearch
	// client is the underlying Elasticsearch client instance.
	client *elasticsearch.Client
	// statsQuit is closed to signal background goroutines to exit.
	statsQuit chan struct{}
	// statsWG tracks running background goroutines so Stop can wait for them.
	statsWG sync.WaitGroup
	// statsClosed ensures statsQuit is only closed once.
	statsClosed bool
	// statsMu guards statsClosed.
	statsMu sync.Mutex
}
