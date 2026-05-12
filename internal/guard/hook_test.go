package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunHook_ClaudeCodeRewrite(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"ssh web1 systemctl status nginx"}}`
	expectedCommand := "lily run web1 'systemctl status nginx'"

	// Create a temp file to simulate stdin
	tmpFile, err := os.CreateTemp("", "hook-input-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(input)
	tmpFile.Seek(0, 0)

	// Redirect stdin
	oldStdin := os.Stdin
	os.Stdin = tmpFile
	defer func() { os.Stdin = oldStdin }()

	// Capture stdout
	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	exitCode := RunHook("claude-code")
	w.Close()

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	var result ClaudeCodeOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("could not parse output: %s\noutput was: %s", err, output)
	}

	if result.HookSpecificOutput == nil {
		t.Fatal("expected hookSpecificOutput")
	}
	if result.HookSpecificOutput.PermissionDecision != "allow" {
		t.Fatalf("expected allow, got %s", result.HookSpecificOutput.PermissionDecision)
	}
	if result.HookSpecificOutput.UpdatedInput == nil {
		t.Fatal("expected updatedInput")
	}
	if result.HookSpecificOutput.UpdatedInput.Command != expectedCommand {
		t.Fatalf("expected %q, got %q", expectedCommand, result.HookSpecificOutput.UpdatedInput.Command)
	}
}

func TestRunHook_ClaudeCodePassthrough(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"git status"}}`

	tmpFile, _ := os.CreateTemp("", "hook-input-*.json")
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(input)
	tmpFile.Seek(0, 0)

	oldStdin := os.Stdin
	os.Stdin = tmpFile
	defer func() { os.Stdin = oldStdin }()

	// Capture stdout
	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	exitCode := RunHook("claude-code")
	w.Close()

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	if n > 0 {
		t.Fatalf("expected no output for passthrough, got: %s", string(buf[:n]))
	}
}

func TestRunHook_ClaudeCodeBlock(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"scp file.txt web1:/tmp/"}}`

	tmpFile, _ := os.CreateTemp("", "hook-input-*.json")
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(input)
	tmpFile.Seek(0, 0)

	oldStdin := os.Stdin
	os.Stdin = tmpFile
	defer func() { os.Stdin = oldStdin }()

	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	exitCode := RunHook("claude-code")
	w.Close()

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	var result ClaudeCodeOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("could not parse output: %s", err)
	}
	if result.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("expected deny, got %s", result.HookSpecificOutput.PermissionDecision)
	}
}

func TestInstallUninstallClaudeCode(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "settings.json")

	target := GuardTarget{
		Name:         "claude-code",
		ConfigPath:   configPath,
		ConfigFormat: "claude-settings",
	}

	// Install
	if err := InstallGuard(target, "lily"); err != nil {
		t.Fatal(err)
	}

	// Verify the file has our hook
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !containsLilyGuardInJSON(string(data)) {
		t.Fatal("expected lily guard to be in settings file")
	}

	// Uninstall
	if err := UninstallGuard(target); err != nil {
		t.Fatal(err)
	}

	// Verify the hook was removed
	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := make(map[string]any)
	json.Unmarshal(data, &cfg)
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks != nil {
		if preToolUse, ok := hooks["PreToolUse"].([]any); ok && len(preToolUse) > 0 {
			for _, entry := range preToolUse {
				if m, ok := entry.(map[string]any); ok {
					if hl, ok := m["hooks"].([]any); ok {
						for _, h := range hl {
							if hm, ok := h.(map[string]any); ok {
								if cmd, ok := hm["command"].(string); ok && containsLilyGuard(cmd) {
									t.Fatal("lily guard should have been removed")
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestInstallUninstallCodex(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "hooks.json")

	target := GuardTarget{
		Name:         "codex",
		ConfigPath:   configPath,
		ConfigFormat: "codex-hooks",
	}

	// Install
	if err := InstallGuard(target, "lily"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !containsLilyGuardInJSON(string(data)) {
		t.Fatal("expected lily guard to be in hooks.json")
	}

	// Verify the structure is correct for Codex
	cfg := make(map[string]any)
	json.Unmarshal(data, &cfg)
	hooks, _ := cfg["hooks"].(map[string]any)
	preToolUse, _ := hooks["PreToolUse"].([]any)
	if len(preToolUse) != 1 {
		t.Fatalf("expected 1 PreToolUse entry, got %d", len(preToolUse))
	}
	entry := preToolUse[0].(map[string]any)
	if entry["matcher"] != "Bash" {
		t.Fatalf("expected matcher 'Bash', got %v", entry["matcher"])
	}

	// Uninstall
	if err := UninstallGuard(target); err != nil {
		t.Fatal(err)
	}

	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if containsLilyGuardInJSON(string(data)) {
		t.Fatal("lily guard should have been removed")
	}
}

func TestRunHook_CodexRewriteDenyWithSuggestion(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"ssh web1 systemctl status nginx"}}`

	tmpFile, _ := os.CreateTemp("", "hook-input-*.json")
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(input)
	tmpFile.Seek(0, 0)

	oldStdin := os.Stdin
	os.Stdin = tmpFile
	defer func() { os.Stdin = oldStdin }()

	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	exitCode := RunHook("codex")
	w.Close()

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	var result ClaudeCodeOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("could not parse output: %s", err)
	}

	// Codex uses deny-with-suggestion since updatedInput isn't supported
	if result.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("expected deny, got %s", result.HookSpecificOutput.PermissionDecision)
	}
	reason := result.HookSpecificOutput.PermissionDecisionReason
	if !containsKeyword(reason, "Use instead: lily run") {
		t.Fatalf("expected reason to contain lily run suggestion, got: %s", reason)
	}
}

func containsKeyword(s, keyword string) bool {
	return len(s) > 0 && len(keyword) > 0 && stringContains(s, keyword)
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
