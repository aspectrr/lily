---
name: lily
description: Read-only remote command execution via SSH for AI agents. Diagnose remote servers, check logs, inspect services, read files, and investigate issues on hosts defined in SSH config. Provides safe read-only access with strict command validation — the agent cannot bypass restrictions.
version: 0.3.0
---

# Lily — Remote Server Diagnostics

## Purpose

Lily gives you safe, read-only SSH access to remote hosts and Kubernetes pods. Use it to investigate server issues, read logs, check service health, inspect configs, and debug problems — without any risk of making changes.

Lily works as:

- **Guard hook** — installs into coding agents (Claude Code, Cursor, Codex) and automatically intercepts/rewrites SSH, cloud CLI, and kubectl exec commands
- **CLI tool** — run commands directly from a terminal or shell session

Both use the same validation engine and allowlist.

## Cloud & Kubernetes Support

Lily extends its read-only validation to cloud provider CLIs (AWS SSM, Google Cloud, Azure) and `kubectl exec`:

```bash
# Cloud providers
lily aws ssm start-session --target i-0123456789abcdef0 --command "ps aux"
lily gcloud compute ssh my-instance --project P --zone Z --command "df -h"
lily az ssh vm --resource-group RG --name VM --command "uptime"

# Kubernetes
lily kubectl exec my-pod -- ps aux
lily kubectl exec my-pod -c sidecar -n prod -- "cat /etc/config.yaml"
```

The guard automatically intercepts raw `kubectl exec POD -- command` invocations and rewrites them to go through lily's validation.

## Installation

Requires **Go 1.26.2+**.

```bash
go install github.com/aspectrr/lily/cmd/lily@latest
```

### Guard hook install

```bash
# Install into a specific agent
lily guard install claude-code
lily guard install cursor
lily guard install codex

# Or install into all detected agents
lily guard install all
```

The guard hook installs as a `PreToolUse` hook that automatically rewrites SSH/cloud/kubectl commands to use lily's validated execution.

### Manual config

If you prefer to configure manually, point your agent at the `lily` binary. The agent can call it directly:

```bash
lily run <host> "<command>"
lily check <host>
lily hosts
lily validate "<command>"
```

## CLI Usage

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

# Manage guard hooks
lily guard status
lily guard install claude-code
lily guard uninstall cursor

# Manage config
lily config-path
lily validate-config
```

## Diagnostic Patterns

### Service not working

```
lily run web1 "systemctl status nginx"
lily run web1 "journalctl -u nginx --since '1 hour ago' --no-pager"
lily run web1 "ss -tlnp | grep 80"
```

### Disk full

```
lily run web1 "df -h"
lily run web1 "du -sh /var/log/* | sort -rh | head -10"
```

### Memory pressure

```
lily run web1 "free -m"
lily run web1 "ps aux --sort=-%mem | head -20"
```

### Read a config file

```
lily run web1 "cat /etc/nginx/nginx.conf"
lily run web1 "find /etc/nginx -name '*.conf' -type f"
```

### Network problems

```
lily run web1 "ip addr"
lily run web1 "ss -tlnp"
lily run web1 "curl -s localhost:9200/_cluster/health?pretty"
```

### Log investigation

```
lily run web1 "tail -100 /var/log/syslog"
lily run web1 "dmesg | tail -50"
lily run web1 "journalctl -p err --no-pager -n 50"
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

# ── Investigation Memory ──
# Learn from past debugging sessions automatically.
memory:
  enabled: false # Set true to activate
  session_timeout: "10m" # Flush investigation after this idle time
  max_investigations_per_host: 50
```

Config only **adds** to the base allowlist. Hardcoded restrictions cannot be overridden — even if an agent edits the YAML file, `rm`, `sudo`, `bash`, etc. remain blocked.

Run `lily validate-config` to check your config, or `lily config-path` to find where it lives.

## Investigation Memory

When enabled in config, Lily automatically tracks debugging sessions and surfaces relevant past investigations when similar issues arise.

```yaml
memory:
  enabled: true
```

When an agent runs a command that matches a past investigation (same host, similar command, overlapping error keywords), Lily appends a hint to the output:

```
← ● nginx.service - A high performance web server
     Active: failed (Result: exit-code)

  ━━ Past Investigation (May 10, 87% similar) ━━
  Root cause: php-fpm pool exhaustion causing nginx 502
  Investigation path:
    web1: systemctl status nginx
    web1: systemctl status php-fpm
```

Multi-host investigations are tracked together — if you debug `web1` then check `db1` in the same session, future sessions on `web1` will see that the problem was linked to `db1`.
