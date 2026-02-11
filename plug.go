package elasticsearch

import (
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/go-lynx/lynx"
	"github.com/go-lynx/lynx/pkg/factory"
	"github.com/go-lynx/lynx/plugins"
)

// init function is a special function in Go that is automatically executed when the package is loaded.
// This function registers the Elasticsearch client plugin to the global plugin factory.
func init() {
	// Call the RegisterPlugin method of the global plugin factory to register the plugin.
	// The first parameter pluginName is the unique name of the plugin, used to identify the plugin.
	// The second parameter confPrefix is the configuration prefix, used to read plugin-related configuration from the config.
	// The third parameter is an anonymous function that returns an instance of plugins.Plugin interface type,
	// by calling the NewElasticsearchClient function to create a new Elasticsearch client plugin instance.
	factory.GlobalTypedFactory().RegisterPlugin(pluginName, confPrefix, func() plugins.Plugin {
		return NewElasticsearchClient()
	})
}

// GetElasticsearch function is used to get the Elasticsearch client instance.
// It gets the plugin manager through the global Lynx application instance, then gets the corresponding plugin instance by plugin name,
// finally converts the plugin instance to *PlugElasticsearch type and returns its client field, which is the Elasticsearch client.
func GetElasticsearch() *elasticsearch.Client {
	plugin := GetElasticsearchPlugin()
	if plugin == nil {
		return nil
	}
	return plugin.GetClient()
}

// GetElasticsearchPlugin gets the Elasticsearch plugin instance.
// Returns nil if the plugin is not loaded or type assertion fails.
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

// GetIndexName returns the index name with configured prefix applied.
// Convenience function that delegates to GetElasticsearchPlugin().GetIndexName.
// Returns name as-is if plugin is not available or index_prefix is not configured.
func GetIndexName(name string) string {
	plugin := GetElasticsearchPlugin()
	if plugin == nil {
		return name
	}
	return plugin.GetIndexName(name)
}
