package adapters

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Registry struct {
	Plugins map[string]PluginConfig
}

var GlobalRegistry = &Registry{Plugins: make(map[string]PluginConfig)}

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
