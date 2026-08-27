package elasticsearch

import (
	"context"
	"testing"
	"time"

	"github.com/go-lynx/lynx-elasticsearch/conf"
	"github.com/go-lynx/lynx/pkg/security"
	"github.com/go-lynx/lynx/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The production lifecycle policy (lynx/internal/app/lifecycle_policy.go) rejects
// any plugin for which plugins.HasTrueContextLifecycle is false when
// security.IsProduction() is true. These tests pin the plugin to that contract.
func TestPlugElasticsearch_HasTrueContextLifecycle(t *testing.T) {
	p := NewElasticsearchClient()

	caps := plugins.DescribePluginCapabilities(p)
	assert.True(t, caps.HasLifecycleWithCtx, "plugin must expose StartContext/StopContext/InitializeContext")
	assert.True(t, caps.HasContextSteps, "plugin must implement a context-aware step hook")
	assert.True(t, caps.IsTrulyContextAware)
	assert.True(t, plugins.HasTrueContextLifecycle(p))

	_, ok := plugins.GetTrueContextLifecycle(p)
	assert.True(t, ok)

	var _ plugins.ContextResourceInitializer = p
	var _ plugins.ContextStartupTasker = p
	var _ plugins.ContextCleanupTasker = p
}

func TestPlugElasticsearch_ProductionLifecyclePolicyAccepts(t *testing.T) {
	t.Setenv("LYNX_ENV", "production")
	require.True(t, security.IsProduction())

	p := NewElasticsearchClient()
	assert.True(t, plugins.HasTrueContextLifecycle(p),
		"plugin %s would be rejected by the production lifecycle policy", p.Name())
}

func TestPlugElasticsearch_StartupTasksContext_ObservesCancellation(t *testing.T) {
	p := NewElasticsearchClient()
	p.conf = &conf.Elasticsearch{Addresses: []string{"http://127.0.0.1:1"}}
	require.NoError(t, p.createClient())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := p.StartupTasksContext(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), time.Second, "cancelled startup must return promptly without pinging")
}

func TestPlugElasticsearch_InitializeResourcesContext_ObservesCancellation(t *testing.T) {
	p := NewElasticsearchClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.InitializeResourcesContext(ctx, plugins.NewSimpleRuntime())
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, p.statsQuit, "no background tasks may be launched after cancellation")
}

func TestPlugElasticsearch_CleanupTasksContext_BoundedByContext(t *testing.T) {
	p := NewElasticsearchClient()
	p.statsQuit = make(chan struct{})

	// A worker that ignores statsQuit for a while: cleanup must not block past ctx.
	release := make(chan struct{})
	p.statsWG.Go(func() { <-release })
	t.Cleanup(func() { close(release); p.statsWG.Wait() })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := p.CleanupTasksContext(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second)
	select {
	case <-p.statsQuit:
	default:
		t.Fatal("workers must still be signalled to stop")
	}
}
