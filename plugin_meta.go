package elasticsearch

import (
	"github.com/go-lynx/lynx/plugins"
)

const (
	pluginName        = "elasticsearch.client"
	pluginVersion     = "v1.6.1"
	pluginDescription = "elasticsearch plugin for lynx framework"
	confPrefix        = "lynx.elasticsearch"
)

// NewElasticsearchClient creates a new Elasticsearch plugin instance.
func NewElasticsearchClient() *PlugElasticsearch {
	return &PlugElasticsearch{
		BasePlugin: plugins.NewBasePlugin(
			plugins.GeneratePluginID("", pluginName, pluginVersion),
			pluginName,
			pluginDescription,
			pluginVersion,
			confPrefix,
			100,
		),
	}
}
