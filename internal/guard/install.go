package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GuardTarget describes an agent that lily guard can be installed into.
type GuardTarget struct {
	Name         string // e.g., "claude-code", "cursor", "pi"
	ConfigPath   string // Absolute path to the agent's config file
	ConfigFormat string // "claude-settings", "cursor-hooks", "pi-extension"
}

// GuardTargets returns all known guard targets on this system.
func GuardTargets() []GuardTarget {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}

	return []GuardTarget{
		{
			Name:         "claude-code",
			ConfigPath:   filepath.Join(home, ".claude", "settings.json"),
			ConfigFormat: "claude-settings",
		},
		{
			Name:         "codex",
			ConfigPath:   filepath.Join(home, ".codex", "hooks.json"),
			ConfigFormat: "codex-hooks",
		},
		{
			Name:         "cursor",
			ConfigPath:   filepath.Join(home, ".cursor", "hooks.json"),
			ConfigFormat: "cursor-hooks",
		},
		{
			Name:         "pi",
			ConfigPath:   filepath.Join(home, ".pi", "agent", "extensions", "lily-guard.ts"),
			ConfigFormat: "pi-extension",
		},
	}
}

// LookupGuardTarget finds a guard target by name.
func LookupGuardTarget(name string) *GuardTarget {
	for _, t := range GuardTargets() {
		if t.Name == name {
			return &t
		}
	}
	return nil
}

// GuardTargetNames returns a comma-separated list of target names.
func GuardTargetNames() string {
	names := make([]string, 0, 10)
	for _, t := range GuardTargets() {
		names = append(names, t.Name)
	}
	return fmt.Sprintf("%s", names)
}

// InstallGuard installs the lily guard hook into the given target's config.
func InstallGuard(target GuardTarget, binaryPath string) error {
	if binaryPath == "" {
		binaryPath = "lily"
	}

	switch target.ConfigFormat {
	case "claude-settings":
		return installClaudeCodeGuard(target.ConfigPath, binaryPath)
	case "codex-hooks":
		return installCodexGuard(target.ConfigPath, binaryPath)
	case "cursor-hooks":
		return installCursorGuard(target.ConfigPath, binaryPath)
	case "pi-extension":
		return installPiGuard(target.ConfigPath, binaryPath)
	default:
		return fmt.Errorf("unknown config format: %s", target.ConfigFormat)
	}
}

// UninstallGuard removes the lily guard hook from the given target's config.
func UninstallGuard(target GuardTarget) error {
	switch target.ConfigFormat {
	case "claude-settings":
		return uninstallClaudeCodeGuard(target.ConfigPath)
	case "codex-hooks":
		return uninstallCodexGuard(target.ConfigPath)
	case "cursor-hooks":
		return uninstallCursorGuard(target.ConfigPath)
	case "pi-extension":
		return uninstallPiGuard(target.ConfigPath)
	default:
		return fmt.Errorf("unknown config format: %s", target.ConfigFormat)
	}
}

// ── Claude Code ──────────────────────────────────────────────────────

func installClaudeCodeGuard(configPath, binaryPath string) error {
	cfg := make(map[string]any)

	data, err := os.ReadFile(configPath)
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			cfg = make(map[string]any)
		}
	}

	// Ensure hooks.PreToolUse exists
	hooks, ok := cfg["hooks"].(map[string]any)
	if !ok {
		hooks = make(map[string]any)
		cfg["hooks"] = hooks
	}

	preToolUse, ok := hooks["PreToolUse"].([]any)
	if !ok {
		preToolUse = []any{}
	}

	// Check if lily guard is already installed
	for _, entry := range preToolUse {
		if matcher, ok := entry.(map[string]any); ok {
			if matcherList, ok := matcher["matcher"].(string); ok && matcherList == "Bash" {
				if hookList, ok := matcher["hooks"].([]any); ok {
					for _, h := range hookList {
						if hm, ok := h.(map[string]any); ok {
							if cmd, ok := hm["command"].(string); ok && (cmd == "lily guard-hook claude-code" ||
								containsLilyGuard(cmd)) {
								fmt.Printf("  lily guard already installed in Claude Code\n")
								return nil
							}
						}
					}
				}
			}
		}
	}

	// Add our hook entry
	newEntry := map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": fmt.Sprintf("%s guard-hook claude-code", binaryPath),
			},
		},
	}

	preToolUse = append(preToolUse, newEntry)
	hooks["PreToolUse"] = preToolUse

	return writeJSONFile(configPath, cfg)
}

func uninstallClaudeCodeGuard(configPath string) error {
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

	hooks, ok := cfg["hooks"].(map[string]any)
	if !ok {
		return nil
	}

	preToolUse, ok := hooks["PreToolUse"].([]any)
	if !ok {
		return nil
	}

	filtered := make([]any, 0, len(preToolUse))
	for _, entry := range preToolUse {
		if matcher, ok := entry.(map[string]any); ok {
			if hookList, ok := matcher["hooks"].([]any); ok {
				// Filter out lily guard hooks
				newHooks := make([]any, 0)
				for _, h := range hookList {
					if hm, ok := h.(map[string]any); ok {
						if cmd, ok := hm["command"].(string); ok && containsLilyGuard(cmd) {
							continue
						}
						newHooks = append(newHooks, hm)
					} else {
						newHooks = append(newHooks, h)
					}
				}
				if len(newHooks) == 0 {
					continue // Skip this matcher entirely
				}
				matcher["hooks"] = newHooks
			}
		}
		filtered = append(filtered, entry)
	}

	hooks["PreToolUse"] = filtered

	// Clean up empty sections
	if len(filtered) == 0 {
		delete(hooks, "PreToolUse")
	}
	if len(hooks) == 0 {
		delete(cfg, "hooks")
	}

	return writeJSONFile(configPath, cfg)
}

// ── Codex ────────────────────────────────────────────────────────────

func installCodexGuard(configPath, binaryPath string) error {
	cfg := make(map[string]any)

	data, err := os.ReadFile(configPath)
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			cfg = make(map[string]any)
		}
	}

	hooks, ok := cfg["hooks"].(map[string]any)
	if !ok {
		hooks = make(map[string]any)
		cfg["hooks"] = hooks
	}

	preToolUse, ok := hooks["PreToolUse"].([]any)
	if !ok {
		preToolUse = []any{}
	}

	// Check if already installed
	for _, entry := range preToolUse {
		if matcher, ok := entry.(map[string]any); ok {
			if hookList, ok := matcher["hooks"].([]any); ok {
				for _, h := range hookList {
					if hm, ok := h.(map[string]any); ok {
						if cmd, ok := hm["command"].(string); ok && containsLilyGuard(cmd) {
							fmt.Printf("  lily guard already installed in Codex\n")
							return nil
						}
					}
				}
			}
		}
	}

	newEntry := map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{
				"type":          "command",
				"command":       fmt.Sprintf("%s guard-hook codex", binaryPath),
				"statusMessage": "Lily guard: checking for SSH",
			},
		},
	}

	preToolUse = append(preToolUse, newEntry)
	hooks["PreToolUse"] = preToolUse

	return writeJSONFile(configPath, cfg)
}

func uninstallCodexGuard(configPath string) error {
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

	hooks, ok := cfg["hooks"].(map[string]any)
	if !ok {
		return nil
	}

	preToolUse, ok := hooks["PreToolUse"].([]any)
	if !ok {
		return nil
	}

	filtered := make([]any, 0, len(preToolUse))
	for _, entry := range preToolUse {
		if matcher, ok := entry.(map[string]any); ok {
			if hookList, ok := matcher["hooks"].([]any); ok {
				newHooks := make([]any, 0)
				for _, h := range hookList {
					if hm, ok := h.(map[string]any); ok {
						if cmd, ok := hm["command"].(string); ok && containsLilyGuard(cmd) {
							continue
						}
						newHooks = append(newHooks, hm)
					} else {
						newHooks = append(newHooks, h)
					}
				}
				if len(newHooks) == 0 {
					continue
				}
				matcher["hooks"] = newHooks
			}
		}
		filtered = append(filtered, entry)
	}

	hooks["PreToolUse"] = filtered
	if len(filtered) == 0 {
		delete(hooks, "PreToolUse")
	}
	if len(hooks) == 0 {
		delete(cfg, "hooks")
	}

	return writeJSONFile(configPath, cfg)
}

// ── Cursor ───────────────────────────────────────────────────────────

func installCursorGuard(configPath, binaryPath string) error {
	cfg := make(map[string]any)

	data, err := os.ReadFile(configPath)
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			cfg = make(map[string]any)
		}
	}

	// Cursor uses a hooks array at the top level
	hooksArr, ok := cfg["hooks"].([]any)
	if !ok {
		hooksArr = []any{}
	}

	// Check if already installed
	for _, entry := range hooksArr {
		if hm, ok := entry.(map[string]any); ok {
			if cmd, ok := hm["command"].(string); ok && containsLilyGuard(cmd) {
				fmt.Printf("  lily guard already installed in Cursor\n")
				return nil
			}
		}
	}

	newEntry := map[string]any{
		"event":   "preToolUse",
		"type":    "command",
		"command": fmt.Sprintf("%s guard-hook cursor", binaryPath),
	}

	hooksArr = append(hooksArr, newEntry)
	cfg["hooks"] = hooksArr

	return writeJSONFile(configPath, cfg)
}

func uninstallCursorGuard(configPath string) error {
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

	hooksArr, ok := cfg["hooks"].([]any)
	if !ok {
		return nil
	}

	filtered := make([]any, 0, len(hooksArr))
	for _, entry := range hooksArr {
		if hm, ok := entry.(map[string]any); ok {
			if cmd, ok := hm["command"].(string); ok && containsLilyGuard(cmd) {
				continue
			}
		}
		filtered = append(filtered, entry)
	}

	if len(filtered) == 0 {
		delete(cfg, "hooks")
	} else {
		cfg["hooks"] = filtered
	}

	return writeJSONFile(configPath, cfg)
}

// ── Pi ───────────────────────────────────────────────────────────────

const piExtensionTemplate = `// lily-guard — Automatically installed by "lily guard install pi"
// Intercepts bash tool calls containing SSH and rewrites them to use lily run.
import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { isToolCallEventType } from "@mariozechner/pi-coding-agent";

export default function (pi: ExtensionAPI) {
  pi.on("tool_call", async (event, ctx) => {
    if (!isToolCallEventType("bash", event)) return;
    const cmd = event.input.command ?? "";
    if (!cmd) return;

    // Quick check: only run if the command mentions ssh/scp/rsync
    if (!/\b(ssh|scp|rsync)\b/.test(cmd)) return;

    const result = await pi.exec("{{BINARY}}", ["rewrite", cmd], { timeout: 10 });
    if (result.code === null || result.code !== 0) return;

    const rewritten = result.stdout.trim();
    if (!rewritten || rewritten === cmd) return;

    // Mutate the command in-place before execution
    event.input.command = rewritten;
  });
}
`

func installPiGuard(configPath, binaryPath string) error {
	// Check if already installed
	if _, err := os.Stat(configPath); err == nil {
		fmt.Printf("  lily guard already installed in pi (%s)\n", configPath)
		return nil
	}

	// Ensure parent directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	content := piExtensionTemplate
	content = replaceAll(content, "{{BINARY}}", binaryPath)

	return os.WriteFile(configPath, []byte(content), 0644)
}

func uninstallPiGuard(configPath string) error {
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(configPath)
}

// ── Helpers ──────────────────────────────────────────────────────────

func containsLilyGuard(cmd string) bool {
	return len(cmd) > 0 && strings.Contains(cmd, "lily guard-hook")
}

func writeJSONFile(path string, data any) error {
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

func replaceAll(s, old, new string) string {
	result := ""
	for {
		idx := indexOf(s, old)
		if idx < 0 {
			return result + s
		}
		result += s[:idx] + new
		s = s[idx+len(old):]
	}
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
