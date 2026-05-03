# lily

[![Release](https://img.shields.io/github/v/release/aspectrr/lily?include_prereleases)](https://github.com/aspectrr/lily/releases) [![Built with GoReleaser](https://img.shields.io/badge/Built%20with-GoReleaser-868e5e4)](https://goreleaser.com)

A compiled Go binary that lets AI agents run **read-only** diagnostic commands on remote hosts via SSH — safely. Commands are validated against a strict allowlist before execution. Destructive operations (`rm`, `sudo`, `bash`, etc.) are hardcoded-blocked and cannot be overridden.

Works as both an **MCP server** (for AI agents) and a **CLI tool** (for humans).

## Install

### Binary (recommended)

```bash
# macOS (Apple Silicon)
curl -sL https://github.com/aspectrr/lily/releases/latest/download/lily_darwin_arm64.tar.gz | tar xz
sudo mv lily /usr/local/bin/

# macOS (Intel)
curl -sL https://github.com/aspectrr/lily/releases/latest/download/lily_darwin_amd64.tar.gz | tar xz
sudo mv lily /usr/local/bin/

# Linux (x86_64)
curl -sL https://github.com/aspectrr/lily/releases/latest/download/lily_linux_amd64.tar.gz | tar xz
sudo mv lily /usr/local/bin/
```

### Homebrew

```bash
brew install aspectrr/lily/lily
```

### From source
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

| Command                        | Description                                             |
| ------------------------------ | ------------------------------------------------------- |
| `serve`                        | Start MCP server on stdio (default if no command given) |
| `hosts`                        | List hosts from `~/.ssh/config`                         |
| `run <host> <command>`         | Execute a validated read-only command on a host         |
| `validate <command>`           | Check if a command is allowed without executing         |
| `check <host>`                 | Test SSH connectivity to a host                         |
| `list-commands`                | Show all allowed commands and subcommand restrictions   |
| `config-path`                  | Print the config file path                              |
| `validate-config`              | Validate the lily.yaml config file                      |
| `install-skill <agent\|all>`   | Install lily into an agent's MCP config                 |
| `uninstall-skill <agent\|all>` | Remove lily from an agent's MCP config                  |
| `list-agents`                  | Show detected agents that support MCP                   |
| `version`                      | Print version                                           |

### Flags

| Flag                  | Default                    | Description         |
| --------------------- | -------------------------- | ------------------- |
| `-config <path>`      | `~/.ssh/config`            | Path to SSH config  |
| `-config-file <path>` | `~/.config/lily/lily.yaml` | Path to lily config |
| `-timeout <duration>` | `30s`                      | SSH command timeout |

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
      "args": [
        "serve",
        "-timeout",
        "60s",
        "-config-file",
        "/etc/lily/lily.yaml"
      ]
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

`install-skill` writes the MCP config entry and deploys a default `lily.yaml` if one doesn't already exist.

---

## Execution Limits

Lily enforces configurable limits to prevent agents from abusing remote hosts:

| Setting     | Default       | Config Key         | Description                              |
| ----------- | ------------- | ------------------ | ---------------------------------------- |
| Rate limit  | 1 command/sec | `rate_limit`       | Minimum interval between commands        |
| Max output  | 1 MB          | `max_output_bytes` | Output cap per command (stdout + stderr) |
| SSH timeout | 30s           | `-timeout` flag    | Maximum execution time per command       |

Rate limiting applies to `run_command` and `check_host` only. Read-only tools like `list_hosts` and `validate_command` are not rate-limited.

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

### Host key verification

Lily uses **Trust On First Use (TOFU)** for SSH host key verification:

- **First connection** to a host → the host key is automatically recorded in `~/.ssh/known_hosts`
- **Subsequent connections** → the key is verified against the recorded value
- **Key mismatch** → the connection is **rejected** with a clear MITM warning

If `~/.ssh/known_hosts` doesn't exist, Lily creates it automatically. No manual setup required.

### ProxyJump (bastion / jump host)

Lily supports SSH ProxyJump for reaching hosts behind a bastion or jump server. This is the standard scenario where your local machine connects to a jump host, and from there tunnels to internal hosts that aren't directly reachable.

Configure it in `~/.ssh/config`:

```
Host bastion
    HostName 203.0.113.1
    User admin

Host web1
    HostName 10.0.0.5
    User deploy
    ProxyJump bastion

Host db1
    HostName 10.0.0.6
    ProxyJump bastion
```

Now `lily run web1 "systemctl status nginx"` automatically tunnels through bastion. The AI agent doesn't need to know about the proxy — it just targets `web1` as usual.

#### How it works

1. Lily reads `ProxyJump` from your SSH config
2. Dials the jump host directly
3. Opens an SSH tunnel through the jump host to the target
4. Runs the validated command on the target
5. All intermediate connections are kept alive and cleaned up together

#### Multi-hop chains

ProxyJump supports chaining through multiple hosts:

```
Host gateway
    HostName 203.0.113.1

Host bastion
    HostName 10.0.0.1
    ProxyJump gateway

Host web1
    HostName 192.168.1.5
    ProxyJump bastion
```

This creates a chain: **local → gateway → bastion → web1**. Lily resolves the chain recursively and detects loops.

You can also use comma-separated proxies (no recursive resolution):

```
Host web1
    HostName 10.0.0.5
    ProxyJump jump1,jump2
```

#### Authentication for proxy hops

Each hop in the chain uses its own SSH config (user, identity file, port). The SSH agent and default keys are tried for every hop, so if your agent has keys for both the bastion and the target, everything works automatically.

#### ProxyCommand is not supported

Lily only supports `ProxyJump` (SSH-native tunneling). `ProxyCommand` is **not supported** because it executes arbitrary local commands, which is a security risk. If your SSH config uses `ProxyCommand`, convert it to `ProxyJump`:

```diff
- ProxyCommand ssh -W %h:%p bastion
+ ProxyJump bastion
```

If a host has `ProxyCommand` but no `ProxyJump`, Lily prints a warning and attempts a direct connection (which will likely fail if the host truly requires a proxy).

#### Proxy display

The `list_hosts` tool and `lily hosts` command show which hosts use a proxy:

```
→ list_hosts()
← Found 3 host(s) in SSH config:
    bastion                   203.0.113.1 (user: admin)
    web1                      10.0.0.5 (user: deploy) [via bastion]
    db1                       10.0.0.6 [via bastion]
```

`check_host` also indicates the proxy:

```
→ check_host(host="web1")
← Host "web1" is reachable (via bastion).
```

### What runs on the remote

The validated command is sent over SSH and executed by the remote host's default shell. No agent, daemon, or restricted shell is installed on the remote — all safety enforcement happens client-side in the compiled binary.

---

## Configuration

### Config file

Location: `~/.config/lily/lily.yaml` (run `lily config-path` to find it)

The config file can only **add** to the base allowlist. The hardcoded blocklist (`rm`, `sudo`, `bash`, etc.) cannot be overridden — even if the YAML is edited to include them, they are silently ignored.

### Full config reference

```yaml
# ── Execution Limits ──────────────────────────────────────────────

# Minimum interval between command executions.
# Prevents agents from flooding hosts with rapid-fire commands.
# Default: "1s"
rate_limit: "1s"

# Maximum output (stdout + stderr) captured per command, in bytes.
# Output beyond this limit is silently truncated.
# Default: 1048576 (1 MB). Minimum: 1024 (1 KB).
max_output_bytes: 1048576

# ── Command Allowlist ────────────────────────────────────────────

# Add commands beyond the built-in allowlist.
# Still subject to metacharacter checks and subcommand restrictions.
extra_commands:
  - docker
  - kubectl

# Restrict which subcommands are allowed for extra commands.
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

# Block specific flags for any command (built-in or extra).
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
# Config valid.
#   Extra commands:     2
#   Extra restrictions: 2
#   Extra blocked flags: 2
#   Rate limit:         1s
#   Max output:         1048576 bytes
```

### Legacy config migration

The config file was renamed from `allowlist.yaml` to `lily.yaml` in v0.2.0. Lily automatically falls back to `~/.config/lily/allowlist.yaml` if `lily.yaml` doesn't exist, so existing setups continue to work without changes.

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

`date` `which` `type` `echo` `test`

### Text processing (in pipes)

`grep` `awk` `sed` (no `-i`) `sort` `uniq` `cut` `tr` `xargs`

### Subcommand restrictions

| Command     | Allowed subcommands                                                             |
| ----------- | ------------------------------------------------------------------------------- |
| `systemctl` | `status`, `show`, `list-units`, `is-active`, `is-enabled`                       |
| `dpkg`      | `-l`, `--list`, `-s`, `--status`                                                |
| `rpm`       | `-qa`, `-q`                                                                     |
| `apt`       | `list`, `show`                                                                  |
| `pip`       | `list`, `show`                                                                  |
| `openssl`   | `x509`, `verify`, `s_client`, `crl`, `version`, `ciphers`, `req` (display only) |
| `curl`      | GET requests only                                                               |

### Blocked flags

| Command | Blocked flags                                                                                                                                                                              |
| ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `sed`   | `-i`, `--in-place`                                                                                                                                                                         |
| `curl`  | `-X`, `-d`, `--data`, `--data-raw`, `--data-binary`, `--data-urlencode`, `-F`, `--form`, `-T`, `--upload-file`, `-o`, `--output`, `-O`, `--remote-name`, `-K`, `--config`, `-x`, `--proxy` |

---

## What's Always Blocked

These commands are hardcoded in the compiled binary and can **never** be allowed, even via user config:

| Category              | Commands                                                                                                                                                             |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Destructive           | `rm` `rmdir` `mv` `cp` `dd` `chmod` `chown` `chgrp` `chattr` `kill` `killall` `pkill` `shutdown` `reboot` `halt` `poweroff` `mkfs` `mount` `umount` `fdisk` `parted` |
| Privilege escalation  | `sudo` `su` `pkexec`                                                                                                                                                 |
| User management       | `useradd` `userdel` `usermod` `groupadd` `groupdel` `groupmod` `passwd` `chpasswd`                                                                                   |
| Shells / interpreters | `bash` `sh` `zsh` `dash` `csh` `tcsh` `fish` `python` `python3` `python2` `perl` `ruby` `node` `php` `lua`                                                           |
| Editors               | `vi` `vim` `nano` `emacs` `pico`                                                                                                                                     |
| File transfer         | `scp` `rsync` `sftp` `ftp` `wget`                                                                                                                                    |
| Build tools           | `make` `gcc` `g++` `cc`                                                                                                                                              |
| Firewalls             | `iptables` `ip6tables` `nft`                                                                                                                                         |
| Cron                  | `crontab` `at` `batch`                                                                                                                                               |

### Metacharacters (always blocked)

- Command substitution: `$(...)` and backticks
- Variable expansion: `$var`, `${var}`
- Output redirection: `>` and `>>`
- Input redirection: `<`
- Process substitution: `<(...)` and `>(...)`
- Subshells: `(...)` and `(...)`
- Newlines and carriage returns
- Environment variable assignments: `FOO=bar cmd`

---

## Security

Lily uses a 7-layer defense-in-depth pipeline:

1. **Metacharacter scanner** — blocks `$()`, backticks, `${}`, `$var`, `>`, `>>`, `<`, `<<`, `()`, newlines
2. **Redirection scanner** — blocks unquoted `>`
3. **Environment scanner** — blocks `VAR=value` assignments (prevents `LD_PRELOAD` injection)
4. **Pipeline splitter** — validates each segment independently
5. **Per-segment validation** — allowlist, blocklist, subcommand restrictions, blocked flags, argument validators
6. **Command sanitizer** — reconstructs the entire command with safe single-quoting
7. **SSH transport** — output caps, timeouts, Trust On First Use (TOFU) host key verification

For the full security model, threat analysis, and bypass vectors, see [SECURITY.md](SECURITY.md).

---

## Sandboxing

**Lily is designed to run inside a sandboxed environment** where the AI agent has no filesystem write access and no network access except via the Lily binary. Without sandboxing, an agent could bypass Lily and SSH directly, modify `~/.ssh/config`, or edit `lily.yaml`.

### macOS / Linux ARM64 — Shuru

[Shuru](https://shuru.run) provides lightweight Linux microVMs using Apple Virtualization.framework (macOS) and KVM (Linux ARM64). Sandboxes are **offline by default**.

#### Setup

```bash
# Install
brew tap superhq-ai/tap && brew install shuru   # macOS
# or: curl -fsSL https://shuru.run/install.sh | sh

# Create project config
cat > shuru.json << 'EOF'
{
  "cpus": 1,
  "memory": 512,
  "mounts": [
    "~/.ssh:/home/agent/.ssh:ro",
    "~/.config/lily:/home/agent/.config/lily:ro"
  ]
}
EOF

# Install Lily binary into sandbox
shuru run --mount ./bin/lily:/usr/local/bin/lily:ro -- lily hosts

# Run your agent inside the sandbox
shuru run --mount ./bin/lily:/usr/local/bin/lily:ro -- your-agent-command
```

#### Key restrictions

| Resource              | Setting                 | Why                            |
| --------------------- | ----------------------- | ------------------------------ |
| Network               | Offline (default)       | Agent can't SSH/curl directly  |
| `~/.ssh/`             | Read-only mount         | Agent can't modify SSH config  |
| `lily.yaml`           | Read-only mount         | Agent can't edit allowlist     |
| `/usr/local/bin/lily` | Read-only mount         | Agent can't replace the binary |
| Memory/CPU            | Minimal (512 MB, 1 CPU) | Contain resource usage         |

### Linux AMD64 — vmsan

[vmsan](https://vmsan.dev) provides Firecracker microVMs with per-VM network namespaces, seccomp-bpf filters, and cgroup resource limits.

#### Setup

```bash
# Install
curl -fsSL https://vmsan.dev/install | bash
vmsan doctor   # verify KVM, disk space, binaries

# Create isolated VM with no network access
vmsan create \
  --vcpus 1 \
  --memory 256 \
  --disk 5gb \
  --network-policy deny-all \
  --timeout 2h

# Get the VM ID
VM_ID=$(vmsan ls --json | jq -r '.[0].id')

# Upload Lily binary and config
vmsan upload "$VM_ID" ./bin/lily
vmsan exec "$VM_ID" -- mkdir -p /root/.config/lily
vmsan upload "$VM_ID" ./lily.yaml
vmsan exec "$VM_ID" -- mv /root/lily.yaml /root/.config/lily/lily.yaml

# Run the agent
vmsan exec "$VM_ID" -- your-agent-command
```

#### SSH-only network policy

To allow SSH to internal hosts while blocking everything else:

```bash
vmsan create \
  --vcpus 1 \
  --memory 256 \
  --network-policy custom \
  --allowed-cidr "10.0.0.0/8" \
  --allowed-cidr "192.168.0.0/16" \
  --denied-cidr "169.254.0.0/16" \
  --denied-cidr "100.100.0.0/16" \
  --timeout 2h
```

#### Key restrictions

| Resource      | Flag                        | Effect                          |
| ------------- | --------------------------- | ------------------------------- |
| Network       | `--network-policy deny-all` | No outbound from VM             |
| seccomp-bpf   | Enabled by default          | Restricts syscalls              |
| PID namespace | Enabled by default          | Process isolation               |
| cgroups       | Enabled by default          | Memory/CPU limits               |
| Timeout       | `--timeout 2h`              | Auto-shutdown after idle period |

### Recommended deployment

```
┌──────────────────────────────────────────────────────────────┐
│  Sandbox (shuru / vmsan)                                     │
│                                                              │
│  ┌─────────────┐    ┌──────────┐    ┌──────────────────┐   │
│  │  AI Agent   │───▶│ Lily CLI │───▶│ SSH to Remote    │   │
│  │             │    │          │    │ Hosts             │   │
│  │ No network  │    │ Validates│    │                  │   │
│  │ No SSH dir  │    │ Sanitizes│    │ Read-only cmds   │   │
│  │ No curl     │    │ Rate lim │    │ only             │   │
│  └─────────────┘    └──────────┘    └──────────────────┘   │
│         │                  │                                 │
│    read-only         read-only                              │
│    lily.yaml         ~/.ssh/                                │
│                                                              │
│  ❌ No raw SSH, curl, wget, nc                              │
│  ❌ No write access to ~/.ssh/ or lily.yaml                 │
│  ❌ No outbound network (except via Lily SSH)               │
└──────────────────────────────────────────────────────────────┘
```

---

## Architecture

```
lily/
├── cmd/lily/main.go           # CLI entry point + all subcommands
├── internal/
│   ├── allowlist/              # YAML config parsing + execution limits
│   │   ├── allowlist.go        #   Config struct, loading, defaults
│   │   └── allowlist_test.go
│   ├── install/                # Agent config install/uninstall
│   │   ├── install.go          #   MCP config writing, config deployment
│   │   └── install_test.go
│   ├── mcp/                    # MCP server + 5 tools + rate limiter
│   │   ├── server.go           #   Tool handlers, rate limiting
│   │   └── server_test.go
│   ├── readonly/               # Command validation engine
│   │   ├── validate.go         #   7-layer validation pipeline
│   │   └── validate_test.go    #   75+ attack vector regression tests
│   ├── sshconfig/              # SSH config parser
│   │   ├── sshconfig.go        #   Host, ProxyJump, ProxyCommand parsing
│   │   └── sshconfig_test.go
│   └── sshexec/                # SSH execution (direct + ProxyJump)
│       ├── sshexec.go          #   Executor with output caps, proxy chains
│       └── sshexec_test.go
├── lily.yaml                   # Example config
├── SECURITY.md                 # Full security model & sandboxing guide
├── SKILL.md                    # Agent skill reference
├── AGENTS.md                   # Agent integration guide
├── Makefile
└── README.md
```

## Why Go?

The compiled binary prevents the AI agent from inspecting or modifying the validation logic at runtime. A Python-based solution could be read by the agent, who could then edit the source to bypass restrictions. Go produces a single static binary with no runtime dependencies.

---

## Development

```bash
make build      # Build to bin/lily
make test       # Run all tests (75 tests across 7 packages)
make all        # Test + build
make install    # Build + copy to /usr/local/bin
make install-go # Install via go install
make fmt        # Format code
make vet        # Run go vet
make clean      # Remove bin/
```
