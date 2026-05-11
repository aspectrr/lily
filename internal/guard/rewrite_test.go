package guard

import (
	"testing"
)

func TestRewrite_BasicSSH(t *testing.T) {
	result := Rewrite("ssh web1 systemctl status nginx")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Host != "web1" {
		t.Fatalf("expected host web1, got %s", result.Host)
	}
	if result.RemoteCommand != "systemctl status nginx" {
		t.Fatalf("expected 'systemctl status nginx', got %q", result.RemoteCommand)
	}
	if result.Rewritten != "lily run web1 'systemctl status nginx'" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

func TestRewrite_SSHWithUser(t *testing.T) {
	result := Rewrite("ssh deploy@web1 ls -la /var/log")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Host != "web1" {
		t.Fatalf("expected host web1, got %s", result.Host)
	}
}

func TestRewrite_SSHWithPort(t *testing.T) {
	result := Rewrite("ssh -p 2222 web1 ps aux")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Host != "web1" {
		t.Fatalf("expected host web1, got %s", result.Host)
	}
}

func TestRewrite_SSHWithIdentity(t *testing.T) {
	result := Rewrite("ssh -i ~/.ssh/id_rsa web1 df -h")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
}

func TestRewrite_SSHWithOptions(t *testing.T) {
	result := Rewrite("ssh -o StrictHostKeyChecking=no web1 uptime")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
}

func TestRewrite_InteractiveSSH(t *testing.T) {
	result := Rewrite("ssh web1")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily ssh web1" {
		t.Fatalf("expected 'lily ssh web1', got %q", result.Rewritten)
	}
}

func TestRewrite_SSHNoHost(t *testing.T) {
	result := Rewrite("ssh")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily ssh" {
		t.Fatalf("expected 'lily ssh', got %q", result.Rewritten)
	}
}

func TestRewrite_SCP(t *testing.T) {
	result := Rewrite("scp file.txt web1:/tmp/")
	if result.Decision != "block" {
		t.Fatalf("expected block, got %s", result.Decision)
	}
	if result.Host != "web1" {
		t.Fatalf("expected host web1, got %s", result.Host)
	}
}

func TestRewrite_RsyncSSH(t *testing.T) {
	result := Rewrite("rsync -avz -e ssh ./src/ web1:/opt/app/")
	if result.Decision != "block" {
		t.Fatalf("expected block, got %s", result.Decision)
	}
}

func TestRewrite_Passthrough(t *testing.T) {
	tests := []string{
		"git status",
		"ls -la",
		"cat file.txt",
		"make build",
		"npm test",
		"docker ps",
		"",
		"lily run web1 ps aux",
	}
	for _, cmd := range tests {
		result := Rewrite(cmd)
		if result.Decision != "passthrough" {
			t.Fatalf("expected passthrough for %q, got %s", cmd, result.Decision)
		}
	}
}

func TestRewrite_EnvPrefix(t *testing.T) {
	result := Rewrite("FOO=bar ssh web1 uptime")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Host != "web1" {
		t.Fatalf("expected host web1, got %s", result.Host)
	}
	if result.Rewritten != "lily run web1 uptime" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

func TestRewrite_MultipleEnvVars(t *testing.T) {
	result := Rewrite("FOO=bar BAZ=qux ssh web1 uptime")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
}

func TestRewrite_ComplexCommand(t *testing.T) {
	result := Rewrite("ssh web1 cat /etc/nginx/nginx.conf | grep server_name")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
}

func TestRewrite_SSHWithMultipleFlags(t *testing.T) {
	result := Rewrite("ssh -v -i ~/.ssh/mykey -p 2222 deploy@db1 'SELECT 1'")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Host != "db1" {
		t.Fatalf("expected host db1, got %s", result.Host)
	}
}

func TestRewrite_SSHLoginFlag(t *testing.T) {
	result := Rewrite("ssh -l admin web1 whoami")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Host != "web1" {
		t.Fatalf("expected host web1, got %s", result.Host)
	}
}

// ── Cloud CLI rewrite tests ────────────────────────────────────────

func TestRewrite_AWSSSMStartSession(t *testing.T) {
	result := Rewrite("aws ssm start-session --target i-12345")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily aws ssm start-session --target i-12345" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

func TestRewrite_AWSSSMWithCommand(t *testing.T) {
	result := Rewrite("aws ssm start-session --target i-12345 --command 'ps aux'")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily aws ssm start-session --target i-12345 --command 'ps aux'" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

func TestRewrite_AWSEC2InstanceConnect(t *testing.T) {
	result := Rewrite("aws ec2-instance-connect ssh --instance-id i-12345")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily aws ec2-instance-connect ssh --instance-id i-12345" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

func TestRewrite_AWSOtherCommand(t *testing.T) {
	result := Rewrite("aws s3 ls")
	if result.Decision != "passthrough" {
		t.Fatalf("expected passthrough for aws s3 ls, got %s", result.Decision)
	}
}

func TestRewrite_AWSSSMSendCommand(t *testing.T) {
	// aws ssm send-command is not detected by the guard (only start-session and ec2-instance-connect ssh)
	result := Rewrite("aws ssm send-command --instance-ids i-12345 --document-name AWS-RunShellScript")
	if result.Decision != "passthrough" {
		t.Fatalf("expected passthrough for aws ssm send-command, got %s", result.Decision)
	}
}

func TestRewrite_GCloudComputeSSH(t *testing.T) {
	result := Rewrite("gcloud compute ssh my-instance --project my-project --zone us-central1-a")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily gcloud compute ssh my-instance --project my-project --zone us-central1-a" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

func TestRewrite_GCloudComputeSSHWithCommand(t *testing.T) {
	result := Rewrite(`gcloud compute ssh my-instance --project P --zone Z --command "ps aux"`)
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
}

func TestRewrite_GCloudOtherCommand(t *testing.T) {
	result := Rewrite("gcloud compute instances list")
	if result.Decision != "passthrough" {
		t.Fatalf("expected passthrough for gcloud compute instances list, got %s", result.Decision)
	}
}

func TestRewrite_GCloudConfig(t *testing.T) {
	result := Rewrite("gcloud config set project my-project")
	if result.Decision != "passthrough" {
		t.Fatalf("expected passthrough for gcloud config, got %s", result.Decision)
	}
}

func TestRewrite_AzureSSHVM(t *testing.T) {
	result := Rewrite("az ssh vm --resource-group MyRG --name MyVM")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily az ssh vm --resource-group MyRG --name MyVM" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

func TestRewrite_AzureSSHVMWithCommand(t *testing.T) {
	result := Rewrite("az ssh vm --resource-group RG --name VM -- ps aux")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily az ssh vm --resource-group RG --name VM -- ps aux" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

func TestRewrite_AzureBastionSSH(t *testing.T) {
	result := Rewrite("az network bastion ssh --name MyBastion --resource-group MyRG --target-resource-id /subscriptions/sub/virtualMachines/MyVM")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily az network bastion ssh --name MyBastion --resource-group MyRG --target-resource-id /subscriptions/sub/virtualMachines/MyVM" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

func TestRewrite_AzureOtherCommand(t *testing.T) {
	result := Rewrite("az vm list")
	if result.Decision != "passthrough" {
		t.Fatalf("expected passthrough for az vm list, got %s", result.Decision)
	}
}

func TestRewrite_AzureVMStart(t *testing.T) {
	result := Rewrite("az vm start --resource-group RG --name MyVM")
	if result.Decision != "passthrough" {
		t.Fatalf("expected passthrough for az vm start, got %s", result.Decision)
	}
}

func TestRewrite_LilyCloudPassthrough(t *testing.T) {
	// Already using lily — should be passthrough
	tests := []string{
		"lily aws ssm start-session --target i-12345",
		"lily gcloud compute ssh my-instance --project P --zone Z",
		"lily az ssh vm --resource-group RG --name VM",
	}
	for _, cmd := range tests {
		result := Rewrite(cmd)
		if result.Decision != "passthrough" {
			t.Fatalf("expected passthrough for %q, got %s", cmd, result.Decision)
		}
	}
}

// ── PR #5 reviewer fix tests ────────────────────────────────────────

// Issue 3: Compound command bypass (&&, ;, ||, |)
func TestRewrite_CompoundCommand(t *testing.T) {
	tests := []struct {
		cmd      string
		decision string
	}{
		{"echo test && ssh web1 cat /etc/shadow", "rewrite"},
		{"true; ssh web1 cat /etc/shadow", "rewrite"},
		{"echo test || ssh web1 cat /etc/shadow", "rewrite"},
		{"echo test | ssh web1 cat /etc/shadow", "rewrite"},
		{"echo test & ssh web1 cat /etc/shadow", "rewrite"},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			result := Rewrite(tt.cmd)
			if result.Decision != tt.decision {
				t.Fatalf("expected %s, got %s", tt.decision, result.Decision)
			}
		})
	}
}

// Issue 3: Compound command with scp/rsync
func TestRewrite_CompoundCommandSCP(t *testing.T) {
	result := Rewrite("echo test && scp file web1:/tmp/")
	if result.Decision != "block" {
		t.Fatalf("expected block, got %s", result.Decision)
	}
}

// Issue 4: Backslash escape bypass
func TestRewrite_BackslashEscape(t *testing.T) {
	result := Rewrite("\\ssh web1 cat /etc/shadow")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Host != "web1" {
		t.Fatalf("expected host web1, got %s", result.Host)
	}
}

// Issue 5: rsync -e variants
func TestRewrite_RsyncSSHVariants(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{"quoted ssh", `rsync -avz -e "ssh" ./src/ web1:/opt/app/`},
		{"single quoted ssh", "rsync -avz -e 'ssh' ./src/ web1:/opt/app/"},
		{"no space", "rsync -avz -essh ./src/ web1:/opt/app/"},
		{"rsh equals", "rsync -avz --rsh=ssh ./src/ web1:/opt/app/"},
		{"rsh space", "rsync -avz --rsh ssh ./src/ web1:/opt/app/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Rewrite(tt.cmd)
			if result.Decision != "block" {
				t.Fatalf("expected block for %q, got %s", tt.cmd, result.Decision)
			}
		})
	}
}

// Issue 6: -D, -b, -B flags should not be treated as hostname
func TestRewrite_SSHMissingFlags(t *testing.T) {
	tests := []struct {
		cmd  string
		host string
	}{
		{"ssh -D 8080 web1 ls", "web1"},
		{"ssh -b 10.0.0.1 web1 ls", "web1"},
		{"ssh -B eth0 web1 ls", "web1"},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			result := Rewrite(tt.cmd)
			if result.Decision != "rewrite" {
				t.Fatalf("expected rewrite, got %s", result.Decision)
			}
			if result.Host != tt.host {
				t.Fatalf("expected host %q, got %q", tt.host, result.Host)
			}
		})
	}
}

// Verify that quoting in compound commands is respected
func TestRewrite_CompoundCommandWithQuotes(t *testing.T) {
	// ssh mentioned inside a quoted string should NOT trigger detection
	result := Rewrite(`echo "use ssh to connect" && ls`)
	if result.Decision != "passthrough" {
		t.Fatalf("expected passthrough for quoted ssh mention, got %s", result.Decision)
	}
}

// ── kubectl exec rewrite tests ────────────────────────────────────────

func TestRewrite_KubectlExec(t *testing.T) {
	result := Rewrite("kubectl exec my-pod -- ps aux")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily kubectl exec my-pod -- ps aux" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

func TestRewrite_KubectlExecWithContainer(t *testing.T) {
	result := Rewrite("kubectl exec my-pod -c sidecar -n prod -- cat /etc/config.yaml")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily kubectl exec my-pod -c sidecar -n prod -- cat /etc/config.yaml" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

func TestRewrite_KubectlExecWithCommand(t *testing.T) {
	result := Rewrite(`kubectl exec my-pod --command "ps aux"`)
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
}

func TestRewrite_KubectlOtherCommand(t *testing.T) {
	tests := []string{
		"kubectl get pods",
		"kubectl describe pod my-pod",
		"kubectl logs my-pod",
		"kubectl apply -f deployment.yaml",
	}
	for _, cmd := range tests {
		result := Rewrite(cmd)
		if result.Decision != "passthrough" {
			t.Fatalf("expected passthrough for %q, got %s", cmd, result.Decision)
		}
	}
}

func TestRewrite_KubectlExecLilyPassthrough(t *testing.T) {
	result := Rewrite("lily kubectl exec my-pod -- ps aux")
	if result.Decision != "passthrough" {
		t.Fatalf("expected passthrough for lily kubectl exec, got %s", result.Decision)
	}
}

func TestRewrite_KubectlExecCompound(t *testing.T) {
	result := Rewrite("echo test && kubectl exec my-pod -- cat /etc/shadow")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
}

func TestRewrite_KubectlExecWithGlobalFlags(t *testing.T) {
	result := Rewrite("kubectl --kubeconfig /tmp/config exec my-pod -- ps aux")
	if result.Decision != "rewrite" {
		t.Fatalf("expected rewrite, got %s", result.Decision)
	}
	if result.Rewritten != "lily kubectl --kubeconfig /tmp/config exec my-pod -- ps aux" {
		t.Fatalf("unexpected rewrite: %s", result.Rewritten)
	}
}

// Ensure quoted compound commands don't false-positive
func TestRewrite_SSHInsideQuotedArg(t *testing.T) {
	result := Rewrite(`echo "ssh is a protocol" && ls`)
	if result.Decision != "passthrough" {
		t.Fatalf("expected passthrough, got %s", result.Decision)
	}
}
