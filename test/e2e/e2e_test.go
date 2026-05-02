package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aspectrr/lily/internal/allowlist"
	"github.com/aspectrr/lily/internal/mcp"
	"github.com/aspectrr/lily/internal/readonly"
	"github.com/aspectrr/lily/internal/sshconfig"
	"github.com/aspectrr/lily/internal/sshexec"
)

// ============================================================================
// Lima VM management (singleton — VMs are created once per test process)
// ============================================================================

const (
	instanceBastion  = "lily-e2e-bastion"
	instanceTarget   = "lily-e2e-target"
	vmStartupTimeout = 5 * time.Minute
)

// limaAlpineYAML is a minimal Lima config using vz (Apple hypervisor).
// Falls back to qemu on x86_64 macOS or non-macOS.
const limaAlpineYAML = `
vmType: vz
arch: default
images:
- location: "https://dl-cdn.alpinelinux.org/alpine/v3.23/releases/cloud/nocloud_alpine-3.23.3-aarch64-uefi-cloudinit-r0.qcow2"
  arch: "aarch64"
  digest: "sha512:7a3cdfaefb0cbf3bb6824cd6ae80d6a3e0b0e367609e5fc50c5f714374d31e8d70dd607094a4e9cfe2e6ead7537781782469fe93bcea79611fbce4ddeacc92e1"
- location: "https://dl-cdn.alpinelinux.org/alpine/v3.23/releases/cloud/nocloud_alpine-3.23.3-x86_64-uefi-cloudinit-r0.qcow2"
  arch: "x86_64"
  digest: "sha512:9b61666394dcbeb65d3b3ab0864f5ab6c992c32094622333c5b37b83021fd359cc41eb3fa00abf5cdbe25ee2cef600577a7b23eb6f1a103cc32aff862ef88862"
cpus: 2
memory: 512MiB
disk: 5GiB
mounts: []
networks:
- lima:shared
containerd:
  system: false
  user: false
provision:
- mode: system
  script: |
    #!/bin/sh
    set -eux
    apk add --no-cache util-linux procps-ng coreutils findutils diffutils file iproute2-ss
ssh:
  localPort: 0
  loadDotSSHPubKeys: true
`

// testEnv holds the shared test environment.
type testEnv struct {
	hosts     []sshconfig.Host
	exec      *sshexec.Executor
	validator *readonly.Validator
	tmpDir    string
}

var (
	globalEnv     *testEnv
	globalEnvOnce sync.Once
	globalEnvErr  error
)

// getTestEnv returns the singleton test environment. VMs are created once.
func getTestEnv(t *testing.T) *testEnv {
	t.Helper()

	globalEnvOnce.Do(func() {
		globalEnv, globalEnvErr = createTestEnv()
	})
	if globalEnvErr != nil {
		t.Fatalf("failed to create test environment: %v", globalEnvErr)
	}
	return globalEnv
}

func createTestEnv() (*testEnv, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("e2e tests require macOS with Lima (Apple hypervisor)")
	}
	if _, err := exec.LookPath("limactl"); err != nil {
		return nil, fmt.Errorf("limactl not found — install Lima: brew install lima")
	}

	tmpDir, err := os.MkdirTemp("", "lily-e2e-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	te := &testEnv{
		tmpDir:    tmpDir,
		validator: readonly.DefaultValidator(),
	}

	// Start both VMs.
	if err := te.startVM(instanceTarget); err != nil {
		return nil, fmt.Errorf("start target VM: %w", err)
	}
	if err := te.startVM(instanceBastion); err != nil {
		return nil, fmt.Errorf("start bastion VM: %w", err)
	}

	// Parse SSH configs.
	te.hosts, err = te.buildHosts()
	if err != nil {
		return nil, fmt.Errorf("build hosts: %w", err)
	}

	te.exec = sshexec.NewExecutor(te.hosts, 30*time.Second, sshexec.DefaultMaxOutputBytes)

	// Seed test data.
	if err := te.seedTestData(); err != nil {
		return nil, fmt.Errorf("seed test data: %w", err)
	}

	return te, nil
}

// startVM ensures a Lima VM instance is running. If the instance already exists,
// it starts it. If not, it creates and starts it.
func (te *testEnv) startVM(name string) error {
	// Check if instance already exists.
	listCmd := limaCmd("list", "--format", "{{.Name}}")
	output, err := listCmd.Output()
	if err == nil && strings.Contains(string(output), name) {
		// Instance exists — make sure it's running.
		startCmd := limaCmd("start", "--tty=false", name)
		startCmd.Stdout = os.Stderr
		startCmd.Stderr = os.Stderr
		ctx, cancel := context.WithTimeout(context.Background(), vmStartupTimeout)
		defer cancel()
		runCmd := exec.CommandContext(ctx, startCmd.Args[0], startCmd.Args[1:]...)
		runCmd.Stdout = os.Stderr
		runCmd.Stderr = os.Stderr
		if err := runCmd.Run(); err != nil {
			return fmt.Errorf("limactl start %s: %w", name, err)
		}
		return nil
	}

	// Instance doesn't exist — create it.
	// First clean up any stale state.
	_ = limaCmd("stop", name).Run()
	_ = limaCmd("delete", name).Run()

	configPath := filepath.Join(te.tmpDir, name+".yaml")
	if err := os.WriteFile(configPath, []byte(limaAlpineYAML), 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Create.
	createCmd := limaCmd("create", "--name", name, "--tty=false", configPath)
	createCmd.Stdout = os.Stderr
	createCmd.Stderr = os.Stderr
	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("limactl create %s: %w", name, err)
	}

	// Start.
	ctx, cancel := context.WithTimeout(context.Background(), vmStartupTimeout)
	defer cancel()
	startCmd := exec.CommandContext(ctx, "limactl", "start", "--tty=false", name)
	startCmd.Stdout = os.Stderr
	startCmd.Stderr = os.Stderr
	if err := startCmd.Run(); err != nil {
		return fmt.Errorf("limactl start %s: %w", name, err)
	}

	return nil
}

func (te *testEnv) buildHosts() ([]sshconfig.Host, error) {
	bastion, err := parseLimaSSHConfig(instanceBastion)
	if err != nil {
		return nil, fmt.Errorf("parse bastion SSH config: %w", err)
	}
	target, err := parseLimaSSHConfig(instanceTarget)
	if err != nil {
		return nil, fmt.Errorf("parse target SSH config: %w", err)
	}

	// Discover the target's IP on the lima:shared network (192.168.105.0/24).
	// This is needed because the bastion VM cannot reach 127.0.0.1:<target_port>
	// from inside its own VM — it needs the shared-network IP instead.
	targetSharedIP, err := getLimaSharedIP(instanceTarget)
	if err != nil {
		return nil, fmt.Errorf("discover target shared IP: %w", err)
	}

	return []sshconfig.Host{
		{
			// Direct connection to bastion via host-side forwarded port.
			Host: "lily-bastion", Names: []string{"lily-bastion"},
			HostName: bastion.HostName, User: bastion.User,
			Port: bastion.Port, IdentityFile: bastion.IdentityFile,
		},
		{
			// Proxied connection: host -> bastion -> target (via shared network).
			// Uses the target's shared-network IP so the bastion can reach it.
			Host: "lily-target", Names: []string{"lily-target"},
			HostName: targetSharedIP, User: target.User,
			Port: "22", IdentityFile: target.IdentityFile,
			ProxyJump: "lily-bastion",
		},
		{
			// Direct connection to target via host-side forwarded port.
			Host: "lily-target-direct", Names: []string{"lily-target-direct"},
			HostName: target.HostName, User: target.User,
			Port: target.Port, IdentityFile: target.IdentityFile,
		},
	}, nil
}

// getLimaSharedIP returns the IP address of a Lima instance on the shared network.
func getLimaSharedIP(instanceName string) (string, error) {
	// limactl shell runs a command inside the VM.
	cmd := limaCmd("shell", instanceName, "ip", "-4", "addr", "show", "dev", "eth0")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("get IP from %s: %w", instanceName, err)
	}

	// Parse output like: inet 192.168.105.42/24 brd 192.168.105.255 scope global eth0
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "inet ") && strings.Contains(line, "192.168.") {
			parts := strings.Fields(line)
			for _, p := range parts {
				if strings.HasPrefix(p, "192.168.") {
					ip := strings.SplitN(p, "/", 2)[0]
					return ip, nil
				}
			}
		}
	}
	return "", fmt.Errorf("no 192.168.x.x address found for %s", instanceName)
}

func parseLimaSSHConfig(instanceName string) (sshconfig.Host, error) {
	configPath := filepath.Join(os.Getenv("HOME"), ".lima", instanceName, "ssh.config")
	hosts, err := sshconfig.Parse(configPath)
	if err != nil {
		return sshconfig.Host{}, fmt.Errorf("parse %s: %w", configPath, err)
	}
	for _, h := range hosts {
		if strings.HasPrefix(h.Host, "lima-") {
			return h, nil
		}
	}
	if len(hosts) > 0 {
		return hosts[0], nil
	}
	return sshconfig.Host{}, fmt.Errorf("no hosts in %s", configPath)
}

func (te *testEnv) seedTestData() error {
	ctx := context.Background()
	h := "lily-target-direct"

	commands := []string{
		"mkdir -p /tmp/lily-test",
		"echo 'Hello from Lily E2E' > /tmp/lily-test/hello.txt",
		"printf 'line1\\nline2\\nline3\\nline4\\nline5\\n' > /tmp/lily-test/multiline.txt",
		"printf 'apple\\nbanana\\ncherry\\napple\\ndate\\n' > /tmp/lily-test/fruits.txt",
		"dd if=/dev/urandom bs=1024 count=2048 2>/dev/null | base64 > /tmp/lily-test/large.txt",
		"mkdir -p /tmp/lily-test/deep/nested/dir",
		"echo 'secret' > /tmp/lily-test/deep/nested/dir/secret.conf",
	}

	for _, cmd := range commands {
		result, err := te.exec.Run(ctx, h, cmd)
		if err != nil {
			return fmt.Errorf("run %q: %w", cmd, err)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("run %q: exit %d: %s", cmd, result.ExitCode, result.Stderr)
		}
	}
	return nil
}

// ============================================================================
// Helpers
// ============================================================================

func limaCmd(args ...string) *exec.Cmd { return exec.Command("limactl", args...) }

func runOK(t *testing.T, ctx context.Context, ex *sshexec.Executor, host, cmd string) *sshexec.Result {
	t.Helper()
	result, err := ex.Run(ctx, host, cmd)
	if err != nil {
		t.Fatalf("run %q on %s: %v", cmd, host, err)
	}
	return result
}

// ============================================================================
// Test suite
// ============================================================================

func TestMain(m *testing.M) {
	if os.Getenv("LILY_E2E") == "" {
		fmt.Fprintln(os.Stderr, "Skipping E2E tests. Set LILY_E2E=1 to run them.")
		os.Exit(0)
	}

	code := m.Run()

	// Cleanup VMs after all tests.
	for _, name := range []string{instanceBastion, instanceTarget} {
		_ = limaCmd("stop", name).Run()
		_ = limaCmd("delete", name).Run()
	}

	os.Exit(code)
}

// ─── Connectivity ───────────────────────────────────────────────────────

func TestDirectConnectivity(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()

	result, err := te.exec.Run(ctx, "lily-target-direct", "echo ok")
	if err != nil {
		t.Fatalf("direct SSH failed: %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "ok" {
		t.Errorf("expected 'ok', got %q", result.Stdout)
	}
}

func TestProxyJumpConnectivity(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()

	result, err := te.exec.Run(ctx, "lily-target", "echo hello-via-proxy")
	if err != nil {
		t.Fatalf("ProxyJump SSH failed: %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "hello-via-proxy" {
		t.Errorf("expected 'hello-via-proxy', got %q", result.Stdout)
	}
}

// ─── File reading ───────────────────────────────────────────────────────

func TestReadFile(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "cat /tmp/lily-test/hello.txt")
	if !strings.Contains(result.Stdout, "Hello from Lily E2E") {
		t.Errorf("expected hello content, got %q", result.Stdout)
	}
}

func TestLs(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "ls /tmp/lily-test")
	for _, name := range []string{"hello.txt", "multiline.txt", "fruits.txt", "large.txt", "deep"} {
		if !strings.Contains(result.Stdout, name) {
			t.Errorf("ls missing %q: %s", name, result.Stdout)
		}
	}
}

func TestFind(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "find /tmp/lily-test -name '*.conf'")
	if !strings.Contains(result.Stdout, "secret.conf") {
		t.Errorf("find didn't locate secret.conf: %s", result.Stdout)
	}
}

func TestHead(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "head -n 2 /tmp/lily-test/multiline.txt")
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %q", len(lines), result.Stdout)
	}
}

func TestTail(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "tail -n 2 /tmp/lily-test/multiline.txt")
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %q", len(lines), result.Stdout)
	}
}

func TestStat(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "stat /tmp/lily-test/hello.txt")
	if !strings.Contains(result.Stdout, "File:") && !strings.Contains(result.Stdout, "Size:") {
		t.Errorf("unexpected stat output: %s", result.Stdout)
	}
}

func TestWc(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "wc -l /tmp/lily-test/fruits.txt")
	if !strings.Contains(result.Stdout, "5") {
		t.Errorf("expected 5 lines: %s", result.Stdout)
	}
}

func TestDu(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "du -sh /tmp/lily-test")
	if !strings.Contains(result.Stdout, "/tmp/lily-test") {
		t.Errorf("unexpected du output: %s", result.Stdout)
	}
}

// ─── System info ────────────────────────────────────────────────────────

func TestUname(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "uname -a")
	if !strings.Contains(result.Stdout, "Linux") {
		t.Errorf("expected Linux: %s", result.Stdout)
	}
}

func TestHostname(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "hostname")
	if strings.TrimSpace(result.Stdout) == "" {
		t.Error("hostname returned empty")
	}
}

func TestUptime(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "uptime")
	if !strings.Contains(result.Stdout, "up") {
		t.Errorf("unexpected uptime: %s", result.Stdout)
	}
}

func TestWhoami(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "whoami")
	if strings.TrimSpace(result.Stdout) == "" {
		t.Error("whoami empty")
	}
}

func TestFree(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "free")
	if !strings.Contains(result.Stdout, "Mem:") {
		t.Errorf("unexpected free: %s", result.Stdout)
	}
}

// ─── Process & system ───────────────────────────────────────────────────

func TestPs(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "ps aux")
	if !strings.Contains(result.Stdout, "sshd") {
		t.Errorf("ps should show sshhd: %s", result.Stdout)
	}
}

func TestDmesg(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result, err := te.exec.Run(ctx, "lily-target-direct", "dmesg | head -n 5")
	if err != nil {
		t.Fatalf("dmesg failed: %v", err)
	}
	t.Logf("dmesg: %s", result.Stdout)
}

// ─── Network ────────────────────────────────────────────────────────────

func TestSs(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "ss -tlnp")
	if !strings.Contains(result.Stdout, "22") {
		t.Errorf("ss should show port 22: %s", result.Stdout)
	}
}

// ─── Disk ───────────────────────────────────────────────────────────────

func TestDf(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "df -h")
	if !strings.Contains(result.Stdout, "/") {
		t.Errorf("unexpected df: %s", result.Stdout)
	}
}

// ─── User ───────────────────────────────────────────────────────────────

func TestId(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct", "id")
	if !strings.Contains(result.Stdout, "uid=") {
		t.Errorf("unexpected id: %s", result.Stdout)
	}
}

// ─── Pipelines ──────────────────────────────────────────────────────────

func TestPipeline(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct",
		"cat /tmp/lily-test/fruits.txt | grep apple | sort | uniq -c")
	if !strings.Contains(result.Stdout, "2") || !strings.Contains(result.Stdout, "apple") {
		t.Errorf("pipeline should count 2 apples: %q", result.Stdout)
	}
}

func TestPipelineWithAwk(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target-direct",
		"printf 'alice 100\\nbob 200\\ncharlie 150\\n' | awk '{print $2}' | sort -n")
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 3 || lines[0] != "100" {
		t.Errorf("awk pipeline unexpected: %q", result.Stdout)
	}
}

// ─── Output truncation ──────────────────────────────────────────────────

func TestOutputTruncation(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()

	smallLimit := int64(512)
	exec := sshexec.NewExecutor(te.hosts, 30*time.Second, smallLimit)

	result, err := exec.Run(ctx, "lily-target-direct", "cat /tmp/lily-test/large.txt")
	if err != nil {
		t.Fatalf("run large file: %v", err)
	}
	if !result.Truncated {
		t.Error("expected output to be truncated")
	}
	if len(result.Stdout) > int(smallLimit) {
		t.Errorf("output %d bytes exceeds limit %d", len(result.Stdout), smallLimit)
	}
	t.Logf("Truncated=%v, size=%d", result.Truncated, len(result.Stdout))
}

// ─── Rate limiting ──────────────────────────────────────────────────────

func TestRateLimiting(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()

	// Verify executor can burst (no rate limiter on executor itself).
	exec := sshexec.NewExecutor(te.hosts, 30*time.Second, sshexec.DefaultMaxOutputBytes)
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := exec.Run(ctx, "lily-target-direct", fmt.Sprintf("echo burst-%d", i)); err != nil {
			t.Fatalf("burst %d: %v", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("burst took too long: %v", elapsed)
	}

	// Verify default rate limit config.
	defaultCfg := &allowlist.Config{}
	if got := defaultCfg.GetRateLimit(); got != 1*time.Second {
		t.Errorf("default rate limit = %v, want 1s", got)
	}

	// Verify custom rate limit config.
	cfg := &allowlist.Config{RateLimit: "500ms"}
	if got := cfg.GetRateLimit(); got != 500*time.Millisecond {
		t.Errorf("custom rate limit = %v, want 500ms", got)
	}
}

// ─── Security: blocked commands (local — no VM needed) ──────────────────

func TestBlockedCommands(t *testing.T) {
	v := readonly.DefaultValidator()

	blocked := []struct {
		name string
		cmd  string
	}{
		{"rm", "rm -rf /"},
		{"sudo", "sudo whoami"},
		{"bash", "bash -c 'echo pwned'"},
		{"python", "python3 -c 'print(1)'"},
		{"chmod", "chmod 777 /etc/passwd"},
		{"shutdown", "shutdown -h now"},
		{"reboot", "reboot"},
		{"kill", "kill -9 1"},
		{"dd", "dd if=/dev/zero of=/tmp/zerofile"},
		{"mv", "mv /tmp/lily-test/hello.txt /tmp/lily-test/moved.txt"},
		{"cp", "cp /tmp/lily-test/hello.txt /tmp/lily-test/copied.txt"},
		{"wget", "wget http://example.com"},
		{"scp", "scp /tmp/file user@host:/tmp"},
		{"vi", "vi /tmp/lily-test/hello.txt"},
		{"subshell", "(echo test)"},
		{"var_expansion", "echo $HOME"},
		{"command_substitution", "echo $(whoami)"},
		{"backtick", "echo `whoami`"},
		{"redirect_out", "echo test > /tmp/out.txt"},
		{"input_redirect", "cat < /tmp/lily-test/hello.txt"},
		{"process_sub", "cat <(echo test)"},
		{"newline", "echo a\necho b"},
		{"env_var", "FOO=bar echo test"},
		{"here_doc", "cat << EOF\nhello\nEOF"},
	}

	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			err := v.ValidateCommand(tc.cmd)
			if err == nil {
				t.Errorf("command %q should be blocked", tc.cmd)
			}
		})
	}
}

// ─── Security: blocked commands over SSH ────────────────────────────────

func TestBlockedCommandsOverSSH(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()

	dangerous := []string{
		"rm -rf /tmp/lily-test",
		"sudo rm -rf /",
		"bash -c 'cat /etc/shadow'",
		"echo hacked > /tmp/lily-test/pwned.txt",
	}
	for _, cmd := range dangerous {
		if err := te.validator.ValidateCommand(cmd); err == nil {
			t.Errorf("command %q should have been blocked", cmd)
		}
	}

	// Verify test data is intact.
	result := runOK(t, ctx, te.exec, "lily-target-direct", "cat /tmp/lily-test/hello.txt")
	if !strings.Contains(result.Stdout, "Hello from Lily E2E") {
		t.Error("test data was corrupted — blocked commands leaked through")
	}
}

// ─── Allowed commands over SSH ──────────────────────────────────────────

func TestAllowedCommandsOverSSH(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()

	allowed := []struct {
		name string
		cmd  string
	}{
		{"echo", "echo hello"},
		{"cat", "cat /tmp/lily-test/hello.txt"},
		{"ls", "ls /tmp/lily-test"},
		{"head", "head -n 1 /tmp/lily-test/multiline.txt"},
		{"tail", "tail -n 1 /tmp/lily-test/multiline.txt"},
		{"wc", "wc -l /tmp/lily-test/fruits.txt"},
		{"find", "find /tmp/lily-test -type f"},
		{"grep", "grep apple /tmp/lily-test/fruits.txt"},
		{"sort", "sort /tmp/lily-test/fruits.txt"},
		{"uname", "uname -a"},
		{"hostname", "hostname"},
		{"whoami", "whoami"},
		{"id", "id"},
		{"uptime", "uptime"},
		{"df", "df -h"},
		{"ps", "ps aux"},
		{"ss", "ss -tlnp"},
		{"du", "du -sh /tmp/lily-test"},
		{"stat", "stat /tmp/lily-test/hello.txt"},
		{"file", "file /tmp/lily-test/hello.txt"},
		{"base64", "echo hello | base64"},
		{"date", "date"},
		{"which", "which cat"},
		{"nproc", "nproc"},
		{"free", "free"},
		{"arch", "arch"},
	}

	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			result, err := te.exec.Run(ctx, "lily-target-direct", tc.cmd)
			if err != nil {
				t.Fatalf("command %q failed: %v", tc.cmd, err)
			}
			if result.ExitCode != 0 {
				t.Errorf("command %q exited %d: %s", tc.cmd, result.ExitCode, result.Stderr)
			}
		})
	}
}

// ─── ProxyJump operations ───────────────────────────────────────────────

func TestProxyJumpReadFile(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target", "cat /tmp/lily-test/hello.txt")
	if !strings.Contains(result.Stdout, "Hello from Lily E2E") {
		t.Errorf("ProxyJump read failed: %q", result.Stdout)
	}
}

func TestProxyJumpSystemInfo(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target", "uname -a")
	if !strings.Contains(result.Stdout, "Linux") {
		t.Errorf("ProxyJump uname failed: %q", result.Stdout)
	}
}

func TestProxyJumpPipeline(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result := runOK(t, ctx, te.exec, "lily-target",
		"cat /tmp/lily-test/fruits.txt | sort | uniq -c | sort -rn")
	if !strings.Contains(result.Stdout, "apple") {
		t.Errorf("ProxyJump pipeline failed: %q", result.Stdout)
	}
}

// ─── MCP server integration ─────────────────────────────────────────────

func TestMCPServerCreation(t *testing.T) {
	te := getTestEnv(t)
	cfg := &allowlist.Config{}
	server := mcp.NewServer(te.hosts, 30*time.Second, cfg)
	if server == nil {
		t.Fatal("expected non-nil MCP server")
	}
}

func TestMCPServerValidateCommand(t *testing.T) {
	v := readonly.DefaultValidator()
	tests := []struct {
		cmd     string
		allowed bool
	}{
		{"cat /etc/hosts", true},
		{"ls -la /tmp", true},
		{"rm -rf /", false},
		{"sudo reboot", false},
		{"bash -c whoami", false},
		{"echo test > /tmp/out", false},
	}
	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			err := v.ValidateCommand(tc.cmd)
			if tc.allowed && err != nil {
				t.Errorf("expected allowed: %v", err)
			}
			if !tc.allowed && err == nil {
				t.Errorf("expected blocked")
			}
		})
	}
}

func TestMCPServerRunCommand(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	v := readonly.DefaultValidator()
	cmd := "cat /tmp/lily-test/hello.txt"

	if err := v.ValidateCommand(cmd); err != nil {
		t.Fatalf("should be allowed: %v", err)
	}
	safeCmd, err := v.SanitizeCommand(cmd)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	result, err := te.exec.Run(ctx, "lily-target-direct", safeCmd)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(result.Stdout, "Hello from Lily E2E") {
		t.Errorf("unexpected output: %s", result.Stdout)
	}
}

func TestMCPServerRunCommandBlocked(t *testing.T) {
	v := readonly.DefaultValidator()
	if err := v.ValidateCommand("rm -rf /tmp/lily-test"); err == nil {
		t.Error("expected rm to be blocked")
	}
}

func TestMCPServerCheckHost(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()

	// Direct host.
	if result, err := te.exec.Run(ctx, "lily-target-direct", "echo ok"); err != nil {
		t.Fatalf("direct: %v", err)
	} else if strings.TrimSpace(result.Stdout) != "ok" {
		t.Errorf("direct: %q", result.Stdout)
	}

	// Proxied host.
	if result, err := te.exec.Run(ctx, "lily-target", "echo ok"); err != nil {
		t.Fatalf("proxy: %v", err)
	} else if strings.TrimSpace(result.Stdout) != "ok" {
		t.Errorf("proxy: %q", result.Stdout)
	}
}

func TestMCPServerCheckHostInvalid(t *testing.T) {
	te := getTestEnv(t)
	_, err := te.exec.Run(context.Background(), "nonexistent-host", "echo ok")
	if err == nil {
		t.Error("expected error for nonexistent host")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found', got: %v", err)
	}
}

func TestMCPServerListAllowedCommands(t *testing.T) {
	v := readonly.DefaultValidator()
	cmds := v.AllowedCommandsList()
	for _, want := range []string{"cat", "ls", "find", "grep", "ps", "ss", "uname"} {
		found := false
		for _, c := range cmds {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q", want)
		}
	}
}

func TestHostConfigIntegrity(t *testing.T) {
	te := getTestEnv(t)

	target := sshconfig.LookupHost(te.hosts, "lily-target")
	if target == nil {
		t.Fatal("lily-target not found")
	}
	if target.ProxyJump != "lily-bastion" {
		t.Errorf("target ProxyJump = %q, want 'lily-bastion'", target.ProxyJump)
	}

	direct := sshconfig.LookupHost(te.hosts, "lily-target-direct")
	if direct == nil {
		t.Fatal("lily-target-direct not found")
	}
	if direct.ProxyJump != "" {
		t.Errorf("direct ProxyJump = %q, want empty", direct.ProxyJump)
	}
}

// ─── Exit code handling ─────────────────────────────────────────────────

func TestExitCode(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	result, err := te.exec.Run(ctx, "lily-target-direct", "cat /tmp/lily-test/nonexistent-file-xyz")
	if err != nil {
		t.Skipf("SSH error (may be expected): %v", err)
	}
	if result.ExitCode == 0 {
		t.Error("expected non-zero exit for nonexistent file")
	}
}

// ─── Timeout ────────────────────────────────────────────────────────────

func TestCommandTimeout(t *testing.T) {
	te := getTestEnv(t)
	ctx := context.Background()
	exec := sshexec.NewExecutor(te.hosts, 2*time.Second, sshexec.DefaultMaxOutputBytes)
	_, err := exec.Run(ctx, "lily-target-direct", "sleep 30")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout, got: %v", err)
	}
}

// ─── Sanitization ───────────────────────────────────────────────────────

func TestCommandSanitization(t *testing.T) {
	v := readonly.DefaultValidator()
	tests := []struct {
		name   string
		cmd    string
		wantOK bool
	}{
		{"simple", "cat /etc/hosts", true},
		{"flags", "ls -la /tmp", true},
		{"pipeline", "cat /tmp/file | grep pattern", true},
		{"redirect", "echo test > /tmp/out", false},
		{"subshell", "(echo test)", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := v.ValidateCommand(tc.cmd)
			if !tc.wantOK {
				if err == nil {
					t.Error("expected rejection")
				}
				return
			}
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			safe, err := v.SanitizeCommand(tc.cmd)
			if err != nil {
				t.Fatalf("sanitize: %v", err)
			}
			t.Logf("sanitized: %q -> %q", tc.cmd, safe)
		})
	}
}
