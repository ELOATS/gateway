package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigResolvesPolicyFileFromNestedWorkingDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}

	nested := filepath.Join(wd, "..", "handlers")
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}
	defer func() { _ = os.Chdir(wd) }()

	cfg := LoadConfig()
	if cfg.Paths.PolicyFile == "" {
		t.Fatal("expected policy file path to be resolved")
	}
	if _, err := os.Stat(cfg.Paths.PolicyFile); err != nil {
		t.Fatalf("expected resolved policy file to exist, got %v", err)
	}
}
