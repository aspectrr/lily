package guard

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// HookInput is the JSON input received from Claude Code's PreToolUse event.
type HookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	// Extra fields we don't use but must tolerate
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	PermissionMode string `json:"permission_mode"`
	HookEventName  string `json:"hook_event_name"`
}

// ClaudeCodeOutput is the JSON output format for Claude Code hooks.
type ClaudeCodeOutput struct {
	HookSpecificOutput *ClaudeCodeHookOutput `json:"hookSpecificOutput,omitempty"`
}

type ClaudeCodeHookOutput struct {
	HookEventName            string        `json:"hookEventName"`
	PermissionDecision       string        `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string        `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             *UpdatedInput `json:"updatedInput,omitempty"`
}

type UpdatedInput struct {
	Command string `json:"command"`
}

// CursorOutput is the JSON output format for Cursor hooks.
type CursorOutput struct {
	Permission   string        `json:"permission,omitempty"`
	UpdatedInput *UpdatedInput `json:"updated_input,omitempty"`
}

// RunHook reads agent-specific JSON from stdin, runs the rewrite logic,
// and writes the agent-specific JSON response to stdout.
// Exit codes:
//
//	0 — rewrite applied or passthrough (check stdout)
//	1 — error (non-blocking, passthrough)
//
// This follows RTK's exit code contract: hooks must NEVER block command
// execution. All error paths exit cleanly so the original command runs.
func RunHook(agent string) int {
	input, err := readHookInput(os.Stdin)
	if err != nil {
		// Non-blocking: warn to stderr, exit 0 so command runs anyway
		fmt.Fprintf(os.Stderr, "[lily guard] warning: could not parse hook input: %s\n", err)
		return 0
	}

	// Only intercept Bash tool calls
	if input.ToolName != "Bash" && input.ToolName != "bash" {
		return 0
	}

	command := input.ToolInput.Command
	if command == "" {
		return 0
	}

	result := Rewrite(command)

	switch result.Decision {
	case "passthrough":
		// No SSH detected — let the command through unchanged
		return 0

	case "rewrite":
		return emitRewrite(agent, command, result)

	case "block":
		return emitBlock(agent, result)

	default:
		return 0
	}
}

func emitRewrite(agent, original string, result RewriteResult) int {
	switch agent {
	case "claude-code":
		output := ClaudeCodeOutput{
			HookSpecificOutput: &ClaudeCodeHookOutput{
				HookEventName:            "PreToolUse",
				PermissionDecision:       "allow",
				PermissionDecisionReason: result.Reason,
				UpdatedInput: &UpdatedInput{
					Command: result.Rewritten,
				},
			},
		}
		writeJSON(output)
		return 0

	case "codex":
		// Codex doesn't support updatedInput yet, so we deny-with-suggestion.
		// The agent sees the deny reason and will retry with the lily command.
		output := ClaudeCodeOutput{
			HookSpecificOutput: &ClaudeCodeHookOutput{
				HookEventName:            "PreToolUse",
				PermissionDecision:       "deny",
				PermissionDecisionReason: fmt.Sprintf("%s Use instead: %s", result.Reason, result.Rewritten),
			},
		}
		writeJSON(output)
		return 0

	case "cursor":
		output := CursorOutput{
			Permission: "allow",
			UpdatedInput: &UpdatedInput{
				Command: result.Rewritten,
			},
		}
		writeJSON(output)
		return 0

	default:
		// Unknown agent — just print the rewritten command to stdout
		fmt.Print(result.Rewritten)
		return 0
	}
}

func emitBlock(agent string, result RewriteResult) int {
	switch agent {
	case "claude-code", "codex":
		output := ClaudeCodeOutput{
			HookSpecificOutput: &ClaudeCodeHookOutput{
				HookEventName:            "PreToolUse",
				PermissionDecision:       "deny",
				PermissionDecisionReason: result.Reason,
			},
		}
		writeJSON(output)
		return 0

	case "cursor":
		output := map[string]string{
			"permission": "deny",
			"reason":     result.Reason,
		}
		writeJSON(output)
		return 0

	default:
		fmt.Fprintf(os.Stderr, "[lily guard] blocked: %s\n", result.Reason)
		return 0
	}
}

func readHookInput(r io.Reader) (*HookInput, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty input")
	}
	var input HookInput
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return &input, nil
}

func writeJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[lily guard] warning: could not marshal JSON: %s\n", err)
		return
	}
	os.Stdout.Write(data)
	os.Stdout.Write([]byte("\n"))
}
