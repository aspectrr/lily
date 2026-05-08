package sshshell

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aspectrr/lily/internal/allowlist"
	"github.com/aspectrr/lily/internal/readonly"
	"github.com/aspectrr/lily/internal/sshconfig"
	"github.com/aspectrr/lily/internal/sshexec"
)

// Shell provides a restricted interactive SSH session where every command
// is validated through lily's read-only allowlist before execution.
// It reads commands from stdin line by line, validates them, and executes
// the allowed ones on the remote host.
type Shell struct {
	hosts    []sshconfig.Host
	exec     *sshexec.Executor
	validate *readonly.Validator
	timeout  TimeoutFunc
}

// TimeoutFunc returns the per-command timeout. Extracted as a function
// so the caller can control it (e.g., from CLI flags).
type TimeoutFunc func() int

// NewShell creates a restricted SSH shell.
func NewShell(
	hosts []sshconfig.Host,
	exec *sshexec.Executor,
	validator *readonly.Validator,
) *Shell {
	return &Shell{
		hosts:    hosts,
		exec:     exec,
		validate: validator,
	}
}

// Run starts the restricted shell loop. It reads commands from stdin,
// validates each one, and executes allowed commands on the remote host.
// It exits when stdin is closed (EOF) or the user types "exit" / "quit".
func (s *Shell) Run(ctx context.Context, hostName string) error {
	host := sshconfig.LookupHost(s.hosts, hostName)
	if host == nil {
		return fmt.Errorf("host %q not found in SSH config", hostName)
	}

	displayHost := host.HostName
	if displayHost == "" {
		displayHost = host.Host
	}

	fmt.Fprintf(os.Stderr, "lily ssh: connected to %s (%s)\n", hostName, displayHost)
	fmt.Fprintf(os.Stderr, "  Every command is validated through lily's read-only allowlist.\n")
	fmt.Fprintf(os.Stderr, "  Type 'exit' or Ctrl+D to disconnect.\n\n")

	scanner := bufio.NewScanner(os.Stdin)
	prompt := fmt.Sprintf("lily/%s> ", hostName)

	for {
		// Print prompt to stderr so it doesn't pollute stdout (command output)
		fmt.Fprint(os.Stderr, prompt)

		if !scanner.Scan() {
			break // EOF
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Built-in commands
		switch line {
		case "exit", "quit":
			fmt.Fprintln(os.Stderr, "disconnected.")
			return nil
		case "help":
			printHelp()
			continue
		}

		// Validate the command
		if err := s.validate.ValidateCommand(line); err != nil {
			fmt.Fprintf(os.Stderr, "blocked: %s\n", err)
			continue
		}

		// Sanitize the command (safe shell quoting)
		safeCommand, err := s.validate.SanitizeCommand(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sanitization failed: %s\n", err)
			continue
		}

		// Execute on the remote host
		result, err := s.exec.Run(ctx, hostName, safeCommand)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s\n", err)
			continue
		}

		if result.Stdout != "" {
			fmt.Print(result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprintf(os.Stderr, "%s", result.Stderr)
		}
		if result.ExitCode != 0 {
			fmt.Fprintf(os.Stderr, "[exit code %d]\n", result.ExitCode)
		}
		if result.Truncated {
			fmt.Fprintf(os.Stderr, "[output truncated]\n")
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading input: %w", err)
	}

	return nil
}

func printHelp() {
	fmt.Fprintln(os.Stderr, "lily ssh — restricted remote shell")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  Every command is validated against lily's read-only allowlist.")
	fmt.Fprintln(os.Stderr, "  Allowed: file inspection (cat, ls, find), system info (ps, systemctl status),")
	fmt.Fprintln(os.Stderr, "           network diagnostics (ss, dig, curl GET), and text processing (grep, awk).")
	fmt.Fprintln(os.Stderr, "  Blocked: destructive commands (rm, mv, chmod), shells (bash, sh, python),")
	fmt.Fprintln(os.Stderr, "           privilege escalation (sudo, su), and file transfer (scp, rsync).")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  exit, quit  — disconnect")
	fmt.Fprintln(os.Stderr, "  help        — show this message")
	fmt.Fprintln(os.Stderr, "")
}

// FilterStdin reads commands from stdin, validates them through lily's
// allowlist, and outputs the validated commands one per line to stdout.
// This is used by the pipe-through mode: `lily ssh web1 < commands.txt`
//
// Deprecated: Use Run instead for the full interactive experience.
func FilterStdin(hosts []sshconfig.Host, cfg *allowlist.Config, command string) string {
	validator := readonly.NewValidator(cfg.ExtraCommands, cfg.SubcommandRestrictions(), cfg.BlockedFlags())
	_ = hosts

	if err := validator.ValidateCommand(command); err != nil {
		return ""
	}

	safe, err := validator.SanitizeCommand(command)
	if err != nil {
		return ""
	}
	return safe
}
