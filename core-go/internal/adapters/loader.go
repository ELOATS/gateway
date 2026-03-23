package adapters

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Registry 存储所有通过 YAML 加载的动态插件。
type Registry struct {
	Plugins map[string]PluginConfig
}

var GlobalRegistry = &Registry{
	Plugins: make(map[string]PluginConfig),
}

// LoadPlugins 从指定目录及子目录搜索 .yaml 或 .yml 文件。
func (r *Registry) LoadPlugins(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			os.MkdirAll(dir, 0755)
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || (filepath.Ext(entry.Name()) != ".yaml" && filepath.Ext(entry.Name()) != ".yml") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cfg PluginConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			fmt.Printf("警告：解析插件配置文件 %s 失败: %v\n", path, err)
			continue
		}

		if cfg.Name == "" {
			cfg.Name = entry.Name()
		}
		r.Plugins[cfg.Name] = cfg
	}
	return nil
}

// GetPlugin 根据名称查找已加载的插件配置。
func (r *Registry) GetPlugin(name string) (PluginConfig, bool) {
	p, ok := r.Plugins[name]
	return p, ok
}
