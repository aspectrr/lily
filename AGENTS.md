# Lily MCP - Agent Reference

## What is this?

Lily MCP is a read-only remote command execution server for AI agents. It allows you to run diagnostic commands on remote hosts via SSH, with strict validation that prevents destructive operations.

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

| Command | Allowed |
|---------|---------|
| systemctl | status, show, list-units, is-active, is-enabled |
| dpkg | -l, --list, -s, --status |
| rpm | -qa, -q |
| apt | list, show |
| pip | list, show |
| openssl | x509, verify, s_client, crl, version, ciphers, req (display only) |
| curl | GET requests only (no -X, -d, -F, -T, -o, -O, --proxy) |

### What's Always Blocked

These can **never** be allowed, even via user config:
- Destructive: rm, mv, cp, dd, chmod, chown, kill, shutdown, reboot
- Privilege escalation: sudo, su, pkexec
- Shells/interpreters: bash, sh, zsh, python, perl, ruby, node, php
- Editors: vi, vim, nano, emacs
- File transfer: scp, rsync, sftp, wget
- Package mutation: apt install/remove, pip install/uninstall, dpkg -i
- Metacharacters: `$(...)`, backticks, `>`, `>>`, `<(...)`, `>(...)`, newlines

## User Configuration

Users can extend the allowlist via YAML config at `~/.config/lily/allowlist.yaml`.

```yaml
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

## Architecture

```
lily/
├── cmd/lily/main.go           # CLI entry point
├── internal/
│   ├── allowlist/                  # YAML config parsing
│   ├── mcp/                        # MCP server & tools
│   ├── readonly/                   # Command validation engine
│   ├── sshconfig/                  # SSH config parser
│   └── sshexec/                    # SSH execution
├── allowlist.yaml                  # Example config
├── Makefile
└── README.md
```

## Development

```bash
make build      # Build to bin/lily
make test       # Run all tests
make install    # Install to /usr/local/bin
```
