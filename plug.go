package elasticsearch

import (
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/go-lynx/lynx"
	"github.com/go-lynx/lynx/pkg/factory"
	"github.com/go-lynx/lynx/plugins"
)

// init registers the Elasticsearch plugin with the global factory on import.
func init() {
	factory.GlobalTypedFactory().RegisterPlugin(pluginName, confPrefix, func() plugins.Plugin {
		return NewElasticsearchClient()
	})
}

// GetElasticsearch returns the Elasticsearch client, or nil if the plugin is not loaded.
func GetElasticsearch() *elasticsearch.Client {
	plugin := GetElasticsearchPlugin()
	if plugin == nil {
		return nil
	}
	return plugin.GetClient()
}

// GetElasticsearchPlugin returns the Elasticsearch plugin instance, or nil.
func GetElasticsearchPlugin() *PlugElasticsearch {
	plugin := lynx.Lynx().GetPluginManager().GetPlugin(pluginName)
	if plugin == nil {
		return nil
	}
	es, ok := plugin.(*PlugElasticsearch)
	if !ok {
		return nil
	}
	return es
}

// GetIndexName returns the index name with the configured prefix applied,
// or the raw name when the plugin is not loaded.
func GetIndexName(name string) string {
	plugin := GetElasticsearchPlugin()
	if plugin == nil {
		return name
	}
	return plugin.GetIndexName(name)
}
