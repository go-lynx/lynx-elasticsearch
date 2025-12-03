package elasticsearch

import (
	"sync"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/go-lynx/lynx-elasticsearch/conf"
	"github.com/go-lynx/lynx/plugins"
)

// PlugElasticsearch represents an Elasticsearch plugin instance
type PlugElasticsearch struct {
	// Inherits from base plugin
	*plugins.BasePlugin
	// Elasticsearch configuration
	conf *conf.Elasticsearch
	// Elasticsearch client instance
	client *elasticsearch.Client
	// Metrics collection
	statsQuit   chan struct{}
	statsWG     sync.WaitGroup
	statsClosed bool
	statsMu     sync.Mutex
}
