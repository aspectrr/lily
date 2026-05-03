package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aspectrr/lily/internal/allowlist"
	"github.com/aspectrr/lily/internal/install"
	"github.com/aspectrr/lily/internal/mcp"
	"github.com/aspectrr/lily/internal/readonly"
	"github.com/aspectrr/lily/internal/sshconfig"
	"github.com/aspectrr/lily/internal/sshexec"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const usageText = `lily - Read-only remote command execution via SSH for AI agents

Usage:
  lily [flags]
  lily <command> [arguments]

Commands:
  serve                  Start MCP server (stdio transport) [default]
  hosts                  List hosts from SSH config
  run <host> <command>   Execute a validated read-only command on a host
  validate <command>     Check if a command is allowed (no execution)
  check <host>           Test SSH connectivity to a host
  list-commands          List all allowed commands
  config-path            Show the config file path
  validate-config        Validate the lily.yaml config file
  install-skill          Install lily into an agent's MCP config
  uninstall-skill        Remove lily from an agent's MCP config
  list-agents            Show detected agents that support MCP
  version                Print version

Flags:
  -config <path>         Path to SSH config (default: ~/.ssh/config)
  -config-file <path>    Path to lily.yaml config (default: ~/.config/lily/lily.yaml)
  -timeout <duration>    SSH command timeout (default: 30s)

Examples:
  lily serve
  lily hosts
  lily run web1 "systemctl status nginx"
  lily validate "rm -rf /"
  lily install-skill claude-code
  lily install-skill all
  lily uninstall-skill cursor
  lily list-agents
`

const version = "0.2.0"

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
		serve(sshConfigPath, configFilePath, timeout)
		return
	}

	switch args[0] {
	case "serve":
		serve(sshConfigPath, configFilePath, timeout)
	case "hosts":
		hosts(sshConfigPath)
	case "run":
		if len(args) < 3 {
			fatal("usage: lily run <host> <command>")
		}
		run(sshConfigPath, configFilePath, timeout, args[1], strings.Join(args[2:], " "))
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
	case "install-skill":
		if len(args) < 2 {
			fatal("usage: lily install-skill <agent|all> [path/to/lily]")
		}
		installSkill(args[1], args[2:])
	case "uninstall-skill":
		if len(args) < 2 {
			fatal("usage: lily uninstall-skill <agent|all>")
		}
		uninstallSkill(args[1])
	case "list-agents":
		listAgents()
	case "version":
		fmt.Printf("lily %s\n", version)
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

func serve(sshConfigPath, configFilePath string, timeout time.Duration) {
	hosts, err := sshconfig.Parse(sshConfigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not parse SSH config: %s\n", err)
	}

	cfg := loadConfig(configFilePath)
	server := mcp.NewServer(hosts, timeout, cfg)

	if err := mcpserver.ServeStdio(server); err != nil {
		fatal(fmt.Sprintf("MCP server error: %s", err))
	}
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

	if result.Stdout != "" {
		fmt.Print(result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprintf(os.Stderr, "%s", result.Stderr)
	}
	if result.Truncated {
		fmt.Fprintf(os.Stderr, "[output truncated at %d bytes]\n", maxOutput)
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

func installSkill(agentName string, extraArgs []string) {
	binaryPath := ""
	if len(extraArgs) > 0 {
		binaryPath = extraArgs[0]
	}
	if binaryPath == "" {
		binaryPath = install.FindBinary()
	}

	if agentName == "all" {
		targets := install.KnownTargets()
		installed := 0
		for _, tgt := range targets {
			result, err := install.Install(tgt, binaryPath, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ %s: %s\n", tgt.Name, err)
			} else {
				fmt.Printf("  ✓ %s → %s\n", tgt.Name, tgt.ConfigPath)
				if result.AllowlistDeployed {
					fmt.Printf("    Created config: %s\n", result.AllowlistPath)
				}
				installed++
			}
		}
		fmt.Printf("\nInstalled to %d agent(s).\n", installed)
		return
	}

	target := install.LookupTarget(agentName)
	if target == nil {
		fatal(fmt.Sprintf("unknown agent %q. Available: %s", agentName, install.TargetNames()))
	}

	result, err := install.Install(*target, binaryPath, nil)
	if err != nil {
		fatal(fmt.Sprintf("install failed: %s", err))
	}
	fmt.Printf("Installed lily to %s → %s\n", target.Name, target.ConfigPath)
	if result.AllowlistDeployed {
		fmt.Printf("Created config: %s\n", result.AllowlistPath)
	}
	fmt.Printf("\nEdit config: %s\n", install.ConfigFilePath())
}

func uninstallSkill(agentName string) {
	if agentName == "all" {
		targets := install.KnownTargets()
		removed := 0
		for _, t := range targets {
			if err := install.Uninstall(t); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ %s: %s\n", t.Name, err)
			} else {
				fmt.Printf("  ✓ %s removed\n", t.Name)
				removed++
			}
		}
		fmt.Printf("\nRemoved from %d agent(s).\n", removed)
		return
	}

	target := install.LookupTarget(agentName)
	if target == nil {
		fatal(fmt.Sprintf("unknown agent %q. Available: %s", agentName, install.TargetNames()))
	}

	if err := install.Uninstall(*target); err != nil {
		fatal(fmt.Sprintf("uninstall failed: %s", err))
	}
	fmt.Printf("Removed lily from %s\n", target.Name)
}

func listAgents() {
	targets := install.KnownTargets()
	detected := install.DetectedTargets()

	fmt.Print("Known MCP agents:\n\n")
	fmt.Printf("  %-20s %-10s %s\n", "AGENT", "STATUS", "CONFIG PATH")
	fmt.Println(strings.Repeat("-", 85))

	detectedMap := map[string]bool{}
	for _, d := range detected {
		detectedMap[d.Name] = true
	}

	for _, t := range targets {
		status := "not found"
		if detectedMap[t.Name] {
			status = "detected"
		}
		fmt.Printf("  %-20s %-10s %s\n", t.Name, status, t.ConfigPath)
	}

	fmt.Printf("\nUsage: lily install-skill <agent>\n       lily install-skill all\n")
}

func fatal(msg string) {
	fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	os.Exit(1)
}
