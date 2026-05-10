package cloud

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aspectrr/lily/internal/readonly"
)

// Provider represents a cloud provider.
type Provider string

const (
	AWS    Provider = "aws"
	GCloud Provider = "gcloud"
	Azure  Provider = "azure"
)

// Result holds the output of a cloud command execution.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// limitedBuffer caps output at a configurable limit.
type limitedBuffer struct {
	bytes.Buffer
	limit     int64
	truncated bool
}

func (lb *limitedBuffer) Write(p []byte) (n int, err error) {
	if lb.limit > 0 && int64(lb.Buffer.Len())+int64(len(p)) > lb.limit {
		remaining := lb.limit - int64(lb.Buffer.Len())
		if remaining > 0 {
			lb.Buffer.Write(p[:remaining])
		}
		lb.truncated = true
		return len(p), nil
	}
	return lb.Buffer.Write(p)
}

func (lb *limitedBuffer) ReadFrom(r io.Reader) (n int64, err error) {
	buf := make([]byte, 32*1024)
	for {
		nr, readErr := r.Read(buf)
		if nr > 0 {
			nw, writeErr := lb.Write(buf[:nr])
			if writeErr != nil {
				return n, writeErr
			}
			n += int64(nw)
		}
		if readErr != nil {
			if readErr == io.EOF {
				readErr = nil
			}
			return n, readErr
		}
		if lb.truncated {
			_, _ = io.Copy(io.Discard, r)
			return n, nil
		}
	}
}

// ParseCommand extracts the remote command from args using multiple strategies:
//  1. --command flag (lily-specific, takes precedence)
//  2. -- separator (Azure-style: everything after -- is the remote command)
//  3. --parameters JSON with "command" or "commands" key (AWS SSM)
//
// Returns the remaining provider args and the extracted command.
func ParseCommand(args []string) ([]string, string) {
	// Strategy 1: --command flag
	for i, arg := range args {
		if arg == "--command" && i+1 < len(args) {
			remaining := make([]string, 0, len(args)-2)
			remaining = append(remaining, args[:i]...)
			remaining = append(remaining, args[i+2:]...)
			return remaining, args[i+1]
		}
		if strings.HasPrefix(arg, "--command=") {
			remaining := make([]string, 0, len(args)-1)
			remaining = append(remaining, args[:i]...)
			remaining = append(remaining, args[i+1:]...)
			return remaining, strings.TrimPrefix(arg, "--command=")
		}
	}

	// Strategy 2: -- separator (Azure-style)
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) && args[i+1] != "" {
			remaining := make([]string, 0, i)
			remaining = append(remaining, args[:i]...)
			return remaining, strings.Join(args[i+1:], " ")
		}
	}

	// Strategy 3: --parameters JSON (AWS SSM)
	for i, arg := range args {
		if arg == "--parameters" && i+1 < len(args) {
			var params map[string]interface{}
			if err := json.Unmarshal([]byte(args[i+1]), &params); err == nil {
				for _, key := range []string{"commands", "command"} {
					if val, ok := params[key]; ok {
						switch v := val.(type) {
						case []interface{}:
							if len(v) > 0 {
								if cmd, ok := v[0].(string); ok {
									remaining := make([]string, 0, len(args)-2)
									remaining = append(remaining, args[:i]...)
									remaining = append(remaining, args[i+2:]...)
									return remaining, cmd
								}
							}
						case string:
							remaining := make([]string, 0, len(args)-2)
							remaining = append(remaining, args[:i]...)
							remaining = append(remaining, args[i+2:]...)
							return remaining, v
						}
					}
				}
			}
		}
	}

	return args, ""
}

// Run validates and executes a single command on a remote cloud instance.
func Run(ctx context.Context, provider Provider, args []string, command string, validator *readonly.Validator, timeout time.Duration, maxOutput int64) (*Result, error) {
	if command == "" {
		return nil, fmt.Errorf("no command specified (use --command)")
	}

	// Validate the command through lily's read-only allowlist
	if err := validator.ValidateCommand(command); err != nil {
		return nil, fmt.Errorf("command blocked: %w", err)
	}

	// Sanitize the command (safe shell quoting)
	safeCommand, err := validator.SanitizeCommand(command)
	if err != nil {
		return nil, fmt.Errorf("command sanitization failed: %w", err)
	}

	// Execute via provider-specific mechanism
	switch provider {
	case AWS:
		return runAWS(ctx, args, safeCommand, timeout, maxOutput)
	case GCloud:
		return runGCloud(ctx, args, safeCommand, timeout, maxOutput)
	case Azure:
		return runAzure(ctx, args, safeCommand, timeout, maxOutput)
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

// Shell runs an interactive restricted shell on a cloud instance.
// Each command typed is validated through lily's read-only allowlist
// before being executed via the cloud provider's CLI.
func Shell(ctx context.Context, provider Provider, args []string, validator *readonly.Validator, timeout time.Duration, maxOutput int64) error {
	identifier := extractIdentifier(provider, args)

	fmt.Fprintf(os.Stderr, "lily %s: connected to %s via %s\n", provider, identifier, providerBinary(provider))
	fmt.Fprintf(os.Stderr, "  Every command is validated through lily's read-only allowlist.\n")
	fmt.Fprintf(os.Stderr, "  Type 'exit' or Ctrl+D to disconnect.\n\n")

	// Check that the cloud CLI is available
	if _, err := exec.LookPath(providerBinary(provider)); err != nil {
		return fmt.Errorf("%s CLI not found: install the %s CLI to use lily %s",
			providerBinary(provider), provider, provider)
	}

	scanner := bufio.NewScanner(os.Stdin)
	prompt := fmt.Sprintf("lily/%s/%s> ", provider, identifier)

	for {
		fmt.Fprint(os.Stderr, prompt)

		if !scanner.Scan() {
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		switch line {
		case "exit", "quit":
			fmt.Fprintln(os.Stderr, "disconnected.")
			return nil
		case "help":
			printShellHelp(provider)
			continue
		}

		// Validate the command
		if err := validator.ValidateCommand(line); err != nil {
			fmt.Fprintf(os.Stderr, "blocked: %s\n", err)
			continue
		}

		// Sanitize the command
		safeCommand, err := validator.SanitizeCommand(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sanitization failed: %s\n", err)
			continue
		}

		// Execute on the remote cloud instance
		result, err := Run(ctx, provider, args, safeCommand, validator, timeout, maxOutput)
		// Run validates again (redundant but safe), so we ignore the double-validation
		// by using the internal run functions directly
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
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading input: %w", err)
	}

	return nil
}

// ── AWS ──────────────────────────────────────────────────────────────

// runAWS executes a command on an AWS instance via SSM send-command.
// Uses AWS-RunShellScript document with command polling for synchronous results.
func runAWS(ctx context.Context, args []string, command string, timeout time.Duration, maxOutput int64) (*Result, error) {
	if len(args) < 2 || args[0] != "ssm" {
		return nil, fmt.Errorf("only 'aws ssm' commands are supported for remote command execution")
	}

	// EC2 Instance Connect SSH — not supported for non-interactive
	if len(args) >= 3 && args[0] == "ec2-instance-connect" && args[1] == "ssh" {
		return nil, fmt.Errorf("aws ec2-instance-connect ssh does not support non-interactive command execution; " +
			"use 'lily aws ssm start-session --target <instance-id> --command \"<command>\"' instead")
	}

	// Extract target instance ID
	target := extractFlagValue(args, "--target")
	if target == "" {
		target = extractFlagValue(args, "--instance-id")
	}
	if target == "" {
		target = extractFlagValue(args, "--instance-ids")
	}
	if target == "" {
		return nil, fmt.Errorf("--target or --instance-id is required for AWS SSM command execution")
	}

	if timeout == 0 {
		timeout = 30 * time.Second
	}

	// Step 1: Send command via SSM
	timeoutSec := int(timeout.Seconds())
	if timeoutSec < 30 {
		timeoutSec = 30
	}

	sendArgs := []string{
		"ssm", "send-command",
		"--instance-ids", target,
		"--document-name", "AWS-RunShellScript",
		"--parameters", fmt.Sprintf(`{"commands":["%s"]}`, escapeJSONString(command)),
		"--timeout-seconds", fmt.Sprintf("%d", timeoutSec),
		"--output", "json",
	}

	sendResult, err := executeCommand(ctx, "aws", sendArgs, timeout, maxOutput)
	if err != nil {
		return nil, fmt.Errorf("aws ssm send-command failed: %w", err)
	}

	// Check for common AWS error responses before parsing
	stdout := strings.TrimSpace(sendResult.Stdout)
	if stdout == "" {
		// Empty response — typically means the endpoint doesn't support SSM SendCommand
		if sendResult.Stderr != "" {
			return nil, fmt.Errorf("aws ssm send-command returned no output; the SSM service may not be available at this endpoint. stderr: %s", strings.TrimSpace(sendResult.Stderr))
		}
		return nil, fmt.Errorf("aws ssm send-command returned no output; the SSM service may not be available at this endpoint")
	}

	// Detect structured error responses from AWS
	var awsErr struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	}
	if json.Unmarshal([]byte(stdout), &awsErr) == nil && awsErr.Code != "" {
		return nil, fmt.Errorf("aws ssm send-command error: %s: %s", awsErr.Code, awsErr.Message)
	}

	// Parse command ID from response
	var sendResp struct {
		Command struct {
			CommandID string `json:"CommandId"`
		} `json:"Command"`
	}
	if err := json.Unmarshal([]byte(stdout), &sendResp); err != nil {
		return nil, fmt.Errorf("failed to parse send-command response: %w\noutput: %s", err, stdout)
	}

	if sendResp.Command.CommandID == "" {
		return nil, fmt.Errorf("no CommandId in send-command response: %s", sendResult.Stdout)
	}

	// Step 2: Poll for command result
	return pollSSMResult(ctx, target, sendResp.Command.CommandID, timeout, maxOutput)
}

// pollSSMResult polls get-command-invocation until the command completes.
func pollSSMResult(ctx context.Context, instanceID, commandID string, timeout time.Duration, maxOutput int64) (*Result, error) {
	deadline := time.Now().Add(timeout)

	// Initial delay for command to start executing
	time.Sleep(1 * time.Second)

	for time.Now().Before(deadline) {
		getArgs := []string{
			"ssm", "get-command-invocation",
			"--command-id", commandID,
			"--instance-id", instanceID,
			"--output", "json",
		}

		result, err := executeCommand(ctx, "aws", getArgs, 10*time.Second, maxOutput)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		var invocation struct {
			Status                string `json:"Status"`
			StandardOutputContent string `json:"StandardOutputContent"`
			StandardErrorContent  string `json:"StandardErrorContent"`
			ResponseCode          int    `json:"ResponseCode"`
		}

		if err := json.Unmarshal([]byte(result.Stdout), &invocation); err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		switch invocation.Status {
		case "Success":
			return &Result{
				Stdout:   invocation.StandardOutputContent,
				Stderr:   invocation.StandardErrorContent,
				ExitCode: invocation.ResponseCode,
			}, nil
		case "Failed", "Cancelled", "TimedOut":
			return &Result{
				Stdout:   invocation.StandardOutputContent,
				Stderr:   invocation.StandardErrorContent,
				ExitCode: 1,
			}, nil
		default:
			// Pending, InProgress — keep polling
			time.Sleep(2 * time.Second)
		}
	}

	return nil, fmt.Errorf("timed out waiting for SSM command %s to complete", commandID)
}

// ── GCloud ───────────────────────────────────────────────────────────

// runGCloud executes a command on a GCP instance via gcloud compute ssh.
// Uses gcloud's native --command flag for non-interactive execution.
func runGCloud(ctx context.Context, args []string, command string, timeout time.Duration, maxOutput int64) (*Result, error) {
	// Verify this is a compute ssh command
	if len(args) < 2 || args[0] != "compute" || args[1] != "ssh" {
		return nil, fmt.Errorf("only 'gcloud compute ssh' commands are supported for remote command execution")
	}

	// Build gcloud command with --command flag
	cmdArgs := make([]string, 0, len(args)+2)
	cmdArgs = append(cmdArgs, args...)
	cmdArgs = append(cmdArgs, "--command", command)

	return executeCommand(ctx, "gcloud", cmdArgs, timeout, maxOutput)
}

// ── Azure ────────────────────────────────────────────────────────────

// runAzure executes a command on an Azure VM via az ssh vm or az network bastion ssh.
// Uses the -- separator to pass the command to the underlying SSH session.
func runAzure(ctx context.Context, args []string, command string, timeout time.Duration, maxOutput int64) (*Result, error) {
	// Verify this is an SSH-type command
	isSSHVM := len(args) >= 2 && args[0] == "ssh" && args[1] == "vm"
	isBastion := len(args) >= 4 && args[0] == "network" && args[1] == "bastion" && args[2] == "ssh"

	if !isSSHVM && !isBastion {
		return nil, fmt.Errorf("only 'az ssh vm' and 'az network bastion ssh' commands are supported for remote command execution")
	}

	// Build az command with -- separator and command
	cmdArgs := make([]string, 0, len(args)+2)
	cmdArgs = append(cmdArgs, args...)
	cmdArgs = append(cmdArgs, "--", command)

	return executeCommand(ctx, "az", cmdArgs, timeout, maxOutput)
}

// ── Shared execution ─────────────────────────────────────────────────

// executeCommand runs a cloud CLI command and captures output.
func executeCommand(ctx context.Context, binary string, args []string, timeout time.Duration, maxOutput int64) (*Result, error) {
	path, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("%s CLI not found: install the %s CLI to use this command", binary, binary)
	}

	if timeout == 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, args...)

	var stdout, stderr limitedBuffer
	stdout.limit = maxOutput
	stderr.limit = maxOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	result := &Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("command timed out after %s", timeout)
		} else {
			return nil, fmt.Errorf("execution failed: %w", err)
		}
	}

	return result, nil
}

// ── Utility functions ────────────────────────────────────────────────

// providerBinary returns the CLI binary name for a provider.
func providerBinary(provider Provider) string {
	switch provider {
	case AWS:
		return "aws"
	case GCloud:
		return "gcloud"
	case Azure:
		return "az"
	default:
		return string(provider)
	}
}

// extractIdentifier extracts a human-readable identifier from cloud CLI args
// for use in the shell prompt.
func extractIdentifier(provider Provider, args []string) string {
	switch provider {
	case AWS:
		if id := extractFlagValue(args, "--target"); id != "" {
			return id
		}
		if id := extractFlagValue(args, "--instance-id"); id != "" {
			return id
		}
		if id := extractFlagValue(args, "--instance-ids"); id != "" {
			return id
		}
	case GCloud:
		// Instance name is the first positional arg after "compute ssh"
		for i, arg := range args {
			if i >= 2 && arg == "ssh" {
				// Next arg after "ssh" is the instance name
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					return args[i+1]
				}
			}
		}
		// Fallback: find first non-flag, non-flag-value positional arg
		skipNext := false
		for i := 2; i < len(args); i++ {
			if skipNext {
				skipNext = false
				continue
			}
			if strings.HasPrefix(args[i], "--") {
				// Skip flag value too (unless --flag=value form)
				if !strings.Contains(args[i], "=") {
					skipNext = true
				}
				continue
			}
			if strings.HasPrefix(args[i], "-") {
				skipNext = true
				continue
			}
			return args[i]
		}
	case Azure:
		if name := extractFlagValue(args, "--name"); name != "" {
			return name
		}
		if rg := extractFlagValue(args, "--resource-group"); rg != "" {
			return rg
		}
	}
	return string(provider)
}

// extractFlagValue returns the value following a flag in args.
// Handles both --flag value and --flag=value forms.
func extractFlagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, flag+"=") {
			return strings.TrimPrefix(arg, flag+"=")
		}
	}
	return ""
}

// escapeJSONString escapes a string for safe embedding in a JSON value.
func escapeJSONString(s string) string {
	// Use json.Marshal to get proper escaping, then strip the quotes
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	// json.Marshal wraps in quotes: "string" → strip them
	if len(b) >= 2 {
		return string(b[1 : len(b)-1])
	}
	return s
}

// printShellHelp displays help for the cloud restricted shell.
func printShellHelp(provider Provider) {
	fmt.Fprintln(os.Stderr, "lily <provider> — restricted cloud shell")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  Every command is validated against lily's read-only allowlist.")
	fmt.Fprintln(os.Stderr, "  Allowed: file inspection (cat, ls, find), system info (ps, systemctl status),")
	fmt.Fprintln(os.Stderr, "           network diagnostics (ss, dig, curl GET), and text processing (grep, awk).")
	fmt.Fprintln(os.Stderr, "  Blocked: destructive commands (rm, mv, chmod), shells (bash, sh, python),")
	fmt.Fprintln(os.Stderr, "           privilege escalation (sudo, su), and file transfer (scp, rsync).")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "  Provider: %s (via %s CLI)\n", provider, providerBinary(provider))
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  exit, quit  — disconnect")
	fmt.Fprintln(os.Stderr, "  help        — show this message")
	fmt.Fprintln(os.Stderr, "")
}

// ValidateSubcommand checks whether the cloud CLI args form a valid SSH
// subcommand that lily can handle. Returns an error describing what's wrong
// if the subcommand is unsupported.
func ValidateSubcommand(provider Provider, args []string) error {
	switch provider {
	case AWS:
		if len(args) < 2 || args[0] != "ssm" {
			return fmt.Errorf("only 'aws ssm' commands are supported; use 'lily aws ssm start-session --target <instance-id> [--command \"<cmd>\"]'")
		}
		if args[1] != "start-session" {
			return fmt.Errorf("only 'aws ssm start-session' is supported; got 'aws ssm %s'", args[1])
		}
	case GCloud:
		if len(args) < 2 || args[0] != "compute" || args[1] != "ssh" {
			return fmt.Errorf("only 'gcloud compute ssh' commands are supported; use 'lily gcloud compute ssh <INSTANCE> --project <P> --zone <Z> [--command \"<cmd>\"]'")
		}
	case Azure:
		isSSHVM := len(args) >= 2 && args[0] == "ssh" && args[1] == "vm"
		isBastion := len(args) >= 4 && args[0] == "network" && args[1] == "bastion" && args[2] == "ssh"
		if !isSSHVM && !isBastion {
			return fmt.Errorf("only 'az ssh vm' and 'az network bastion ssh' commands are supported; use 'lily azure ssh vm --resource-group <RG> --name <VM> [--command \"<cmd>\"]'")
		}
	default:
		return fmt.Errorf("unknown provider: %s", provider)
	}
	return nil
}

// DetectCloudSSH checks if a command is a cloud CLI SSH command and returns
// the provider and whether it's detected. Used by the guard for rewrite.
func DetectCloudSSH(command string) (provider Provider, rewritten string, detected bool) {
	tokens := tokenizeCommand(command)
	if len(tokens) == 0 {
		return "", "", false
	}

	firstCmd := tokens[0]
	// Strip path prefix
	if idx := strings.LastIndex(firstCmd, "/"); idx >= 0 {
		firstCmd = firstCmd[idx+1:]
	}

	switch firstCmd {
	case "aws":
		return detectAWSCloudSSH(command, tokens)
	case "gcloud":
		return detectGCloudCloudSSH(command, tokens)
	case "az":
		return detectAzureCloudSSH(command, tokens)
	}

	return "", "", false
}

func detectAWSCloudSSH(command string, tokens []string) (Provider, string, bool) {
	// aws ssm start-session
	if len(tokens) >= 3 && tokens[1] == "ssm" && tokens[2] == "start-session" {
		return AWS, "lily " + command, true
	}
	// aws ec2-instance-connect ssh
	if len(tokens) >= 3 && tokens[1] == "ec2-instance-connect" && tokens[2] == "ssh" {
		return AWS, "lily " + command, true
	}
	return "", "", false
}

func detectGCloudCloudSSH(command string, tokens []string) (Provider, string, bool) {
	// gcloud compute ssh
	if len(tokens) >= 3 && tokens[1] == "compute" && tokens[2] == "ssh" {
		return GCloud, "lily " + command, true
	}
	return "", "", false
}

func detectAzureCloudSSH(command string, tokens []string) (Provider, string, bool) {
	// az ssh vm
	if len(tokens) >= 3 && tokens[1] == "ssh" && tokens[2] == "vm" {
		// Replace "az" with "lily azure"
		rest := strings.Join(tokens[1:], " ")
		return Azure, "lily azure " + rest, true
	}
	// az network bastion ssh
	if len(tokens) >= 4 && tokens[1] == "network" && tokens[2] == "bastion" && tokens[3] == "ssh" {
		rest := strings.Join(tokens[1:], " ")
		return Azure, "lily azure " + rest, true
	}
	return "", "", false
}

// tokenizeCommand splits a command string into tokens, respecting quotes.
func tokenizeCommand(s string) []string {
	var tokens []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for _, ch := range s {
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
		case ch == '"' && !inSingle:
			inDouble = !inDouble
		case (ch == ' ' || ch == '\t') && !inSingle && !inDouble:
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(ch)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}
