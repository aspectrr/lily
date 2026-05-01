package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Target represents an agent that can have lily installed.
type Target struct {
	Name         string
	ConfigPath   string // Absolute path to the agent's MCP config file
	ConfigFormat string // "claude" or "mcp-json"
}

// KnownTargets returns all known agent targets on this system.
func KnownTargets() []Target {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}

	var targets []Target

	// Claude Code (claude-cli / Claude Desktop)
	targets = append(targets, Target{
		Name:         "claude-code",
		ConfigPath:   filepath.Join(home, ".claude.json"),
		ConfigFormat: "claude",
	})
	targets = append(targets, Target{
		Name:         "claude-desktop",
		ConfigPath:   claudeDesktopConfigPath(home),
		ConfigFormat: "mcp-json",
	})

	// Cursor
	targets = append(targets, Target{
		Name:         "cursor",
		ConfigPath:   filepath.Join(home, ".cursor", "mcp.json"),
		ConfigFormat: "mcp-json",
	})

	// Cursor (project-level example)
	targets = append(targets, Target{
		Name:         "cursor-project",
		ConfigPath:   ".cursor/mcp.json",
		ConfigFormat: "mcp-json",
	})

	// Windsurf / Codeium
	targets = append(targets, Target{
		Name:         "windsurf",
		ConfigPath:   filepath.Join(home, ".codeium", "windsurf", "mcp.json"),
		ConfigFormat: "mcp-json",
	})

	// Cline (VS Code extension)
	targets = append(targets, Target{
		Name:         "cline",
		ConfigPath:   filepath.Join(home, ".cline", "mcp_settings.json"),
		ConfigFormat: "mcp-json",
	})

	// Pi (pi-coding-agent)
	targets = append(targets, Target{
		Name:         "pi",
		ConfigPath:   filepath.Join(home, ".pi", "mcp.json"),
		ConfigFormat: "mcp-json",
	})

	// Goose (Block)
	targets = append(targets, Target{
		Name:         "goose",
		ConfigPath:   filepath.Join(home, ".config", "goose", "mcp.json"),
		ConfigFormat: "mcp-json",
	})

	return targets
}

func claudeDesktopConfigPath(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "Claude", "claude_desktop_config.json")
	default:
		return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
	}
}

// DetectedTargets returns only targets whose config files (or parent dirs) exist.
func DetectedTargets() []Target {
	var detected []Target
	for _, t := range KnownTargets() {
		// Check if the config file exists or its parent directory exists
		if fileExists(t.ConfigPath) || dirExists(filepath.Dir(t.ConfigPath)) {
			detected = append(detected, t)
		}
	}
	return detected
}

// InstallResult holds the outcome of installing lily.
type InstallResult struct {
	AgentConfigWritten bool
	AllowlistDeployed  bool
	AllowlistPath      string
}

// Install adds lily to the given target's MCP configuration and
// deploys the default allowlist config if one doesn't already exist.
// If binaryPath is empty, it uses "lily" (assumes it's on PATH).
func Install(target Target, binaryPath string, args []string) (*InstallResult, error) {
	if binaryPath == "" {
		binaryPath = "lily"
	}

	result := &InstallResult{}

	// Write the agent MCP config
	var err error
	switch target.ConfigFormat {
	case "mcp-json":
		err = installMCPJSON(target.ConfigPath, binaryPath, args)
	case "claude":
		err = installClaude(target.ConfigPath, binaryPath, args)
	default:
		err = fmt.Errorf("unknown config format: %s", target.ConfigFormat)
	}
	if err != nil {
		return result, err
	}
	result.AgentConfigWritten = true

	// Deploy default allowlist config if it doesn't exist
	allowlistPath := AllowlistConfigPath()
	if !fileExists(allowlistPath) {
		if err := DeployDefaultAllowlist(); err == nil {
			result.AllowlistDeployed = true
		}
		// Non-fatal: the server works fine without a config file
	}
	result.AllowlistPath = allowlistPath

	return result, nil
}

// Uninstall removes lily from the given target's MCP configuration.
func Uninstall(target Target) error {
	switch target.ConfigFormat {
	case "mcp-json":
		return uninstallMCPJSON(target.ConfigPath)
	case "claude":
		return uninstallClaude(target.ConfigPath)
	default:
		return fmt.Errorf("unknown config format: %s", target.ConfigFormat)
	}
}

func installMCPJSON(configPath, binaryPath string, args []string) error {
	cfg := make(map[string]any)

	// Read existing config
	data, err := os.ReadFile(configPath)
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			// Try wrapping in mcpServers if it's a flat file
			cfg = make(map[string]any)
		}
	}

	// Ensure mcpServers exists
	mcpServers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = make(map[string]any)
		cfg["mcpServers"] = mcpServers
	}

	// Build the server entry
	fullArgs := append([]string{"serve"}, args...)
	mcpServers["lily"] = map[string]any{
		"command": binaryPath,
		"args":    fullArgs,
	}

	return writeJSON(configPath, cfg)
}

func uninstallMCPJSON(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	cfg := make(map[string]any)
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	mcpServers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		return nil
	}

	delete(mcpServers, "lily")

	// Remove mcpServers key if empty
	if len(mcpServers) == 0 {
		delete(cfg, "mcpServers")
	}

	return writeJSON(configPath, cfg)
}

func installClaude(configPath, binaryPath string, args []string) error {
	cfg := make(map[string]any)

	data, err := os.ReadFile(configPath)
	if err == nil && len(data) > 0 {
		json.Unmarshal(data, &cfg)
	}

	// Claude uses mcpServers at the top level
	mcpServers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = make(map[string]any)
		cfg["mcpServers"] = mcpServers
	}

	fullArgs := append([]string{"serve"}, args...)
	mcpServers["lily"] = map[string]any{
		"command": binaryPath,
		"args":    fullArgs,
	}

	return writeJSON(configPath, cfg)
}

func uninstallClaude(configPath string) error {
	return uninstallMCPJSON(configPath)
}

func writeJSON(path string, data any) error {
	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, b, 0644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// FindBinary attempts to locate the lily binary.
func FindBinary() string {
	// Check common locations
	home, err := os.UserHomeDir()
	if err != nil {
		home = "" // fall back to non-home candidates
	}
	candidates := []string{
		filepath.Join(".", "bin", "lily"),
		filepath.Join(".", "lily"),
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "lily"),
			filepath.Join(home, "go", "bin", "lily"),
		)
	}

	// Check absolute path first
	if abs, err := filepath.Abs("./bin/lily"); err == nil {
		if fileExists(abs) {
			return abs
		}
	}

	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			abs = c
		}
		if fileExists(abs) {
			return abs
		}
	}

	// Fallback: assume it's on PATH
	return "lily"
}

// TargetNames returns a comma-separated list of known target names.
func TargetNames() string {
	names := make([]string, 0, 10)
	for _, t := range KnownTargets() {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

// LookupTarget finds a target by name.
func LookupTarget(name string) *Target {
	for _, t := range KnownTargets() {
		if t.Name == name {
			return &t
		}
	}
	return nil
}

// ConfigFilePath returns the path where the lily.yaml config should live.
func ConfigFilePath() string {
	if configDir := os.Getenv("XDG_CONFIG_HOME"); configDir != "" {
		return filepath.Join(configDir, "lily", "lily.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "lily", "lily.yaml")
	}
	return filepath.Join(home, ".config", "lily", "lily.yaml")
}

// AllowlistConfigPath returns the config file path (alias for ConfigFilePath).
// Deprecated: use ConfigFilePath instead.
func AllowlistConfigPath() string {
	return ConfigFilePath()
}

// DeployDefaultConfig creates the default lily.yaml config file
// if one doesn't already exist.
func DeployDefaultConfig() error {
	path := ConfigFilePath()
	if fileExists(path) {
		return nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, []byte(defaultConfig), 0644)
}

// DeployDefaultAllowlist is an alias for DeployDefaultConfig.
// Deprecated: use DeployDefaultConfig instead.
func DeployDefaultAllowlist() error {
	return DeployDefaultConfig()
}

const defaultConfig = `# Lily MCP Configuration
#
# Location: ~/.config/lily/lily.yaml (run "lily config-path" to find it)
#
# This file customizes which commands are available to the MCP server.
# It can only ADD commands to the base allowlist — the hardcoded safety
# restrictions (blocking rm, sudo, bash, python, etc.) cannot be overridden.
#
# After editing, validate with: lily validate-config

# ── Execution Limits ──────────────────────────────────────────────

# Minimum interval between commands. Prevents agents from flooding hosts.
# Default: "1s" (1 command per second). Set to "500ms" for faster, "5s" for stricter.
rate_limit: "1s"

# Maximum output (stdout + stderr) captured per command, in bytes.
# Default: 1048576 (1 MB). Minimum: 1024 (1 KB).
max_output_bytes: 1048576

# ── Command Allowlist ────────────────────────────────────────────

# Extra commands to allow beyond the built-in allowlist.
# These are still subject to metacharacter checks (no $(), backticks, >, etc.)
# and any subcommand restrictions defined below.
extra_commands:
  # - docker
  # - kubectl

# Subcommand restrictions for extra commands.
# Only the listed subcommands/flags will be allowed.
extra_subcommand_restrictions:
  # docker:
  #   - ps
  #   - logs
  #   - inspect
  #   - stats
  # kubectl:
  #   - get
  #   - describe
  #   - logs

# Extra blocked flags for specific commands.
extra_blocked_flags:
  # docker:
  #   - exec
  #   - run
`
