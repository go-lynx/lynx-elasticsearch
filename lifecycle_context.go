package elasticsearch

// IsContextAware asserts that the plugin's lifecycle genuinely observes context
// cancellation: the core BasePlugin drives InitializeContext/StartContext/
// StopContext and routes into the InitializeResourcesContext,
// StartupTasksContext and CleanupTasksContext hooks in elasticsearch.go.
func (p *PlugElasticsearch) IsContextAware() bool {
	return true
}
