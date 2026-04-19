package adapters

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Registry 维护了所有已加载的动态适配器插件及其配置。
type Registry struct {
	Plugins map[string]PluginConfig // 已注册的插件映射表
}

// GlobalRegistry 提供了一个单例入口，用于全局存取已动态加载的适配器。
var GlobalRegistry = &Registry{Plugins: make(map[string]PluginConfig)}

// LoadPlugins 扫描指定目录下的所有 YAML 文件，并将其解析为动态适配器配置。
// 设计功能：支持自动纠错，如果目标目录不存在则尝试自动创建。
func (r *Registry) LoadPlugins(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			_ = os.MkdirAll(dir, 0o755)
			return nil
		}
		return err
	}

	var invalid []string
	for _, entry := range entries {
		if entry.IsDir() || (filepath.Ext(entry.Name()) != ".yaml" && filepath.Ext(entry.Name()) != ".yml") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			invalid = append(invalid, fmt.Sprintf("%s: read failed: %v", path, err))
			continue
		}

		var cfg PluginConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			invalid = append(invalid, fmt.Sprintf("%s: invalid yaml: %v", path, err))
			continue
		}

		if cfg.Name == "" {
			cfg.Name = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		if err := validatePluginConfig(cfg); err != nil {
			invalid = append(invalid, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		r.Plugins[cfg.Name] = cfg
	}

	if len(invalid) > 0 {
		return fmt.Errorf("plugin validation failed: %s", strings.Join(invalid, "; "))
	}
	return nil
}

// GetPlugin 根据名称从内存注册表中检索已加载的插件配置。
func (r *Registry) GetPlugin(name string) (PluginConfig, bool) {
	p, ok := r.Plugins[name]
	return p, ok
}

func validatePluginConfig(cfg PluginConfig) error {
	if strings.TrimSpace(cfg.Name) == "" {
		return fmt.Errorf("plugin name is required")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return fmt.Errorf("plugin base_url is required")
	}
	return nil
}
