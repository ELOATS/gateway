package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryLoadPluginsFailsFastOnInvalidPlugin(t *testing.T) {
	dir := t.TempDir()
	invalid := []byte("name: broken\nheaders:\n  x: y\n")
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), invalid, 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	registry := &Registry{Plugins: map[string]PluginConfig{}}
	if err := registry.LoadPlugins(dir); err == nil {
		t.Fatal("expected invalid plugin to return an error")
	}
}
