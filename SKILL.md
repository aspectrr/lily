---
name: lily
description: Read-only remote command execution via SSH for AI agents. Diagnose remote servers, check logs, inspect services, read files, and investigate issues on hosts defined in SSH config. Provides safe read-only access with strict command validation — the agent cannot bypass restrictions.
version: 0.2.0
tools:
  - list_hosts
  - check_host
  - run_command
  - validate_command
  - list_allowed_commands
---

# Lily MCP — Remote Server Diagnostics

## Purpose

Lily MCP gives you safe, read-only SSH access to remote hosts and Kubernetes pods. Use it to investigate server issues, read logs, check service health, inspect configs, and debug problems — without any risk of making changes.

There are two ways to use it:

- **MCP server** — the agent calls tools directly (primary mode for AI agents)
- **CLI tool** — run commands from a terminal or shell session

Both use the same validation engine and allowlist.

## Cloud & Kubernetes Support

Lily extends its read-only validation to cloud provider CLIs (AWS SSM, Google Cloud, Azure) and `kubectl exec`:

```bash
# Cloud providers
lily aws ssm start-session --target i-0123456789abcdef0 --command "ps aux"
lily gcloud compute ssh my-instance --project P --zone Z --command "df -h"
lily azure ssh vm --resource-group RG --name VM --command "uptime"

# Kubernetes
lily kubectl exec my-pod -- ps aux
lily kubectl exec my-pod -c sidecar -n prod -- "cat /etc/config.yaml"
```

The guard automatically intercepts raw `kubectl exec POD -- command` invocations and rewrites them to go through lily's validation.

## Installation

### Quick install into your agent

```bash
# Build
make build

# See which agents are detected
bin/lily list-agents

# Install to a specific agent (writes MCP config + deploys config)
bin/lily install-skill claude-code
bin/lily install-skill cursor
bin/lily install-skill all

# Or install globally
make install
lily install-skill all
```

`install-skill` does two things:

1. Writes the MCP server entry into the agent's config file
2. Deploys a default `~/.config/lily/lily.yaml` if one doesn't exist

### Manual MCP config

If you prefer to configure manually, add this to your agent's MCP config:

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

Optional flags: `-config-file <path>`, `-config <path>`, `-timeout <duration>`

## MCP Tools

When running as an MCP server, these tools are available:

### `list_hosts`

Discover hosts from SSH config. **Always call this first.**

```
→ list_hosts()
← Found 2 host(s):
    web1    192.168.1.10 (user: deploy)
    db1     db.example.com (user: postgres)
```

### `check_host`

Verify SSH connectivity before running diagnostics.

```
→ check_host(host="web1")
← Host "web1" is reachable.
```

### `validate_command`

Check if a command would pass validation without executing it. Useful when unsure.

```
→ validate_command(command="systemctl restart nginx")
← BLOCKED: systemctl subcommand "restart" is not allowed in read-only mode
```

### `run_command`

Execute a validated read-only command on a remote host.

```
→ run_command(host="web1", command="journalctl -u nginx --no-pager -n 50")
← (command output)
```

### `list_allowed_commands`

Show all currently allowed commands including user-configured additions.

## CLI Usage

Everything the MCP tools do is also available from the command line:

```bash
# List available hosts
lily hosts

# Test connectivity
lily check web1

# Run a command
lily run web1 "systemctl status nginx"

# Validate without running
lily validate "rm -rf /"

# Show all allowed commands
lily list-commands

# Manage agent installs
lily list-agents
lily install-skill claude-code
lily uninstall-skill cursor

# Manage config
lily config-path
lily validate-config
```

## Diagnostic Patterns

### Service not working

```
run_command(host="web1", command="systemctl status nginx")
run_command(host="web1", command="journalctl -u nginx --since '1 hour ago' --no-pager")
run_command(host="web1", command="ss -tlnp | grep 80")
```

### Disk full

```
run_command(host="web1", command="df -h")
run_command(host="web1", command="du -sh /var/log/* | sort -rh | head -10")
```

### Memory pressure

```
run_command(host="web1", command="free -m")
run_command(host="web1", command="ps aux --sort=-%mem | head -20")
```

### Read a config file

```
run_command(host="web1", command="cat /etc/nginx/nginx.conf")
run_command(host="web1", command="find /etc/nginx -name '*.conf' -type f")
```

### Network problems

```
run_command(host="web1", command="ip addr")
run_command(host="web1", command="ss -tlnp")
run_command(host="web1", command="curl -s localhost:9200/_cluster/health?pretty")
```

### Log investigation

```
run_command(host="web1", command="tail -100 /var/log/syslog")
run_command(host="web1", command="dmesg | tail -50")
run_command(host="web1", command="journalctl -p err --no-pager -n 50")
```

## What's Allowed

| Category        | Commands                                                      |
| --------------- | ------------------------------------------------------------- |
| File inspection | cat, ls, find, head, tail, stat, file, wc, du, tree           |
| Text processing | grep, awk, sed (no -i), sort, uniq, cut, tr, xargs            |
| Process/system  | ps, top, pgrep, journalctl, dmesg                             |
| Systemctl       | status, show, list-units, is-active, is-enabled               |
| Network         | ss, netstat, ip, dig, nslookup, ping, curl (GET only)         |
| Disk            | df, lsblk, blkid                                              |
| System info     | uname, hostname, uptime, free, lscpu, lsmod, lspci, lsusb     |
| User info       | whoami, id, groups, who, w, last                              |
| Packages        | dpkg -l, rpm -qa, apt list, pip list                          |
| TLS             | openssl x509/verify/s_client (localhost only)/version/ciphers |

## What's Blocked (hardcoded, non-overridable)

- **Destructive:** rm, mv, cp, dd, chmod, chown, kill, shutdown, reboot, mkfs, mount
- **Escalation:** sudo, su, pkexec
- **Shells/interpreters:** bash, sh, zsh, python, perl, ruby, node, php
- **Editors:** vi, vim, nano, emacs
- **Transfer:** scp, rsync, wget, sftp
- **Metacharacters:** `$(...)`, backticks, `>`, `>>`, `<(...)`, `>(...)`, newlines

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
    - stats
  kubectl:
    - get
    - describe
    - logs

extra_blocked_flags:
  docker:
    - exec
    - run
```

Config only **adds** to the base allowlist. Hardcoded restrictions cannot be overridden — even if an agent edits the YAML file, `rm`, `sudo`, `bash`, etc. remain blocked.

Run `lily validate-config` to check your config, or `lily config-path` to find where it lives.
