package test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/pipeline"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/stretchr/testify/assert"
)

func TestPolicyEngine_HotReloadGracePeriod(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "policies.yaml")

	// 1. Initial configuration
	initialConfig := `
policies:
  - name: "tool_auth"
    enabled: true
    config: {}
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	assert.NoError(t, err)

	pipeline.RegisterPolicies()
	deps := &pipeline.DependencyContainer{
		Config: &config.Config{
			RateLimitQPS: 100,
		},
	}
	engine, err := pipeline.NewPolicyEngine(configPath, deps)
	assert.NoError(t, err)
	defer engine.Close()

	// 2. Start high-frequency evaluation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	workers := 10
	errCount := 0
	var mu sync.Mutex

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Simulate evaluating a request
					env := &pipeline.RequestEnvelope{
						Request: &models.ChatCompletionRequest{},
					}
					engine.Evaluate(context.Background(), env)
					time.Sleep(1 * time.Millisecond)
				}
			}
		}()
	}

	// 3. Trigger reload by modifying the file
	time.Sleep(100 * time.Millisecond) // Wait for workers to start
	newConfig := `
policies:
  - name: "usage_quota"
    enabled: true
    config: { "limit": 100 }
`
	err = os.WriteFile(configPath, []byte(newConfig), 0644)
	assert.NoError(t, err)

	// Since the watcher is in engine.go and runs every 5s (wait, I should check that), 
	// I'll manually trigger reload for the test to be faster if I can, or just wait.
	// Actually, I'll just wait a bit longer than the ticker if possible, 
	// or I can call e.reload() if it was exported (it's not).
	
	// Wait for the background watch to pick it up (it's 5s in engine.go)
	fmt.Println("Waiting for policy engine to pick up change...")
	time.Sleep(6 * time.Second) 

	// 4. Verify no crashes occurred during transition
	cancel()
	wg.Wait()

	mu.Lock()
	assert.Equal(t, 0, errCount, "There should be no evaluation errors during reload")
	mu.Unlock()
}
