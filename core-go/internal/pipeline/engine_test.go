package pipeline

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-gateway/core/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyEngine_EvaluationOrder(t *testing.T) {
	// 注册两个测试策略，用于验证顺序
	RegisterPolicy("test_a", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		return &testPolicy{name: "a"}, nil
	})
	RegisterPolicy("test_b", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		return &testPolicy{name: "b"}, nil
	})

	yamlCfg := `
policies:
  - name: test_b
    enabled: true
  - name: test_a
    enabled: true
`
	engine, err := NewPolicyEngineFromReader(strings.NewReader(yamlCfg), nil)
	require.NoError(t, err)

	names := engine.GetChainNames()
	assert.Equal(t, []string{"b", "a"}, names, "策略执行顺序应与 YAML 定义一致")
}

func TestPolicyEngine_StopOnDeny(t *testing.T) {
	RegisterPolicy("deny_all", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		return &testPolicy{name: "deny", deny: true}, nil
	})
	RegisterPolicy("never_reached", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		return &testPolicy{name: "unreachable"}, nil
	})

	yamlCfg := `
policies:
  - name: deny_all
    enabled: true
  - name: never_reached
    enabled: true
`
	engine, err := NewPolicyEngineFromReader(strings.NewReader(yamlCfg), nil)
	require.NoError(t, err)

	env := &RequestEnvelope{Prompt: "hello", Request: &models.ChatCompletionRequest{}}
	decision := engine.Evaluate(context.Background(), env)

	assert.False(t, decision.Allow)
	assert.Equal(t, "deny", decision.Reason)
}

type testPolicy struct {
	name string
	deny bool
}

func (p *testPolicy) Name() string { return p.name }
func (p *testPolicy) Close()       {}
func (p *testPolicy) Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	if p.deny {
		return &PolicyDecision{Allow: false, Reason: p.name, StatusCode: http.StatusForbidden}
	}
	return nil
}
