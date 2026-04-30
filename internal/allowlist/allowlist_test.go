package allowlist

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_Nonexistent(t *testing.T) {
	cfg, err := Load("/nonexistent/path/allowlist.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if len(cfg.ExtraCommands) != 0 {
		t.Error("expected empty extra commands")
	}
}

func TestLoad_Valid(t *testing.T) {
	content := `
extra_commands:
  - docker
  - kubectl

extra_subcommand_restrictions:
  docker:
    - ps
    - logs
    - inspect
  kubectl:
    - get
    - describe

extra_blocked_flags:
  docker:
    - exec
    - run
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "allowlist.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.ExtraCommands) != 2 {
		t.Errorf("expected 2 extra commands, got %d", len(cfg.ExtraCommands))
	}
	if cfg.ExtraCommands[0] != "docker" || cfg.ExtraCommands[1] != "kubectl" {
		t.Errorf("unexpected commands: %v", cfg.ExtraCommands)
	}

	if len(cfg.ExtraSubcommandRestrictions) != 2 {
		t.Errorf("expected 2 restricted commands, got %d", len(cfg.ExtraSubcommandRestrictions))
	}
	if subs, ok := cfg.ExtraSubcommandRestrictions["docker"]; !ok || len(subs) != 3 {
		t.Errorf("expected docker with 3 subs, got %v", cfg.ExtraSubcommandRestrictions["docker"])
	}

	if len(cfg.ExtraBlockedFlags) != 1 {
		t.Errorf("expected 1 blocked flag entry, got %d", len(cfg.ExtraBlockedFlags))
	}
	if flags, ok := cfg.ExtraBlockedFlags["docker"]; !ok || len(flags) != 2 {
		t.Errorf("expected docker with 2 blocked flags, got %v", cfg.ExtraBlockedFlags["docker"])
	}
}

func TestLoad_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "allowlist.yaml")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ExtraCommands) != 0 {
		t.Error("expected empty config")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "allowlist.yaml")
	if err := os.WriteFile(path, []byte("{{invalid yaml"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestSubcommandRestrictions(t *testing.T) {
	cfg := &Config{
		ExtraSubcommandRestrictions: map[string][]string{
			"docker":   {"ps", "logs", "inspect"},
			"kubectl":  {"get", "describe"},
		},
	}

	subs := cfg.SubcommandRestrictions()
	if len(subs) != 2 {
		t.Errorf("expected 2 entries, got %d", len(subs))
	}

	dockerSubs := subs["docker"]
	if len(dockerSubs) != 3 {
		t.Errorf("expected 3 docker subs, got %d", len(dockerSubs))
	}
	if !dockerSubs["ps"] || !dockerSubs["logs"] || !dockerSubs["inspect"] {
		t.Errorf("unexpected docker subs: %v", dockerSubs)
	}
}

func TestBlockedFlags(t *testing.T) {
	cfg := &Config{
		ExtraBlockedFlags: map[string][]string{
			"docker": {"exec", "run", "rm"},
		},
	}

	flags := cfg.BlockedFlags()
	if len(flags) != 1 {
		t.Errorf("expected 1 entry, got %d", len(flags))
	}
	if len(flags["docker"]) != 3 {
		t.Errorf("expected 3 docker flags, got %d", len(flags["docker"]))
	}
}

func TestDefaultConfigPath(t *testing.T) {
	path := DefaultConfigPath()
	if path == "" {
		t.Error("expected non-empty path")
	}
	if !filepath.IsAbs(path) {
		t.Errorf("expected absolute path, got %s", path)
	}
}

func TestValidate(t *testing.T) {
	cfg := &Config{
		ExtraCommands: []string{"docker"},
		ExtraSubcommandRestrictions: map[string][]string{
			"docker": {"ps", "logs"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestEnsureConfigDir(t *testing.T) {
	// Just verify it doesn't error
	if err := EnsureConfigDir(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
