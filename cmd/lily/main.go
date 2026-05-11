package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aspectrr/lily/internal/allowlist"
	"github.com/aspectrr/lily/internal/cloud"
	"github.com/aspectrr/lily/internal/guard"
	"github.com/aspectrr/lily/internal/memory"
	"github.com/aspectrr/lily/internal/readonly"
	"github.com/aspectrr/lily/internal/sshconfig"
	"github.com/aspectrr/lily/internal/sshexec"
	"github.com/aspectrr/lily/internal/sshshell"
	"github.com/aspectrr/lily/internal/version"
)

const usageText = `lily - Read-only remote command execution via SSH for AI agents

Usage:
  lily [flags]
  lily <command> [arguments]

Commands:
  hosts                  List hosts from SSH config
  run <host> <command>   Execute a validated read-only command on a host
  ssh <host>             Open a restricted interactive SSH shell on a host
  validate <command>     Check if a command is allowed (no execution)
  check <host>           Test SSH connectivity to a host
  list-commands          List all allowed commands
  config-path            Show the config file path
  validate-config        Validate the lily.yaml config file
  aws <args...>          Run validated command on AWS instance via SSM
  gcloud <args...>       Run validated command on GCP instance via gcloud
  az <args...>           Run validated command on Azure VM via az
  kubectl <args...>       Run validated command in Kubernetes pod via kubectl exec
  rewrite <command>      Rewrite SSH commands to use lily run (for hooks)
  guard-hook <agent>     Run as agent hook (reads JSON stdin)
  guard install <agent>  Install guard hook into an agent
  guard uninstall <agent> Remove guard hook from an agent
  guard status           Show guard hook installation status
  memory status          Show investigation memory status
  memory list            List past investigations
  memory clear           Clear all stored investigations
  version                Print version

Flags:
  -config <path>         Path to SSH config (default: ~/.ssh/config)
  -config-file <path>    Path to lily.yaml config (default: ~/.config/lily/lily.yaml)
  -timeout <duration>    SSH command timeout (default: 30s)

Examples:
  lily hosts
  lily run web1 "systemctl status nginx"
  lily validate "rm -rf /"
  lily aws ssm start-session --target i-xxx --command "ps aux"
  lily gcloud compute ssh my-instance --project P --zone Z --command "ps aux"
  lily az ssh vm --resource-group RG --name VM --command "ps aux"
  lily kubectl exec my-pod -- ps aux
  lily kubectl exec my-pod -c sidecar -n prod -- "cat /etc/config.yaml"
  lily guard install claude-code
  lily guard install all
  lily guard status
`

// version is now defined in internal/version to avoid duplication.
// Use: version.Version
// Build override: go build -ldflags="-X internal/version.Version=X.Y.Z"

func main() {
	args := os.Args[1:]

	sshConfigPath := ""
	configFilePath := ""
	timeout := 30 * time.Second

	i := 0
	for i < len(args) {
		switch args[i] {
		case "-config":
			if i+1 >= len(args) {
				fatal("missing value for -config")
			}
			sshConfigPath = args[i+1]
			args = append(args[:i], args[i+2:]...)
		case "-allowlist", "-config-file":
			if i+1 >= len(args) {
				fatal("missing value for -config-file")
			}
			configFilePath = args[i+1]
			args = append(args[:i], args[i+2:]...)
		case "-timeout":
			if i+1 >= len(args) {
				fatal("missing value for -timeout")
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				fatal(fmt.Sprintf("invalid timeout %q: %s", args[i+1], err))
			}
			timeout = d
			args = append(args[:i], args[i+2:]...)
		case "-h", "-help", "--help":
			fmt.Print(usageText)
			os.Exit(0)
		default:
			i++
		}
	}

	if len(args) == 0 {
		fmt.Print(usageText)
		os.Exit(0)
	}

	switch args[0] {
	case "hosts":
		hosts(sshConfigPath)
	case "run":
		if len(args) < 3 {
			fatal("usage: lily run <host> <command>")
		}
		run(sshConfigPath, configFilePath, timeout, args[1], strings.Join(args[2:], " "))
	case "ssh":
		if len(args) < 2 {
			fatal("usage: lily ssh <host>")
		}
		sshShell(sshConfigPath, configFilePath, timeout, args[1])
	case "validate":
		if len(args) < 2 {
			fatal("usage: lily validate <command>")
		}
		validate(configFilePath, strings.Join(args[1:], " "))
	case "check":
		if len(args) < 2 {
			fatal("usage: lily check <host>")
		}
		check(sshConfigPath, configFilePath, timeout, args[1])
	case "list-commands":
		listCommands(configFilePath)
	case "config-path":
		fmt.Println(allowlist.DefaultConfigPath())
	case "validate-config":
		validateConfig(configFilePath)
	case "version":
		fmt.Printf("lily %s\n", version.Version)
	case "memory":
		memoryCmd(args[1:])
	case "rewrite":
		if len(args) < 2 {
			fatal("usage: lily rewrite <command>")
		}
		rewriteCmd(strings.Join(args[1:], " "))
	case "guard-hook":
		if len(args) < 2 {
			fatal("usage: lily guard-hook <agent>  (reads JSON stdin)")
		}
		os.Exit(guard.RunHook(args[1]))
	case "guard":
		guardCmd(args[1:])
	case "aws":
		cloudCmd(cloud.AWS, args[1:], configFilePath, timeout)
	case "gcloud":
		cloudCmd(cloud.GCloud, args[1:], configFilePath, timeout)
	case "az":
		cloudCmd(cloud.Azure, args[1:], configFilePath, timeout)
	case "kubectl":
		cloudCmd(cloud.Kubectl, args[1:], configFilePath, timeout)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		fmt.Print(usageText)
		os.Exit(1)
	}
}

func loadConfig(path string) *allowlist.Config {
	cfg, err := allowlist.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to load config: %s\n", err)
		return &allowlist.Config{}
	}
	return cfg
}

func hosts(sshConfigPath string) {
	entries, err := sshconfig.Parse(sshConfigPath)
	if err != nil {
		fatal(fmt.Sprintf("failed to parse SSH config: %s", err))
	}

	if len(entries) == 0 {
		fmt.Println("No hosts found in SSH config.")
		return
	}

	fmt.Printf("%-25s %-40s %-10s %-10s %s\n", "HOST", "HOSTNAME", "USER", "PORT", "PROXY")
	fmt.Println(strings.Repeat("-", 100))
	for _, h := range entries {
		hostName := h.HostName
		if hostName == "" {
			hostName = h.Host
		}
		user := h.User
		if user == "" {
			user = "-"
		}
		port := h.Port
		if port == "" {
			port = "22"
		}
		proxy := h.ProxyJump
		if proxy == "" {
			proxy = "-"
		}
		fmt.Printf("%-25s %-40s %-10s %-10s %s\n", h.Host, hostName, user, port, proxy)
	}
}

func run(sshConfigPath, configFilePath string, timeout time.Duration, hostName, command string) {
	cfg := loadConfig(configFilePath)
	validator := readonly.NewValidator(cfg.ExtraCommands, cfg.SubcommandRestrictions(), cfg.BlockedFlags())

	if err := validator.ValidateCommand(command); err != nil {
		fatal(fmt.Sprintf("command blocked: %s", err))
	}

	safeCommand, err := validator.SanitizeCommand(command)
	if err != nil {
		fatal(fmt.Sprintf("command sanitization failed: %s", err))
	}

	hosts, err := sshconfig.Parse(sshConfigPath)
	if err != nil {
		fatal(fmt.Sprintf("failed to parse SSH config: %s", err))
	}

	if sshconfig.LookupHost(hosts, hostName) == nil {
		fatal(fmt.Sprintf("host %q not found in SSH config", hostName))
	}

	maxOutput := cfg.GetMaxOutputBytes()
	exec := sshexec.NewExecutor(hosts, timeout, maxOutput)
	result, err := exec.Run(context.Background(), hostName, safeCommand)
	if err != nil {
		fatal(fmt.Sprintf("execution failed: %s", err))
	}

	// Record command in investigation memory and get hints
	var hint *memory.Hint
	if cfg.Memory.Enabled {
		tracker := memory.NewTracker(&memory.Config{
			Enabled:                  true,
			SessionTimeout:           cfg.Memory.SessionTimeout,
			MaxInvestigationsPerHost: cfg.Memory.MaxInvestigationsPerHost,
		})
		output := result.Stdout
		if result.Stderr != "" {
			output += "\n" + result.Stderr
		}
		hint = tracker.RecordCommand(hostName, command, output)
	}

	if result.Stdout != "" {
		fmt.Print(result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprintf(os.Stderr, "%s", result.Stderr)
	}
	if result.Truncated {
		fmt.Fprintf(os.Stderr, "[output truncated at %d bytes]\n", maxOutput)
	}

	// Append past investigation hint if relevant
	if hint != nil {
		if hintText := hint.FormatHint(); hintText != "" {
			fmt.Print(hintText)
		}
	}

	os.Exit(result.ExitCode)
}

func validate(configFilePath, command string) {
	cfg := loadConfig(configFilePath)
	validator := readonly.NewValidator(cfg.ExtraCommands, cfg.SubcommandRestrictions(), cfg.BlockedFlags())

	if err := validator.ValidateCommand(command); err != nil {
		fmt.Printf("BLOCKED: %s\n", err)
		os.Exit(1)
	}
	fmt.Printf("ALLOWED: %q\n", command)
}

func sshShell(sshConfigPath, configFilePath string, timeout time.Duration, hostName string) {
	hosts, err := sshconfig.Parse(sshConfigPath)
	if err != nil {
		fatal(fmt.Sprintf("failed to parse SSH config: %s", err))
	}

	if sshconfig.LookupHost(hosts, hostName) == nil {
		fatal(fmt.Sprintf("host %q not found in SSH config", hostName))
	}

	cfg := loadConfig(configFilePath)
	validator := readonly.NewValidator(cfg.ExtraCommands, cfg.SubcommandRestrictions(), cfg.BlockedFlags())
	maxOutput := cfg.GetMaxOutputBytes()
	exec := sshexec.NewExecutor(hosts, timeout, maxOutput)

	shell := sshshell.NewShell(hosts, exec, validator)
	if err := shell.Run(context.Background(), hostName); err != nil {
		fatal(fmt.Sprintf("ssh session failed: %s", err))
	}
}

func check(sshConfigPath, configFilePath string, timeout time.Duration, hostName string) {
	cfg := loadConfig(configFilePath)

	hosts, err := sshconfig.Parse(sshConfigPath)
	if err != nil {
		fatal(fmt.Sprintf("failed to parse SSH config: %s", err))
	}

	if sshconfig.LookupHost(hosts, hostName) == nil {
		fatal(fmt.Sprintf("host %q not found in SSH config", hostName))
	}

	exec := sshexec.NewExecutor(hosts, timeout, cfg.GetMaxOutputBytes())
	result, err := exec.Run(context.Background(), hostName, "echo ok")
	if err != nil {
		fatal(fmt.Sprintf("connection failed: %s", err))
	}

	if result.ExitCode != 0 {
		fatal(fmt.Sprintf("host returned exit code %d: %s", result.ExitCode, result.Stderr))
	}

	fmt.Printf("Host %q is reachable.\n", hostName)
}

func listCommands(configFilePath string) {
	cfg := loadConfig(configFilePath)
	validator := readonly.NewValidator(cfg.ExtraCommands, cfg.SubcommandRestrictions(), cfg.BlockedFlags())

	cmds := validator.AllowedCommandsList()
	fmt.Printf("Allowed commands (%d):\n\n", len(cmds))
	for i, cmd := range cmds {
		if i > 0 && i%6 == 0 {
			fmt.Println()
		}
		fmt.Printf("  %-14s", cmd)
	}
	fmt.Println()

	if len(cfg.ExtraCommands) > 0 {
		fmt.Printf("\nUser-configured extras: %s\n", strings.Join(cfg.ExtraCommands, ", "))
	}

	restrictions := map[string][]string{
		"systemctl": {"status", "show", "list-units", "is-active", "is-enabled"},
		"dpkg":      {"-l", "--list", "-s", "--status"},
		"rpm":       {"-qa", "-q"},
		"apt":       {"list", "show"},
		"pip":       {"list", "show"},
		"openssl":   {"x509", "verify", "s_client", "crl", "version", "ciphers", "req"},
	}
	for cmd, subs := range cfg.ExtraSubcommandRestrictions {
		restrictions[cmd] = subs
	}
	fmt.Println("\nSubcommand restrictions:")
	b, _ := json.MarshalIndent(restrictions, "  ", "  ")
	fmt.Printf("  %s\n", string(b))

	fmt.Printf("\nExecution limits:\n")
	fmt.Printf("  Rate limit:       %s\n", cfg.GetRateLimit())
	fmt.Printf("  Max output:       %d bytes (%.1f MB)\n", cfg.GetMaxOutputBytes(), float64(cfg.GetMaxOutputBytes())/(1024*1024))
}

func validateConfig(configFilePath string) {
	cfg, err := allowlist.Load(configFilePath)
	if err != nil {
		fatal(fmt.Sprintf("invalid config: %s", err))
	}
	fmt.Printf("Config valid.\n")
	fmt.Printf("  Extra commands:    %d\n", len(cfg.ExtraCommands))
	fmt.Printf("  Extra restrictions: %d\n", len(cfg.ExtraSubcommandRestrictions))
	fmt.Printf("  Extra blocked flags: %d\n", len(cfg.ExtraBlockedFlags))
	fmt.Printf("  Rate limit:        %s\n", cfg.GetRateLimit())
	fmt.Printf("  Max output:        %d bytes\n", cfg.GetMaxOutputBytes())
}

func fatal(msg string) {
	fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	os.Exit(1)
}

func findBinary() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
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

	if abs, err := filepath.Abs("./bin/lily"); err == nil {
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
	}

	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			abs = c
		}
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
	}

	return "lily"
}

func rewriteCmd(command string) {
	result := guard.Rewrite(command)
	switch result.Decision {
	case "rewrite":
		fmt.Print(result.Rewritten)
	case "block":
		fmt.Fprintf(os.Stderr, "blocked: %s\n", result.Reason)
		os.Exit(1)
	case "passthrough":
		// No output, exit 0 — passthrough
	default:
		// No output, exit 0
	}
}

func guardCmd(args []string) {
	if len(args) == 0 {
		fatal("usage: lily guard <install|uninstall|status> [agent]")
	}

	switch args[0] {
	case "install":
		if len(args) < 2 {
			fatal("usage: lily guard install <agent|all>")
		}
		installGuard(args[1])
	case "uninstall":
		if len(args) < 2 {
			fatal("usage: lily guard uninstall <agent|all>")
		}
		uninstallGuard(args[1])
	case "status":
		guardStatus()
	default:
		fatal(fmt.Sprintf("unknown guard subcommand: %s", args[0]))
	}
}

func installGuard(agentName string) {
	binaryPath := findBinary()

	if agentName == "all" {
		targets := guard.GuardTargets()
		installed := 0
		for _, tgt := range targets {
			if err := guard.InstallGuard(tgt, binaryPath); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ %s: %s\n", tgt.Name, err)
			} else {
				fmt.Printf("  ✓ %s → %s\n", tgt.Name, tgt.ConfigPath)
				installed++
			}
		}
		fmt.Printf("\nInstalled guard to %d agent(s).\n", installed)
		return
	}

	target := guard.LookupGuardTarget(agentName)
	if target == nil {
		fatal(fmt.Sprintf("unknown agent %q. Available: %s", agentName, guard.GuardTargetNames()))
	}

	if err := guard.InstallGuard(*target, binaryPath); err != nil {
		fatal(fmt.Sprintf("install failed: %s", err))
	}
	fmt.Printf("Installed lily guard to %s → %s\n", target.Name, target.ConfigPath)
}

func uninstallGuard(agentName string) {
	if agentName == "all" {
		targets := guard.GuardTargets()
		removed := 0
		for _, t := range targets {
			if err := guard.UninstallGuard(t); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ %s: %s\n", t.Name, err)
			} else {
				fmt.Printf("  ✓ %s removed\n", t.Name)
				removed++
			}
		}
		fmt.Printf("\nRemoved guard from %d agent(s).\n", removed)
		return
	}

	target := guard.LookupGuardTarget(agentName)
	if target == nil {
		fatal(fmt.Sprintf("unknown agent %q. Available: %s", agentName, guard.GuardTargetNames()))
	}

	if err := guard.UninstallGuard(*target); err != nil {
		fatal(fmt.Sprintf("uninstall failed: %s", err))
	}
	fmt.Printf("Removed lily guard from %s\n", target.Name)
}

func guardStatus() {
	targets := guard.GuardTargets()

	fmt.Println("Lily guard status:")
	fmt.Println()
	fmt.Printf("  %-20s %-12s %s\n", "AGENT", "GUARD", "CONFIG PATH")
	fmt.Println(strings.Repeat("-", 85))

	for _, t := range targets {
		status := "not installed"
		switch t.ConfigFormat {
		case "claude-settings":
			if isGuardInstalledInJSON(t.ConfigPath) {
				status = "✓ installed"
			}
		case "codex-hooks":
			if isGuardInstalledInJSON(t.ConfigPath) {
				status = "✓ installed"
			}
		case "cursor-hooks":
			if isGuardInstalledInJSON(t.ConfigPath) {
				status = "✓ installed"
			}
		case "pi-extension":
			if fileExists(t.ConfigPath) {
				status = "✓ installed"
			}
		}
		fmt.Printf("  %-20s %-12s %s\n", t.Name, status, t.ConfigPath)
	}

	fmt.Printf("\nUsage: lily guard install <agent>\n       lily guard install all\n")
}

// cloudCmd handles lily aws, lily gcloud, and lily az CLI commands.
// These wrap cloud provider SSH commands with lily's read-only validation.
//
// Usage:
//
//	lily aws ssm start-session --target i-xxx --command "ps aux"
//	lily gcloud compute ssh INSTANCE --project P --zone Z --command "ps aux"
//	lily az ssh vm --resource-group RG --name VM --command "ps aux"
//
// Without --command, opens an interactive restricted shell.
func cloudCmd(provider cloud.Provider, args []string, configFilePath string, timeout time.Duration) {
	if len(args) == 0 {
		switch provider {
		case cloud.AWS:
			fatal("usage: lily aws ssm start-session --target <instance-id> [--command \"<cmd>\"]")
		case cloud.GCloud:
			fatal("usage: lily gcloud compute ssh <INSTANCE> --project <P> --zone <Z> [--command \"<cmd>\"]")
		case cloud.Azure:
			fatal("usage: lily az ssh vm --resource-group <RG> --name <VM> [--command \"<cmd>\"]")
		case cloud.Kubectl:
			fatal("usage: lily kubectl exec <POD> [-c <container>] [-n <namespace>] -- <command>")
		}
	}

	cfg := loadConfig(configFilePath)
	validator := readonly.NewValidator(cfg.ExtraCommands, cfg.SubcommandRestrictions(), cfg.BlockedFlags())
	maxOutput := cfg.GetMaxOutputBytes()

	// Parse --command from args
	providerArgs, command := cloud.ParseCommand(args)

	// Validate the subcommand structure before proceeding
	if err := cloud.ValidateSubcommand(provider, providerArgs); err != nil {
		fatal(err.Error())
	}

	if command != "" {
		// Single command mode: validate and execute
		result, err := cloud.Run(context.Background(), provider, providerArgs, command, validator, timeout, maxOutput)
		if err != nil {
			fatal(err.Error())
		}

		// Record command in investigation memory and get hints
		var hint *memory.Hint
		if cfg.Memory.Enabled {
			identifier := cloud.ExtractIdentifier(provider, providerArgs)
			tracker := memory.NewTracker(&memory.Config{
				Enabled:                  true,
				SessionTimeout:           cfg.Memory.SessionTimeout,
				MaxInvestigationsPerHost: cfg.Memory.MaxInvestigationsPerHost,
			})
			output := result.Stdout
			if result.Stderr != "" {
				output += "\n" + result.Stderr
			}
			hint = tracker.RecordCommand(identifier, command, output)
		}

		if result.Stdout != "" {
			fmt.Print(result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprintf(os.Stderr, "%s", result.Stderr)
		}

		// Append past investigation hint if relevant
		if hint != nil {
			if hintText := hint.FormatHint(); hintText != "" {
				fmt.Print(hintText)
			}
		}

		os.Exit(result.ExitCode)
	} else {
		// Interactive restricted shell mode
		if err := cloud.Shell(context.Background(), provider, providerArgs, validator, timeout, maxOutput); err != nil {
			fatal(err.Error())
		}
	}
}

func isGuardInstalledInJSON(configPath string) bool {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "lily guard-hook")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func memoryCmd(args []string) {
	if len(args) == 0 {
		fatal("usage: lily memory <status|list|clear>")
	}

	cfg := loadConfig("")

	switch args[0] {
	case "status":
		tracker := memory.NewTracker(&memory.Config{
			Enabled:                  cfg.Memory.Enabled,
			SessionTimeout:           cfg.Memory.SessionTimeout,
			MaxInvestigationsPerHost: cfg.Memory.MaxInvestigationsPerHost,
		})
		invs := tracker.ListInvestigations()
		fmt.Printf("Investigation memory: %s\n", func() string {
			if cfg.Memory.Enabled {
				return "enabled"
			}
			return "disabled"
		}())
		fmt.Printf("  Stored investigations: %d\n", len(invs))
		fmt.Printf("  Session timeout:      %s\n", cfg.Memory.SessionTimeout)
		fmt.Printf("  Max per host:         %d\n", func() int {
			if cfg.Memory.MaxInvestigationsPerHost > 0 {
				return cfg.Memory.MaxInvestigationsPerHost
			}
			return memory.DefaultMaxInvestigations
		}())
		fmt.Printf("  Memory directory:     %s\n", memory.MemoryDir())

	case "list":
		tracker := memory.NewTracker(&memory.Config{
			Enabled:                  true, // Always allow listing even if disabled
			SessionTimeout:           cfg.Memory.SessionTimeout,
			MaxInvestigationsPerHost: cfg.Memory.MaxInvestigationsPerHost,
		})
		invs := tracker.ListInvestigations()
		if len(invs) == 0 {
			fmt.Println("No past investigations found.")
			return
		}
		fmt.Printf("Past investigations (%d):\n\n", len(invs))
		for _, inv := range invs {
			fmt.Printf("  %s  hosts: [%s]  trigger: %s",
				inv.StartTime.Format("2006-01-02 15:04"),
				strings.Join(inv.Hosts, ", "),
				inv.Trigger)
			if inv.RootCauseHint != "" {
				fmt.Printf("  cause: %s", inv.RootCauseHint)
			}
			fmt.Printf("  (%d commands)\n", len(inv.Commands))
		}

	case "clear":
		tracker := memory.NewTracker(&memory.Config{
			Enabled:                  true,
			SessionTimeout:           cfg.Memory.SessionTimeout,
			MaxInvestigationsPerHost: cfg.Memory.MaxInvestigationsPerHost,
		})
		if err := tracker.ClearAll(); err != nil {
			fatal(fmt.Sprintf("failed to clear memory: %s", err))
		}
		fmt.Println("All investigation memory cleared.")

	default:
		fatal(fmt.Sprintf("unknown memory subcommand: %s", args[0]))
	}
}
