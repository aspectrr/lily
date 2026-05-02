package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aspectrr/lily/internal/allowlist"
	"github.com/aspectrr/lily/internal/readonly"
	"github.com/aspectrr/lily/internal/sshconfig"
	"github.com/aspectrr/lily/internal/sshexec"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const serverName = "lily"
const serverVersion = "0.2.0"

// rateLimiter enforces a minimum interval between command executions.
type rateLimiter struct {
	mu          sync.Mutex
	lastRun     time.Time
	minInterval time.Duration
}

func newRateLimiter(interval time.Duration) *rateLimiter {
	return &rateLimiter{minInterval: interval}
}

func (r *rateLimiter) wait() {
	r.mu.Lock()
	defer r.mu.Unlock()

	elapsed := time.Since(r.lastRun)
	if elapsed < r.minInterval {
		time.Sleep(r.minInterval - elapsed)
	}
	r.lastRun = time.Now()
}

// NewServer creates a configured MCP server with all tools registered.
func NewServer(hosts []sshconfig.Host, timeout time.Duration, cfg *allowlist.Config) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(serverName, serverVersion)

	maxOutput := cfg.GetMaxOutputBytes()
	exec := sshexec.NewExecutor(hosts, timeout, maxOutput)
	validator := readonly.NewValidator(
		cfg.ExtraCommands,
		cfg.SubcommandRestrictions(),
		cfg.BlockedFlags(),
	)

	limiter := newRateLimiter(cfg.GetRateLimit())

	s.AddTool(runCommandTool(hosts, exec, validator, limiter))
	s.AddTool(listHostsTool(hosts))
	s.AddTool(validateCommandTool(validator))
	s.AddTool(checkHostTool(hosts, exec, limiter))
	s.AddTool(listAllowedCommandsTool(validator))

	return s
}

func runCommandTool(hosts []sshconfig.Host, exec *sshexec.Executor, v *readonly.Validator, limiter *rateLimiter) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	tool := mcp.NewTool("run_command",
		mcp.WithDescription("Execute a read-only command on a remote host via SSH. The command is validated against a strict allowlist before execution. Only read-only operations are permitted: file inspection (cat, ls, find, head, tail), process viewing (ps, systemctl status, journalctl), network diagnostics (ss, dig, curl GET), system info (uname, df, free), and text processing (grep, awk, sed). No command substitution, output redirection, or destructive operations. Use list_hosts to discover available hosts."),
		mcp.WithString("host",
			mcp.Required(),
			mcp.Description("Host alias from SSH config"),
			mcp.Enum(hostNames(hosts)...),
		),
		mcp.WithString("command",
			mcp.Required(),
			mcp.Description("Shell command to execute (validated against read-only allowlist)"),
		),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		hostName, _ := args["host"].(string)
		command, _ := args["command"].(string)

		if hostName == "" {
			return errorResult("host is required"), nil
		}
		if command == "" {
			return errorResult("command is required"), nil
		}

		if sshconfig.LookupHost(hosts, hostName) == nil {
			return errorResult(fmt.Sprintf("host %q not found in SSH config", hostName)), nil
		}

		if err := v.ValidateCommand(command); err != nil {
			return errorResult(fmt.Sprintf("command blocked: %s", err.Error())), nil
		}

		// Sanitize the command: reconstruct with safe quoting so the remote
		// shell cannot reinterpret any special characters.
		safeCommand, err := v.SanitizeCommand(command)
		if err != nil {
			return errorResult(fmt.Sprintf("command sanitization failed: %s", err.Error())), nil
		}

		// Enforce rate limit
		limiter.wait()

		result, err := exec.Run(ctx, hostName, safeCommand)
		if err != nil {
			return errorResult(fmt.Sprintf("execution failed: %s", err.Error())), nil
		}

		var sb strings.Builder
		if result.Stdout != "" {
			sb.WriteString(result.Stdout)
		}
		if result.Stderr != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString("[stderr] ")
			sb.WriteString(result.Stderr)
		}
		if result.ExitCode != 0 {
			sb.WriteString(fmt.Sprintf("\n[exit code %d]", result.ExitCode))
		}

		output := sb.String()
		if output == "" {
			output = "(no output)"
		}

		return textResult(output), nil
	}

	return tool, handler
}

func listHostsTool(hosts []sshconfig.Host) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	tool := mcp.NewTool("list_hosts",
		mcp.WithDescription("List all hosts available from the user's SSH config. Use this to discover what hosts can be accessed before running commands."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if len(hosts) == 0 {
			return textResult("No hosts found in SSH config."), nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d host(s) in SSH config:\n\n", len(hosts)))

		for _, h := range hosts {
			displayHost := h.HostName
			if displayHost == "" {
				displayHost = h.Host
			}
			sb.WriteString(fmt.Sprintf("  %-25s %s", h.Host, displayHost))
			if h.User != "" {
				sb.WriteString(fmt.Sprintf(" (user: %s)", h.User))
			}
			if h.Port != "" && h.Port != "22" {
				sb.WriteString(fmt.Sprintf(" :%s", h.Port))
			}
			sb.WriteString("\n")
		}

		return textResult(sb.String()), nil
	}

	return tool, handler
}

func validateCommandTool(v *readonly.Validator) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	tool := mcp.NewTool("validate_command",
		mcp.WithDescription("Check whether a command would be allowed by the read-only allowlist without executing it. Useful to test commands before running them."),
		mcp.WithString("command",
			mcp.Required(),
			mcp.Description("Shell command to validate"),
		),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		command, _ := args["command"].(string)
		if command == "" {
			return errorResult("command is required"), nil
		}

		if err := v.ValidateCommand(command); err != nil {
			return errorResult(fmt.Sprintf("BLOCKED: %s", err.Error())), nil
		}

		return textResult(fmt.Sprintf("ALLOWED: %q is safe to execute", command)), nil
	}

	return tool, handler
}

func checkHostTool(hosts []sshconfig.Host, exec *sshexec.Executor, limiter *rateLimiter) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	tool := mcp.NewTool("check_host",
		mcp.WithDescription("Test SSH connectivity to a host by running a simple echo command. Use this to verify a host is reachable before running diagnostic commands."),
		mcp.WithString("host",
			mcp.Required(),
			mcp.Description("Host alias from SSH config"),
			mcp.Enum(hostNames(hosts)...),
		),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		hostName, _ := args["host"].(string)
		if hostName == "" {
			return errorResult("host is required"), nil
		}

		if sshconfig.LookupHost(hosts, hostName) == nil {
			return errorResult(fmt.Sprintf("host %q not found in SSH config", hostName)), nil
		}

		// Enforce rate limit
		limiter.wait()

		result, err := exec.Run(ctx, hostName, "echo ok")
		if err != nil {
			return errorResult(fmt.Sprintf("connection failed: %s", err.Error())), nil
		}

		if result.ExitCode != 0 {
			return errorResult(fmt.Sprintf("host returned exit code %d: %s", result.ExitCode, result.Stderr)), nil
		}

		return textResult(fmt.Sprintf("Host %q is reachable.", hostName)), nil
	}

	return tool, handler
}

func listAllowedCommandsTool(v *readonly.Validator) (mcp.Tool, mcpserver.ToolHandlerFunc) {
	tool := mcp.NewTool("list_allowed_commands",
		mcp.WithDescription("List all commands currently allowed by the read-only allowlist, including any user-configured additions."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	)

	handler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cmds := v.AllowedCommandsList()
		return textResult(fmt.Sprintf("Allowed commands (%d): %s", len(cmds), strings.Join(cmds, ", "))), nil
	}

	return tool, handler
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: text},
		},
	}
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: msg},
		},
		IsError: true,
	}
}

func hostNames(hosts []sshconfig.Host) []string {
	names := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if strings.Contains(h.Host, "*") || strings.Contains(h.Host, "?") {
			continue
		}
		names = append(names, h.Host)
	}
	return names
}
