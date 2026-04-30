# lily

A compiled Go binary that lets AI agents run **read-only** diagnostic commands on remote hosts via SSH — safely. Commands are validated against a strict allowlist before execution. Destructive operations (`rm`, `sudo`, `bash`, etc.) are hardcoded-blocked and cannot be overridden.

Works as both an **MCP server** (for AI agents) and a **CLI tool** (for humans).

## Install

```bash
# From source (any Go version 1.23+)
go install github.com/aspectrr/lily/cmd/lily@latest

# Or clone and build
git clone https://github.com/aspectrr/lily.git
cd lily
make build
sudo make install   # copies bin/lily to /usr/local/bin
```

## Quick Start

```bash
# List hosts from your SSH config
lily hosts

# Check connectivity
lily check web1

# Run a diagnostic command
lily run web1 "systemctl status nginx"

# Install into your AI agent
lily install-skill claude-code
```

---

## CLI Reference

```
lily [flags]
lily <command> [arguments]
```

### Commands

| Command | Description |
|---------|-------------|
| `serve` | Start MCP server on stdio (default if no command given) |
| `hosts` | List hosts from `~/.ssh/config` |
| `run <host> <command>` | Execute a validated read-only command on a host |
| `validate <command>` | Check if a command is allowed without executing |
| `check <host>` | Test SSH connectivity to a host |
| `list-commands` | Show all allowed commands and subcommand restrictions |
| `config-path` | Print the allowlist config file path |
| `validate-config` | Validate the allowlist YAML file |
| `install-skill <agent\|all>` | Install lily into an agent's MCP config |
| `uninstall-skill <agent\|all>` | Remove lily from an agent's MCP config |
| `list-agents` | Show detected agents that support MCP |
| `version` | Print version |

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-config <path>` | `~/.ssh/config` | Path to SSH config |
| `-allowlist <path>` | `~/.config/lily/allowlist.yaml` | Path to allowlist YAML |
| `-timeout <duration>` | `30s` | SSH command timeout |

---

## MCP Server

### What it does

When you run `lily serve` (or just `lily`), it starts an MCP server on stdio that exposes 5 tools to the connected AI agent:

### Tools

#### `list_hosts`
Discover hosts from SSH config.

```
→ list_hosts()
← Found 2 host(s) in SSH config:
    web1    192.168.1.10 (user: deploy)
    db1     db.example.com (user: postgres)
```

#### `check_host`
Test SSH connectivity before running commands.

```
→ check_host(host="web1")
← Host "web1" is reachable.
```

#### `validate_command`
Check whether a command would be allowed without executing it.

```
→ validate_command(command="rm -rf /")
← BLOCKED: command "rm" is not allowed in read-only mode
```

#### `run_command`
Execute a validated read-only command on a remote host. The command is checked against the full allowlist before execution. If it fails validation, the command is never sent to the host.

```
→ run_command(host="web1", command="journalctl -u nginx --no-pager -n 50")
← Apr 30 00:12:03 web1 systemd[1]: Started nginx.service...
```

#### `list_allowed_commands`
Show all currently allowed commands including user-configured additions.

### How to connect

Add to your agent's MCP configuration:

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

With custom flags:

```json
{
  "mcpServers": {
    "lily": {
      "command": "/usr/local/bin/lily",
      "args": ["serve", "-timeout", "60s", "-allowlist", "/etc/lily/allowlist.yaml"]
    }
  }
}
```

### One-command install into agents

```bash
lily list-agents          # See which agents are detected
lily install-skill all    # Install to all detected agents
lily install-skill claude-code    # Specific agent
lily uninstall-skill cursor       # Remove from an agent
```

Supported agents: **Claude Code**, **Claude Desktop**, **Cursor**, **Windsurf**, **Cline**, **Pi**, **Goose**

`install-skill` writes the MCP config entry and deploys a default `allowlist.yaml` if one doesn't already exist.

---

## SSH Execution

### How hosts are discovered

lily reads `~/.ssh/config` to find available hosts. Only hosts explicitly defined in SSH config can be accessed — the agent cannot connect to arbitrary hosts.

```
Host web1
    HostName 192.168.1.10
    User deploy
    Port 2222
    IdentityFile ~/.ssh/web1_key

Host db1
    HostName db.example.com
    User postgres
```

### Authentication

lily uses the user's existing SSH credentials:

1. **SSH agent** (`SSH_AUTH_SOCK`) — loaded keys are tried first
2. **IdentityFile** — if specified in SSH config for the host
3. **Default keys** — `~/.ssh/id_ed25519`, `~/.ssh/id_ecdsa`, `~/.ssh/id_rsa`

No special setup or server-side installation is needed. If you can `ssh web1` from your terminal, lily can too.

### What runs on the remote

The validated command is sent over SSH and executed by the remote host's default shell. No agent, daemon, or restricted shell is installed on the remote — all safety enforcement happens client-side in the compiled binary.

---

## Allowlist Configuration

### Config file

Location: `~/.config/lily/allowlist.yaml` (run `lily config-path` to find it)

The config file can only **add** to the base allowlist. The hardcoded blocklist (`rm`, `sudo`, `bash`, etc.) cannot be overridden — even if the YAML is edited to include them, they are silently ignored.

### Example config

```yaml
# Add commands beyond the built-in allowlist
extra_commands:
  - docker
  - kubectl

# Restrict which subcommands are allowed for extra commands
extra_subcommand_restrictions:
  docker:
    - ps
    - logs
    - inspect
    - stats
    - top
    - events
    - images
    - network ls
    - volume ls
  kubectl:
    - get
    - describe
    - logs
    - top
    - explain
    - api-resources
    - api-versions

# Block specific flags for extra commands
extra_blocked_flags:
  docker:
    - exec
    - run
    - rm
    - rmi
    - build
    - push
    - pull
  kubectl:
    - delete
    - apply
    - create
    - edit
    - patch
    - replace
```

### Validate your config

```bash
lily validate-config
# Config valid. Extra commands: 2, Extra restrictions: 2, Extra blocked flags: 2
```

---

## Allowed Commands

Run `lily list-commands` for the full list.

### File inspection
`cat` `ls` `find` `head` `tail` `stat` `file` `wc` `du` `tree` `strings` `md5sum` `sha256sum` `readlink` `realpath` `basename` `dirname` `base64`

### Process / System
`ps` `top` `pgrep` `systemctl` (read-only) `journalctl` `dmesg`

### Network
`ss` `netstat` `ip` `ifconfig` `dig` `nslookup` `ping` `curl` (GET only) `openssl` (read-only)

### Disk
`df` `lsblk` `blkid`

### Package query
`dpkg` (list/status) `rpm` (query) `apt` (list/show) `pip` (list/show)

### System info
`uname` `hostname` `uptime` `free` `lscpu` `lsmod` `lspci` `lsusb` `arch` `nproc`

### User info
`whoami` `id` `groups` `who` `w` `last`

### Misc
`env` `printenv` `date` `which` `type` `echo` `test`

### Text processing (in pipes)
`grep` `awk` `sed` (no `-i`) `sort` `uniq` `cut` `tr` `xargs`

### Subcommand restrictions

| Command | Allowed subcommands |
|---------|-------------------|
| `systemctl` | `status`, `show`, `list-units`, `is-active`, `is-enabled` |
| `dpkg` | `-l`, `--list`, `-s`, `--status` |
| `rpm` | `-qa`, `-q` |
| `apt` | `list`, `show` |
| `pip` | `list`, `show` |
| `openssl` | `x509`, `verify`, `s_client`, `crl`, `version`, `ciphers`, `req` (display only) |
| `curl` | GET requests only |

### Blocked flags

| Command | Blocked flags |
|---------|--------------|
| `sed` | `-i`, `--in-place` |
| `curl` | `-X`, `-d`, `--data`, `--data-raw`, `--data-binary`, `--data-urlencode`, `-F`, `--form`, `-T`, `--upload-file`, `-o`, `--output`, `-O`, `--remote-name`, `-K`, `--config`, `-x`, `--proxy` |

---

## What's Always Blocked

These commands are hardcoded in the compiled binary and can **never** be allowed, even via user config:

| Category | Commands |
|----------|----------|
| Destructive | `rm` `rmdir` `mv` `cp` `dd` `chmod` `chown` `chgrp` `chattr` `kill` `killall` `pkill` `shutdown` `reboot` `halt` `poweroff` `mkfs` `mount` `umount` `fdisk` `parted` |
| Privilege escalation | `sudo` `su` `pkexec` |
| User management | `useradd` `userdel` `usermod` `groupadd` `groupdel` `groupmod` `passwd` `chpasswd` |
| Shells / interpreters | `bash` `sh` `zsh` `dash` `csh` `tcsh` `fish` `python` `python3` `python2` `perl` `ruby` `node` `php` `lua` |
| Editors | `vi` `vim` `nano` `emacs` `pico` |
| File transfer | `scp` `rsync` `sftp` `ftp` `wget` |
| Build tools | `make` `gcc` `g++` `cc` |
| Firewalls | `iptables` `ip6tables` `nft` |
| Cron | `crontab` `at` `batch` |

### Metacharacters (always blocked)

- Command substitution: `$(...)` and backticks
- Output redirection: `>` and `>>`
- Process substitution: `<(...)` and `>(...)`
- Newlines and carriage returns

---

## Why Go?

The compiled binary prevents the AI agent from inspecting or modifying the validation logic at runtime. A Python-based solution could be read by the agent, who could then edit the source to bypass restrictions. Go produces a single static binary with no runtime dependencies.

---

## Development

```bash
make build      # Build to bin/lily
make test       # Run all tests
make all        # Test + build
make install    # Build + copy to /usr/local/bin
make install-go # Install via go install
make fmt        # Format code
make vet        # Run go vet
make clean      # Remove bin/
```

### Running tests

```bash
make test   # 47 tests across 5 packages
```

### Project structure

```
lily/
├── cmd/lily/main.go           # CLI entry point + all subcommands
├── internal/
│   ├── allowlist/                  # YAML config parsing + defaults
│   │   ├── allowlist.go
│   │   └── allowlist_test.go
│   ├── install/                    # Agent config install/uninstall
│   │   ├── install.go
│   │   └── install_test.go
│   ├── mcp/                        # MCP server + 5 tools
│   │   ├── server.go
│   │   └── server_test.go
│   ├── readonly/                   # Command validation engine
│   │   ├── validate.go
│   │   └── validate_test.go
│   ├── sshconfig/                  # SSH config parser
│   │   ├── sshconfig.go
│   │   └── sshconfig_test.go
│   └── sshexec/                    # SSH execution (agent/key auth)
│       ├── sshexec.go
│       └── sshexec_test.go
├── allowlist.yaml                  # Example allowlist config
├── SKILL.md                        # Agent skill reference
├── AGENTS.md                       # Agent integration guide
├── Makefile
└── README.md
```
