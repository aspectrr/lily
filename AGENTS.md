# Lily MCP - Agent Reference

## What is this?

Lily MCP is a read-only remote command execution server for AI agents. It allows you to run diagnostic commands on remote hosts via SSH, with strict validation that prevents destructive operations.

Lily also supports **cloud provider SSH** — wrapping AWS SSM, Google Cloud, and Azure CLI commands with the same read-only validation. Cloud CLI commands are intercepted by the guard and automatically rewritten to use lily.

> **Note**: When the codebase changes, update `README.md`, `SECURITY.md`, and `SKILL.md` to reflect those changes.

## Quick Start

### MCP Server (stdio)

Configure your MCP client:

```json
{
  "mcpServers": {
    "lily": {
      "command": "lily",
      "args": ["serve"]
    }
  }
}
```

### CLI

```bash
# List available hosts
lily hosts

# Check connectivity
lily check web1

# Run a command
lily run web1 "systemctl status nginx"

# Validate without running
lily validate "rm -rf /"

# Show config file location
lily config-path

# Cloud provider SSH
lily aws ssm start-session --target i-12345 --command "ps aux"
lily gcloud compute ssh my-instance --project P --zone us-central1-a --command "ps aux"
lily azure ssh vm --resource-group MyRG --name MyVM --command "ps aux"
```

## MCP Tools

### `list_hosts`

Discover what hosts are available from the user's SSH config.

```
→ list_hosts()
← Found 2 host(s) in SSH config:
    web1                      192.168.1.10 (user: deploy)
    db1                       db.example.com (user: postgres)
```

### `check_host`

Test SSH connectivity before running commands.

```
→ check_host(host="web1")
← Host "web1" is reachable.
```

### `validate_command`

Check if a command would be allowed without executing it.

```
→ validate_command(command="systemctl restart nginx")
← BLOCKED: systemctl subcommand "restart" is not allowed in read-only mode
```

### `run_command`

Execute a validated read-only command on a remote host.

```
→ run_command(host="web1", command="journalctl -u nginx --no-pager -n 20")
← (output of journalctl)
```

### `list_allowed_commands`

Show all currently allowed commands including user-configured additions.

## Execution Limits

Lily enforces configurable limits to prevent abuse:

| Setting     | Default       | Config Key         | Description                              |
| ----------- | ------------- | ------------------ | ---------------------------------------- |
| Rate limit  | 1 command/sec | `rate_limit`       | Minimum interval between commands        |
| Max output  | 1 MB          | `max_output_bytes` | Output cap per command (stdout + stderr) |
| SSH timeout | 30s           | `-timeout` flag    | Maximum execution time per command       |

Rate limiting applies to `run_command` and `check_host` only. Read-only tools like `list_hosts` and `validate_command` are not rate-limited.

## ProxyJump (Bastion / Jump Host)

Lily supports SSH ProxyJump for reaching hosts that aren't directly accessible. When a host has `ProxyJump` set in `~/.ssh/config`, Lily automatically tunnels through the specified jump host(s).

```
Host bastion
    HostName 203.0.113.1
    User admin

Host web1
    HostName 10.0.0.5
    ProxyJump bastion
```

The AI agent uses `web1` normally — Lily handles the tunneling transparently. Multi-hop chains and recursive resolution are supported with loop detection.

Only `ProxyJump` is supported (SSH-native tunneling). `ProxyCommand` is intentionally not supported because it executes arbitrary local commands.

## Allowed Commands

**File inspection:** cat, ls, find, head, tail, stat, file, wc, du, tree, strings, md5sum, sha256sum, readlink, realpath, basename, dirname, base64

**Process/system:** ps, top, pgrep, systemctl (read-only subcommands only), journalctl, dmesg

**Network:** ss, netstat, ip, ifconfig, dig, nslookup, ping, curl (GET only), openssl (read-only)

**Disk:** df, lsblk, blkid

**Package query:** dpkg (list/status), rpm (query), apt (list/show), pip (list/show)

**System info:** uname, hostname, uptime, free, lscpu, lsmod, lspci, lsusb, arch, nproc

**User:** whoami, id, groups, who, w, last

**Text processing in pipes:** grep, awk, sed (no -i), sort, uniq, cut, tr, xargs

### Subcommand Restrictions

| Command   | Allowed                                                           |
| --------- | ----------------------------------------------------------------- |
| systemctl | status, show, list-units, is-active, is-enabled                   |
| dpkg      | -l, --list, -s, --status                                          |
| rpm       | -qa, -q                                                           |
| apt       | list, show                                                        |
| pip       | list, show                                                        |
| openssl   | x509, verify, s_client, crl, version, ciphers, req (display only) |
| curl      | GET requests only (no -X, -d, -F, -T, -o, -O, --proxy)            |

### What's Always Blocked

These can **never** be allowed, even via user config:

- Destructive: rm, mv, cp, dd, chmod, chown, kill, shutdown, reboot
- Privilege escalation: sudo, su, pkexec
- Shells/interpreters: bash, sh, zsh, python, perl, ruby, node, php
- Editors: vi, vim, nano, emacs
- File transfer: scp, rsync, sftp, wget
- Package mutation: apt install/remove, pip install/uninstall, dpkg -i
- Metacharacters: `$(...)`, backticks, `>`, `>>`, `<(...)`, `>(...)`, newlines

## Configuration

Config lives at `~/.config/lily/lily.yaml` (run `lily config-path` to find it).

```yaml
# ── Execution Limits ──
rate_limit: "1s" # min interval between commands (default 1s)
max_output_bytes: 1048576 # max output per command in bytes (default 1 MB)

# ── Command Allowlist ──
extra_commands:
  - docker
  - kubectl

extra_subcommand_restrictions:
  docker:
    - ps
    - logs
    - inspect
  kubectl:
    - get
    - describe
    - logs

extra_blocked_flags:
  docker:
    - exec
    - run
```

Config only **adds** to the base allowlist. Hardcoded restrictions (rm, sudo, bash, etc.) cannot be overridden.

## Cloud Provider SSH

Lily wraps cloud provider CLI commands with the same read-only validation used for SSH. This lets agents run diagnostic commands on cloud instances without needing SSH config entries.

### AWS (SSM Session Manager)

```bash
# Run a single command
lily aws ssm start-session --target i-0123456789abcdef0 --command "systemctl status nginx"

# Interactive restricted shell
lily aws ssm start-session --target i-0123456789abcdef0
```

Under the hood, lily uses `aws ssm send-command` with the `AWS-RunShellScript` document for non-interactive execution, and polls `get-command-invocation` for the result.

**Note**: `aws ec2-instance-connect ssh` is detected by the guard but does not support non-interactive command execution. Use SSM instead.

### Google Cloud

```bash
# Run a single command
lily gcloud compute ssh my-instance --project my-project --zone us-central1-a --command "ps aux"

# Via IAP tunnel (private IPs)
lily gcloud compute ssh my-instance --tunnel-through-iap --command "df -h"

# Interactive restricted shell
lily gcloud compute ssh my-instance --project my-project --zone us-central1-a
```

Uses gcloud's native `--command` flag.

### Azure

```bash
# Run a single command
lily azure ssh vm --resource-group MyResourceGroup --name MyVM --command "uptime"

# Via Azure Bastion
lily azure network bastion ssh --name MyBastion --resource-group MyRG --target-resource-id /subscriptions/.../virtualMachines/MyVM --command "free -h"

# Interactive restricted shell
lily azure ssh vm --resource-group MyResourceGroup --name MyVM
```

Uses the `--` separator to pass commands to the underlying SSH session.

### Guard Integration

The guard automatically detects and rewrites raw cloud CLI SSH commands:

```
aws ssm start-session --target i-xxx        → lily aws ssm start-session --target i-xxx
aws ec2-instance-connect ssh --instance-id  → lily aws ec2-instance-connect ssh --instance-id ...
gcloud compute ssh INSTANCE ...             → lily gcloud compute ssh INSTANCE ...
az ssh vm --resource-group RG --name VM     → lily azure ssh vm --resource-group RG --name VM
az network bastion ssh ...                  → lily azure network bastion ssh ...
```

Commands already prefixed with `lily` are left unchanged (passthrough).

## Architecture

```
lily/
├── cmd/lily/main.go           # CLI entry point
├── internal/
│   ├── allowlist/                  # YAML config parsing + execution limits
│   ├── cloud/                      # Cloud provider SSH (AWS, GCloud, Azure)
│   ├── guard/                      # Guard hooks (SSH + cloud CLI rewrite)
│   ├── mcp/                        # MCP server, tools, rate limiter
│   ├── readonly/                   # Command validation engine
│   ├── sshconfig/                  # SSH config parser
│   └── sshexec/                    # SSH execution
├── lily.yaml                       # Example config
├── SECURITY.md                     # Security model & sandboxing guide
├── Makefile
└── README.md
```

## Development

```bash
make build      # Build to bin/lily
make test       # Run all tests
make install    # Install to /usr/local/bin
```
