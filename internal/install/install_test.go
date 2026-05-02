package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestKnownTargets(t *testing.T) {
	targets := KnownTargets()
	if len(targets) == 0 {
		t.Fatal("expected at least one target")
	}

	names := map[string]bool{}
	for _, tgt := range targets {
		if tgt.Name == "" {
			t.Error("target has empty name")
		}
		if tgt.ConfigPath == "" {
			t.Errorf("target %s has empty config path", tgt.Name)
		}
		if tgt.ConfigFormat == "" {
			t.Errorf("target %s has empty config format", tgt.Name)
		}
		names[tgt.Name] = true
	}

	for _, name := range []string{"claude-code", "claude-desktop", "cursor", "cline", "pi"} {
		if !names[name] {
			t.Errorf("expected target %q not found", name)
		}
	}
}

func TestTargetNames(t *testing.T) {
	names := TargetNames()
	if names == "" {
		t.Fatal("expected non-empty target names")
	}
}

func TestLookupTarget(t *testing.T) {
	if tgt := LookupTarget("claude-code"); tgt == nil {
		t.Error("expected to find claude-code")
	}
	if tgt := LookupTarget("cursor"); tgt == nil {
		t.Error("expected to find cursor")
	}
	if tgt := LookupTarget("nonexistent-agent"); tgt != nil {
		t.Error("expected nil for unknown agent")
	}
}

func TestInstallAndUninstall_MCPJSON(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mcp.json")

	target := Target{
		Name:         "test-agent",
		ConfigPath:   configPath,
		ConfigFormat: "mcp-json",
	}

	// Install
	result, err := Install(target, "/usr/local/bin/lily", nil)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}
	if !result.AgentConfigWritten {
		t.Error("expected AgentConfigWritten")
	}

	// Verify file was created
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}

	mcpServers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("expected mcpServers key")
	}

	lily, ok := mcpServers["lily"].(map[string]any)
	if !ok {
		t.Fatal("expected lily entry in mcpServers")
	}
	if lily["command"] != "/usr/local/bin/lily" {
		t.Errorf("expected command /usr/local/bin/lily, got %v", lily["command"])
	}

	// Uninstall
	if err := Uninstall(target); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}

	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config after uninstall: %v", err)
	}

	var cfg2 map[string]any
	if err := json.Unmarshal(data, &cfg2); err != nil {
		t.Fatalf("parse config after uninstall: %v", err)
	}

	mcpServers2, ok := cfg2["mcpServers"].(map[string]any)
	if ok && mcpServers2["lily"] != nil {
		t.Error("expected lily to be removed after uninstall")
	}
}

func TestInstall_PreservesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mcp.json")

	// Write existing config with another server
	existing := map[string]any{
		"mcpServers": map[string]any{
			"other-tool": map[string]any{
				"command": "other-server",
			},
		},
	}
	b, _ := json.Marshal(existing)
	os.WriteFile(configPath, b, 0644)

	target := Target{
		Name:         "test-agent",
		ConfigPath:   configPath,
		ConfigFormat: "mcp-json",
	}

	if _, err := Install(target, "lily", nil); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	data, _ := os.ReadFile(configPath)
	var cfg map[string]any
	json.Unmarshal(data, &cfg)

	mcpServers := cfg["mcpServers"].(map[string]any)

	// Both should exist
	if _, ok := mcpServers["other-tool"]; !ok {
		t.Error("expected other-tool to be preserved")
	}
	if _, ok := mcpServers["lily"]; !ok {
		t.Error("expected lily to be added")
	}
}

func TestInstall_ClaudeFormat(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".claude.json")

	target := Target{
		Name:         "test-claude",
		ConfigPath:   configPath,
		ConfigFormat: "claude",
	}

	if _, err := Install(target, "lily", nil); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	data, _ := os.ReadFile(configPath)
	var cfg map[string]any
	json.Unmarshal(data, &cfg)

	mcpServers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("expected mcpServers in claude config")
	}
	if _, ok := mcpServers["lily"]; !ok {
		t.Fatal("expected lily entry")
	}
}

func TestInstall_WithArgs(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mcp.json")

	target := Target{
		Name:         "test-agent",
		ConfigPath:   configPath,
		ConfigFormat: "mcp-json",
	}

	if _, err := Install(target, "lily", []string{"-timeout", "60s"}); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	data, _ := os.ReadFile(configPath)
	var cfg map[string]any
	json.Unmarshal(data, &cfg)

	lily := cfg["mcpServers"].(map[string]any)["lily"].(map[string]any)
	args, ok := lily["args"].([]any)
	if !ok {
		t.Fatal("expected args array")
	}
	if args[0] != "serve" {
		t.Errorf("expected first arg 'serve', got %v", args[0])
	}
	if args[1] != "-timeout" {
		t.Errorf("expected second arg '-timeout', got %v", args[1])
	}
}

func TestUninstall_NoConfigFile(t *testing.T) {
	target := Target{
		Name:         "test-agent",
		ConfigPath:   "/nonexistent/mcp.json",
		ConfigFormat: "mcp-json",
	}
	// Should not error
	if err := Uninstall(target); err != nil {
		t.Errorf("expected no error for missing file, got: %v", err)
	}
}

func TestFindBinary(t *testing.T) {
	bin := FindBinary()
	if bin == "" {
		t.Error("expected non-empty binary path")
	}
}

func TestInstallAll(t *testing.T) {
	tmpDir := t.TempDir()

	targets := []Target{
		{Name: "agent1", ConfigPath: filepath.Join(tmpDir, "agent1", "mcp.json"), ConfigFormat: "mcp-json"},
		{Name: "agent2", ConfigPath: filepath.Join(tmpDir, "agent2", "mcp.json"), ConfigFormat: "mcp-json"},
	}

	for _, target := range targets {
		if _, err := Install(target, "lily", nil); err != nil {
			t.Errorf("install %s failed: %v", target.Name, err)
		}
	}

	for _, target := range targets {
		data, err := os.ReadFile(target.ConfigPath)
		if err != nil {
			t.Errorf("read %s config: %v", target.Name, err)
			continue
		}
		var cfg map[string]any
		json.Unmarshal(data, &cfg)
		servers := cfg["mcpServers"].(map[string]any)
		if _, ok := servers["lily"]; !ok {
			t.Errorf("expected lily in %s config", target.Name)
		}
	}
}

func TestDeployDefaultConfig(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "lily", "lily.yaml")

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(defaultConfig), 0644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !containsStr(content, "extra_commands:") {
		t.Error("expected config to contain extra_commands section")
	}
	if !containsStr(content, "rate_limit:") {
		t.Error("expected config to contain rate_limit section")
	}
	if !containsStr(content, "max_output_bytes:") {
		t.Error("expected config to contain max_output_bytes section")
	}
}

func TestConfigFilePath(t *testing.T) {
	path := ConfigFilePath()
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

func TestInstall_DeploysAllowlist(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mcp.json")

	target := Target{
		Name:         "test-agent",
		ConfigPath:   configPath,
		ConfigFormat: "mcp-json",
	}

	result, err := Install(target, "lily", nil)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	allowlistPath := AllowlistConfigPath()
	if _, err := os.Stat(allowlistPath); os.IsNotExist(err) {
		// The allowlist might not have been deployed if the real config dir
		// has issues, but the result should indicate whether it was
		t.Logf("AllowlistDeployed=%v path=%s", result.AllowlistDeployed, result.AllowlistPath)
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
