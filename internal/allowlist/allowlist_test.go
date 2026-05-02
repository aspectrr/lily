package allowlist

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_Nonexistent(t *testing.T) {
	cfg, err := Load("/nonexistent/path/lily.yaml")
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

rate_limit: "2s"
max_output_bytes: 2097152
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "lily.yaml")
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

	if cfg.RateLimit != "2s" {
		t.Errorf("expected rate_limit 2s, got %q", cfg.RateLimit)
	}
	if cfg.MaxOutputBytes != 2097152 {
		t.Errorf("expected max_output_bytes 2097152, got %d", cfg.MaxOutputBytes)
	}
}

func TestLoad_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "lily.yaml")
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
	path := filepath.Join(tmpDir, "lily.yaml")
	if err := os.WriteFile(path, []byte("{{invalid yaml"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoad_LegacyAllowlistFallback(t *testing.T) {
	tmpDir := t.TempDir()

	// Create legacy allowlist.yaml but no lily.yaml
	legacyPath := filepath.Join(tmpDir, "allowlist.yaml")
	content := `
extra_commands:
  - docker
`
	if err := os.WriteFile(legacyPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Load from a path that doesn't exist — should not trigger legacy fallback
	// (legacy fallback only works with the default config path)
	// This test verifies the Load function handles missing files gracefully.
	cfg, err := Load(filepath.Join(tmpDir, "lily.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ExtraCommands) != 0 {
		t.Error("expected empty config when lily.yaml is missing and path is explicit")
	}
}

func TestGetRateLimit(t *testing.T) {
	tests := []struct {
		config   string
		expected time.Duration
	}{
		{"", DefaultRateLimit},
		{"500ms", 500 * time.Millisecond},
		{"2s", 2 * time.Second},
		{"1m", 1 * time.Minute},
	}

	for _, tc := range tests {
		cfg := &Config{RateLimit: tc.config}
		got := cfg.GetRateLimit()
		if got != tc.expected {
			t.Errorf("GetRateLimit(%q) = %v, want %v", tc.config, got, tc.expected)
		}
	}

	// Invalid duration should return default
	cfg := &Config{RateLimit: "not-a-duration"}
	if got := cfg.GetRateLimit(); got != DefaultRateLimit {
		t.Errorf("GetRateLimit(invalid) = %v, want %v", got, DefaultRateLimit)
	}
}

func TestGetMaxOutputBytes(t *testing.T) {
	tests := []struct {
		config   int
		expected int64
	}{
		{0, DefaultMaxOutputBytes},
		{-1, DefaultMaxOutputBytes},
		{512, 1024}, // minimum is 1KB
		{1024, 1024},
		{5242880, 5242880}, // 5 MB
	}

	for _, tc := range tests {
		cfg := &Config{MaxOutputBytes: tc.config}
		got := cfg.GetMaxOutputBytes()
		if got != tc.expected {
			t.Errorf("GetMaxOutputBytes(%d) = %d, want %d", tc.config, got, tc.expected)
		}
	}
}

func TestValidate_InvalidRateLimit(t *testing.T) {
	cfg := &Config{RateLimit: "not-valid"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid rate_limit")
	}
}

func TestValidate_NegativeMaxOutput(t *testing.T) {
	cfg := &Config{MaxOutputBytes: -1}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for negative max_output_bytes")
	}
}

func TestSubcommandRestrictions(t *testing.T) {
	cfg := &Config{
		ExtraSubcommandRestrictions: map[string][]string{
			"docker":  {"ps", "logs", "inspect"},
			"kubectl": {"get", "describe"},
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
	if filepath.Base(path) != "lily.yaml" {
		t.Errorf("expected filename lily.yaml, got %s", filepath.Base(path))
	}
}

func TestEnsureConfigDir(t *testing.T) {
	// Just verify it doesn't error
	if err := EnsureConfigDir(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
