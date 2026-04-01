package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// PolicyFactory 是创建策略实例的工厂函数。
type PolicyFactory func(deps *DependencyContainer, config map[string]any) (Policy, error)

var registry = make(map[string]PolicyFactory)

// RegisterPolicy 允许外部（例如在 policy.go 中）注册可供引擎使用的策略类型。
func RegisterPolicy(name string, factory PolicyFactory) {
	registry[name] = factory
}

// PolicyConfig 对应 YAML 中单个策略的定义。
type PolicyConfig struct {
	Name    string         `yaml:"name"`
	Enabled bool           `yaml:"enabled"`
	Config  map[string]any `yaml:"config"`
}

// EngineConfig 对应 policies.yaml 的顶层结构。
type EngineConfig struct {
	Policies []PolicyConfig `yaml:"policies"`
}

// PolicyEngine 实现了基于声明式配置的策略决策。
type PolicyEngine struct {
	mu           sync.RWMutex
	chain        []Policy
	configPath   string
	deps         *DependencyContainer
	lastModTime  time.Time
}

// NewPolicyEngine 从文件路径加载并初始化策略引擎，并开启自动热加载。
func NewPolicyEngine(path string, deps *DependencyContainer) (*PolicyEngine, error) {
	engine := &PolicyEngine{
		configPath: path,
		deps:       deps,
	}

	if err := engine.reload(); err != nil {
		return nil, err
	}

	// 开启背景监控
	go engine.watch(context.Background())

	return engine, nil
}

// reload 从文件重新加载配置。调用方需处理锁逻辑。
func (e *PolicyEngine) reload() error {
	info, err := os.Stat(e.configPath)
	if err != nil {
		return fmt.Errorf("stat config: %w", err)
	}

	// 如果文件没变，跳过
	if info.ModTime().Equal(e.lastModTime) {
		return nil
	}

	data, err := os.ReadFile(e.configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var cfg EngineConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}

	var newChain []Policy
	for _, pc := range cfg.Policies {
		if !pc.Enabled {
			continue
		}
		factory, ok := registry[pc.Name]
		if !ok {
			return fmt.Errorf("unknown policy: %s", pc.Name)
		}
		p, err := factory(e.deps, pc.Config)
		if err != nil {
			return fmt.Errorf("init policy %s: %w", pc.Name, err)
		}
		newChain = append(newChain, p)
	}

	e.mu.Lock()
	oldChain := e.chain
	e.chain = newChain
	e.lastModTime = info.ModTime()
	e.mu.Unlock()

	// 清理旧策略占用的背景资源（例如计时器、协程）
	for _, p := range oldChain {
		p.Close()
	}

	slog.Info("policy engine reloaded successfully", "path", e.configPath, "policies", len(newChain))
	return nil
}

// watch 定期检查配置文件变化进行热加载。
func (e *PolicyEngine) watch(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.reload(); err != nil {
				slog.Error("background policy reload failed", "error", err)
			}
		}
	}
}

// NewPolicyEngineFromReader 从 reader 加载并初始化策略引擎。
func NewPolicyEngineFromReader(r io.Reader, deps *DependencyContainer) (*PolicyEngine, error) {
	var cfg EngineConfig
	if err := yaml.NewDecoder(r).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode policies config: %w", err)
	}

	engine := &PolicyEngine{}
	for _, pc := range cfg.Policies {
		if !pc.Enabled {
			continue
		}

		factory, ok := registry[pc.Name]
		if !ok {
			return nil, fmt.Errorf("unknown policy type: %s", pc.Name)
		}

		policy, err := factory(deps, pc.Config)
		if err != nil {
			return nil, fmt.Errorf("initialize policy %s: %w", pc.Name, err)
		}

		engine.chain = append(engine.chain, policy)
	}

	return engine, nil
}

// Evaluate 按顺序执行策略链。返回第一个拒绝请求的策略结果。
func (e *PolicyEngine) Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	e.mu.RLock()
	chain := e.chain
	e.mu.RUnlock()

	var combinedDegraded bool
	var combinedReason string
	var sanitizedPrompt = env.Prompt

	for _, p := range chain {
		decision := p.Evaluate(ctx, env)
		if decision == nil {
			continue
		}
		if !decision.Allow {
			return decision
		}
		if decision.Degraded {
			combinedDegraded = true
			if combinedReason == "" {
				combinedReason = decision.DegradeReason
			} else {
				combinedReason += "; " + decision.DegradeReason
			}
		}
		if decision.SanitizedPrompt != "" && decision.SanitizedPrompt != env.Prompt {
			sanitizedPrompt = decision.SanitizedPrompt
			env.Prompt = sanitizedPrompt
		}
	}

	return &PolicyDecision{
		Allow:           true,
		SanitizedPrompt: sanitizedPrompt,
		Degraded:        combinedDegraded,
		DegradeReason:   combinedReason,
	}
}

// GetChainNames 返回当前链条中的策略名称，用于调试。
func (e *PolicyEngine) GetChainNames() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var names []string
	for _, p := range e.chain {
		names = append(names, p.Name())
	}
	return names
}
