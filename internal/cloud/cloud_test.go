package cloud

import (
	"testing"
)

func TestParseCommand_CommandFlag(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantArgs    []string
		wantCommand string
	}{
		{
			name:        "no command",
			args:        []string{"ssm", "start-session", "--target", "i-xxx"},
			wantArgs:    []string{"ssm", "start-session", "--target", "i-xxx"},
			wantCommand: "",
		},
		{
			name:        "--command flag",
			args:        []string{"ssm", "start-session", "--target", "i-xxx", "--command", "ps aux"},
			wantArgs:    []string{"ssm", "start-session", "--target", "i-xxx"},
			wantCommand: "ps aux",
		},
		{
			name:        "--command=value form",
			args:        []string{"compute", "ssh", "my-instance", "--command=ps aux"},
			wantArgs:    []string{"compute", "ssh", "my-instance"},
			wantCommand: "ps aux",
		},
		{
			name:        "command at start",
			args:        []string{"--command", "uptime", "compute", "ssh", "my-instance"},
			wantArgs:    []string{"compute", "ssh", "my-instance"},
			wantCommand: "uptime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, cmd := ParseCommand(tt.args)
			if cmd != tt.wantCommand {
				t.Errorf("ParseCommand() command = %q, want %q", cmd, tt.wantCommand)
			}
			if len(args) != len(tt.wantArgs) {
				t.Errorf("ParseCommand() args = %v, want %v", args, tt.wantArgs)
				return
			}
			for i, arg := range args {
				if arg != tt.wantArgs[i] {
					t.Errorf("ParseCommand() args[%d] = %q, want %q", i, arg, tt.wantArgs[i])
				}
			}
		})
	}
}

func TestParseCommand_DashDashSeparator(t *testing.T) {
	args, cmd := ParseCommand([]string{"ssh", "vm", "--resource-group", "RG", "--name", "VM", "--", "ps aux"})
	if cmd != "ps aux" {
		t.Errorf("expected command 'ps aux', got %q", cmd)
	}
	expectedArgs := []string{"ssh", "vm", "--resource-group", "RG", "--name", "VM"}
	if len(args) != len(expectedArgs) {
		t.Fatalf("expected %d args, got %d: %v", len(expectedArgs), len(args), args)
	}
	for i, arg := range args {
		if arg != expectedArgs[i] {
			t.Errorf("args[%d] = %q, want %q", i, arg, expectedArgs[i])
		}
	}
}

func TestParseCommand_DashDashNoCommand(t *testing.T) {
	args, cmd := ParseCommand([]string{"ssh", "vm", "--"})
	if cmd != "" {
		t.Errorf("expected empty command, got %q", cmd)
	}
	// With nothing after --, the -- is left in args since there's no command to extract
	if len(args) != 3 || args[0] != "ssh" || args[1] != "vm" || args[2] != "--" {
		t.Errorf("expected [ssh vm --], got %v", args)
	}
}

func TestParseCommand_ParametersJSON(t *testing.T) {
	providerArgs, cmd := ParseCommand([]string{
		"ssm", "start-session",
		"--target", "i-xxx",
		"--parameters", `{"commands":["ps aux"]}`,
	})
	if cmd != "ps aux" {
		t.Errorf("expected command 'ps aux', got %q", cmd)
	}
	// Should have removed --parameters and its value
	for _, arg := range providerArgs {
		if arg == "--parameters" || arg == `{"commands":["ps aux"]}` {
			t.Errorf("--parameters should have been removed, found: %q", arg)
		}
	}
}

func TestParseCommand_ParametersJSONSingleKey(t *testing.T) {
	providerArgs, cmd := ParseCommand([]string{
		"ssm", "send-command",
		"--instance-ids", "i-xxx",
		"--parameters", `{"command":["systemctl status nginx"]}`,
	})
	if cmd != "systemctl status nginx" {
		t.Errorf("expected command 'systemctl status nginx', got %q", cmd)
	}
	for _, arg := range providerArgs {
		if arg == "--parameters" {
			t.Errorf("--parameters should have been removed")
		}
	}
}

func TestParseCommand_ParametersInvalidJSON(t *testing.T) {
	providerArgs, cmd := ParseCommand([]string{
		"ssm", "start-session",
		"--parameters", "not-json",
	})
	if cmd != "" {
		t.Errorf("expected empty command for invalid JSON, got %q", cmd)
	}
	if len(providerArgs) != 4 {
		t.Errorf("expected args to be unchanged, got %v", providerArgs)
	}
}

func TestParseCommand_CommandFlagTakesPrecedenceOverDashDash(t *testing.T) {
	_, cmd := ParseCommand([]string{
		"ssh", "vm",
		"--command", "ps aux",
		"--", "systemctl status nginx",
	})
	if cmd != "ps aux" {
		t.Errorf("expected --command to take precedence, got %q", cmd)
	}
}

func TestParseCommand_CommandFlagTakesPrecedenceOverParameters(t *testing.T) {
	_, cmd := ParseCommand([]string{
		"ssm", "start-session",
		"--command", "uptime",
		"--parameters", `{"commands":["ps aux"]}`,
	})
	if cmd != "uptime" {
		t.Errorf("expected --command to take precedence, got %q", cmd)
	}
}

func TestProviderBinary(t *testing.T) {
	tests := []struct {
		provider Provider
		binary   string
	}{
		{AWS, "aws"},
		{GCloud, "gcloud"},
		{Azure, "az"},
	}
	for _, tt := range tests {
		if got := providerBinary(tt.provider); got != tt.binary {
			t.Errorf("providerBinary(%s) = %q, want %q", tt.provider, got, tt.binary)
		}
	}
}

func TestExtractIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		args     []string
		want     string
	}{
		{
			name:     "aws ssm --target",
			provider: AWS,
			args:     []string{"ssm", "start-session", "--target", "i-12345"},
			want:     "i-12345",
		},
		{
			name:     "aws ssm --instance-id",
			provider: AWS,
			args:     []string{"ssm", "start-session", "--instance-id", "i-67890"},
			want:     "i-67890",
		},
		{
			name:     "aws fallback",
			provider: AWS,
			args:     []string{"ssm", "start-session"},
			want:     "aws",
		},
		{
			name:     "gcloud compute ssh instance name",
			provider: GCloud,
			args:     []string{"compute", "ssh", "my-instance", "--project", "P", "--zone", "Z"},
			want:     "my-instance",
		},
		{
			name:     "gcloud fallback",
			provider: GCloud,
			args:     []string{"compute", "ssh", "--project", "P"},
			want:     "gcloud",
		},
		{
			name:     "azure --name",
			provider: Azure,
			args:     []string{"ssh", "vm", "--resource-group", "RG", "--name", "MyVM"},
			want:     "MyVM",
		},
		{
			name:     "azure --resource-group fallback",
			provider: Azure,
			args:     []string{"ssh", "vm", "--resource-group", "RG"},
			want:     "RG",
		},
		{
			name:     "azure fallback",
			provider: Azure,
			args:     []string{"ssh", "vm"},
			want:     "azure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractIdentifier(tt.provider, tt.args)
			if got != tt.want {
				t.Errorf("ExtractIdentifier() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractFlagValue(t *testing.T) {
	tests := []struct {
		args []string
		flag string
		want string
	}{
		{[]string{"--target", "i-xxx"}, "--target", "i-xxx"},
		{[]string{"--target=i-yyy"}, "--target", "i-yyy"},
		{[]string{"--target"}, "--target", ""},
		{[]string{"--other", "value"}, "--target", ""},
		{[]string{"--project", "my-project", "--zone", "us-east1"}, "--zone", "us-east1"},
	}

	for _, tt := range tests {
		got := extractFlagValue(tt.args, tt.flag)
		if got != tt.want {
			t.Errorf("extractFlagValue(%v, %q) = %q, want %q", tt.args, tt.flag, got, tt.want)
		}
	}
}

func TestEscapeJSONString(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"has spaces", "has spaces"},
		{`has "quotes"`, `has \"quotes\"`},
		{"has\nnewline", "has\\nnewline"},
		{"has\\backslash", "has\\\\backslash"},
	}

	for _, tt := range tests {
		got := escapeJSONString(tt.input)
		if got != tt.want {
			t.Errorf("escapeJSONString(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ── DetectCloudSSH tests ──────────────────────────────────────────────

func TestDetectCloudSSH_AWSSSM(t *testing.T) {
	provider, rewritten, detected := DetectCloudSSH("aws ssm start-session --target i-12345")
	if !detected {
		t.Fatal("expected detection")
	}
	if provider != AWS {
		t.Fatalf("expected AWS provider, got %s", provider)
	}
	if rewritten != "lily aws ssm start-session --target i-12345" {
		t.Fatalf("unexpected rewrite: %s", rewritten)
	}
}

func TestDetectCloudSSH_AWSEC2InstanceConnect(t *testing.T) {
	provider, rewritten, detected := DetectCloudSSH("aws ec2-instance-connect ssh --instance-id i-12345")
	if !detected {
		t.Fatal("expected detection")
	}
	if provider != AWS {
		t.Fatalf("expected AWS provider, got %s", provider)
	}
	if rewritten != "lily aws ec2-instance-connect ssh --instance-id i-12345" {
		t.Fatalf("unexpected rewrite: %s", rewritten)
	}
}

func TestDetectCloudSSH_AWSOtherCommand(t *testing.T) {
	_, _, detected := DetectCloudSSH("aws s3 ls")
	if detected {
		t.Fatal("aws s3 ls should not be detected as cloud SSH")
	}
}

func TestDetectCloudSSH_GCloudComputeSSH(t *testing.T) {
	provider, rewritten, detected := DetectCloudSSH("gcloud compute ssh my-instance --project my-project --zone us-central1-a")
	if !detected {
		t.Fatal("expected detection")
	}
	if provider != GCloud {
		t.Fatalf("expected GCloud provider, got %s", provider)
	}
	if rewritten != "lily gcloud compute ssh my-instance --project my-project --zone us-central1-a" {
		t.Fatalf("unexpected rewrite: %s", rewritten)
	}
}

func TestDetectCloudSSH_GCloudOtherCommand(t *testing.T) {
	_, _, detected := DetectCloudSSH("gcloud compute instances list")
	if detected {
		t.Fatal("gcloud compute instances list should not be detected")
	}
}

func TestDetectCloudSSH_AzureSSHVM(t *testing.T) {
	provider, rewritten, detected := DetectCloudSSH("az ssh vm --resource-group MyRG --name MyVM")
	if !detected {
		t.Fatal("expected detection")
	}
	if provider != Azure {
		t.Fatalf("expected Azure provider, got %s", provider)
	}
	if rewritten != "lily az ssh vm --resource-group MyRG --name MyVM" {
		t.Fatalf("unexpected rewrite: %s", rewritten)
	}
}

func TestDetectCloudSSH_AzureBastion(t *testing.T) {
	provider, rewritten, detected := DetectCloudSSH("az network bastion ssh --name MyBastion --resource-group MyRG --target-resource-id /sub/VM")
	if !detected {
		t.Fatal("expected detection")
	}
	if provider != Azure {
		t.Fatalf("expected Azure provider, got %s", provider)
	}
	if rewritten != "lily az network bastion ssh --name MyBastion --resource-group MyRG --target-resource-id /sub/VM" {
		t.Fatalf("unexpected rewrite: %s", rewritten)
	}
}

func TestDetectCloudSSH_AzureOtherCommand(t *testing.T) {
	_, _, detected := DetectCloudSSH("az vm list")
	if detected {
		t.Fatal("az vm list should not be detected")
	}
}

func TestDetectCloudSSH_Passthrough(t *testing.T) {
	tests := []string{
		"ls -la",
		"git status",
		"docker ps",
		"",
		"make build",
	}
	for _, cmd := range tests {
		_, _, detected := DetectCloudSSH(cmd)
		if detected {
			t.Fatalf("expected passthrough for %q", cmd)
		}
	}
}

func TestDetectCloudSSH_WithPathPrefix(t *testing.T) {
	provider, rewritten, detected := DetectCloudSSH("/usr/local/bin/aws ssm start-session --target i-xxx")
	if !detected {
		t.Fatal("expected detection with path prefix")
	}
	if provider != AWS {
		t.Fatalf("expected AWS, got %s", provider)
	}
	if rewritten != "lily /usr/local/bin/aws ssm start-session --target i-xxx" {
		t.Fatalf("unexpected rewrite: %s", rewritten)
	}
}

func TestDetectCloudSSH_GCloudWithCommand(t *testing.T) {
	_, rewritten, detected := DetectCloudSSH("gcloud compute ssh my-instance --project P --zone Z --command 'ps aux'")
	if !detected {
		t.Fatal("expected detection")
	}
	if rewritten != "lily gcloud compute ssh my-instance --project P --zone Z --command 'ps aux'" {
		t.Fatalf("unexpected rewrite: %s", rewritten)
	}
}

func TestDetectCloudSSH_AzureWithDashDash(t *testing.T) {
	_, rewritten, detected := DetectCloudSSH("az ssh vm --resource-group RG --name VM -- ps aux")
	if !detected {
		t.Fatal("expected detection")
	}
	if rewritten != "lily az ssh vm --resource-group RG --name VM -- ps aux" {
		t.Fatalf("unexpected rewrite: %s", rewritten)
	}
}

// ── Guard bypass regression tests (PR #5) ──────────────────────────────

func TestDetectCloudSSH_AWSWithGlobalFlags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "--profile flag before ssm",
			input: "aws --profile admin ssm start-session --target i-xxx",
			want:  "lily aws --profile admin ssm start-session --target i-xxx",
		},
		{
			name:  "--region flag before ssm",
			input: "aws --region us-east-1 ssm start-session --target i-12345",
			want:  "lily aws --region us-east-1 ssm start-session --target i-12345",
		},
		{
			name:  "--output json before ec2-instance-connect",
			input: "aws --output json ec2-instance-connect ssh --instance-id i-xxx",
			want:  "lily aws --output json ec2-instance-connect ssh --instance-id i-xxx",
		},
		{
			name:  "ssm send-command is detected",
			input: "aws ssm send-command --instance-ids i-xxx --document-name AWS-RunShellScript",
			want:  "lily aws ssm send-command --instance-ids i-xxx --document-name AWS-RunShellScript",
		},
		{
			name:  "ssm send-command with global flags",
			input: "aws --profile admin ssm send-command --instance-ids i-xxx --document-name AWS-RunShellScript",
			want:  "lily aws --profile admin ssm send-command --instance-ids i-xxx --document-name AWS-RunShellScript",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, rewritten, detected := DetectCloudSSH(tt.input)
			if !detected {
				t.Fatal("expected detection")
			}
			if provider != AWS {
				t.Fatalf("expected AWS provider, got %s", provider)
			}
			if rewritten != tt.want {
				t.Fatalf("unexpected rewrite: %s", rewritten)
			}
		})
	}
}

func TestDetectCloudSSH_GCloudWithGlobalFlags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "--project flag before compute",
			input: "gcloud --project my-project compute ssh my-instance --zone us-central1-a",
			want:  "lily gcloud --project my-project compute ssh my-instance --zone us-central1-a",
		},
		{
			name:  "--verbosity flag before compute",
			input: "gcloud --verbosity debug compute ssh my-instance --project P --zone Z",
			want:  "lily gcloud --verbosity debug compute ssh my-instance --project P --zone Z",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, rewritten, detected := DetectCloudSSH(tt.input)
			if !detected {
				t.Fatal("expected detection")
			}
			if provider != GCloud {
				t.Fatalf("expected GCloud provider, got %s", provider)
			}
			if rewritten != tt.want {
				t.Fatalf("unexpected rewrite: %s", rewritten)
			}
		})
	}
}

func TestDetectCloudSSH_AzureWithGlobalFlags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "--output json before ssh vm",
			input: "az --output json ssh vm --resource-group MyRG --name MyVM",
			want:  "lily az --output json ssh vm --resource-group MyRG --name MyVM",
		},
		{
			name:  "--subscription flag before network bastion ssh",
			input: "az --subscription sub-123 network bastion ssh --name B --resource-group RG",
			want:  "lily az --subscription sub-123 network bastion ssh --name B --resource-group RG",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, rewritten, detected := DetectCloudSSH(tt.input)
			if !detected {
				t.Fatal("expected detection")
			}
			if provider != Azure {
				t.Fatalf("expected Azure provider, got %s", provider)
			}
			if rewritten != tt.want {
				t.Fatalf("unexpected rewrite: %s", rewritten)
			}
		})
	}
}

func TestDetectCloudSSH_AzureQuotedArgsPreserved(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "double-quoted resource group with spaces",
			input: `az ssh vm --resource-group "My RG" --name MyVM`,
			want:  `lily az ssh vm --resource-group "My RG" --name MyVM`,
		},
		{
			name:  "single-quoted resource group with spaces",
			input: "az ssh vm --resource-group 'My RG' --name MyVM",
			want:  "lily az ssh vm --resource-group 'My RG' --name MyVM",
		},
		{
			name:  "bastion with quoted resource group",
			input: `az network bastion ssh --name MyBastion --resource-group "My RG"`,
			want:  `lily az network bastion ssh --name MyBastion --resource-group "My RG"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, rewritten, detected := DetectCloudSSH(tt.input)
			if !detected {
				t.Fatal("expected detection")
			}
			if provider != Azure {
				t.Fatalf("expected Azure provider, got %s", provider)
			}
			if rewritten != tt.want {
				t.Fatalf("unexpected rewrite: %s\nwant: %s", rewritten, tt.want)
			}
		})
	}
}

func TestTokenizeCommand(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"aws ssm start-session --target i-xxx", []string{"aws", "ssm", "start-session", "--target", "i-xxx"}},
		{`gcloud compute ssh INSTANCE --command "ps aux"`, []string{"gcloud", "compute", "ssh", "INSTANCE", "--command", "ps aux"}},
		{"az ssh vm --name 'My VM'", []string{"az", "ssh", "vm", "--name", "My VM"}},
		{"", nil},
		{"  ", nil},
	}

	for _, tt := range tests {
		got := tokenizeCommand(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("tokenizeCommand(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i, tok := range got {
			if tok != tt.want[i] {
				t.Errorf("tokenizeCommand(%q)[%d] = %q, want %q", tt.input, i, tok, tt.want[i])
			}
		}
	}
}

// ── ValidateSubcommand tests ───────────────────────────────────────────

func TestValidateSubcommand_AWS(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"valid ssm start-session", []string{"ssm", "start-session", "--target", "i-xxx"}, false},
		{"valid ssm start-session with command", []string{"ssm", "start-session", "--target", "i-xxx", "--command", "ps aux"}, false},
		{"valid ssm send-command", []string{"ssm", "send-command", "--instance-ids", "i-xxx", "--document-name", "AWS-RunShellScript"}, false},
		{"valid ssm send-command with parameters", []string{"ssm", "send-command", "--instance-ids", "i-xxx", "--document-name", "AWS-RunShellScript", "--parameters", `{"commands":["ps aux"]}`}, false},
		{"wrong service", []string{"s3", "ls"}, true},
		{"wrong ssm subcommand", []string{"ssm", "describe-instance-information"}, true},
		{"empty args", []string{}, true},
		{"only ssm", []string{"ssm"}, true},
		{"ec2 run-instances", []string{"ec2", "run-instances"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSubcommand(AWS, tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSubcommand(AWS, %v) = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSubcommand_GCloud(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"valid compute ssh", []string{"compute", "ssh", "my-instance", "--project", "P", "--zone", "Z"}, false},
		{"valid compute ssh with command", []string{"compute", "ssh", "my-instance", "--project", "P", "--zone", "Z", "--command", "ps aux"}, false},
		{"instances list", []string{"compute", "instances", "list"}, true},
		{"config set", []string{"config", "set", "project", "P"}, true},
		{"empty args", []string{}, true},
		{"only compute", []string{"compute"}, true},
		{"only compute ssh", []string{"compute", "ssh"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSubcommand(GCloud, tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSubcommand(GCloud, %v) = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSubcommand_Azure(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"valid ssh vm", []string{"ssh", "vm", "--resource-group", "RG", "--name", "VM"}, false},
		{"valid bastion ssh", []string{"network", "bastion", "ssh", "--name", "B", "--resource-group", "RG"}, false},
		{"vm list", []string{"vm", "list"}, true},
		{"vm start", []string{"vm", "start", "--name", "VM", "--resource-group", "RG"}, true},
		{"empty args", []string{}, true},
		{"only ssh", []string{"ssh"}, true},
		{"network list", []string{"network", "vnet", "list"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSubcommand(Azure, tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSubcommand(Azure, %v) = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSubcommand_Kubectl(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"valid exec", []string{"exec", "my-pod", "--", "ps aux"}, false},
		{"valid exec with container", []string{"exec", "my-pod", "-c", "sidecar", "--", "ps aux"}, false},
		{"valid exec with namespace", []string{"exec", "my-pod", "-n", "prod", "--", "ps aux"}, false},
		{"valid exec with global kubeconfig flag", []string{"--kubeconfig", "/tmp/config", "exec", "my-pod", "--", "ps aux"}, false},
		{"valid exec with global context flag", []string{"--context", "prod", "exec", "my-pod", "--", "ps aux"}, false},
		{"get pods", []string{"get", "pods"}, true},
		{"logs", []string{"logs", "my-pod"}, true},
		{"apply", []string{"apply", "-f", "deployment.yaml"}, true},
		{"empty args", []string{}, true},
		{"only exec", []string{"exec"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSubcommand(Kubectl, tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSubcommand(Kubectl, %v) = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
		})
	}
}

// ── kubectl exec detection tests ─────────────────────────────────────────

func TestDetectCloudSSH_KubectlExec(t *testing.T) {
	provider, rewritten, detected := DetectCloudSSH("kubectl exec my-pod -- ps aux")
	if !detected {
		t.Fatal("expected detection")
	}
	if provider != Kubectl {
		t.Fatalf("expected Kubectl provider, got %s", provider)
	}
	if rewritten != "lily kubectl exec my-pod -- ps aux" {
		t.Fatalf("unexpected rewrite: %s", rewritten)
	}
}

func TestDetectCloudSSH_KubectlExecWithFlags(t *testing.T) {
	provider, rewritten, detected := DetectCloudSSH("kubectl exec my-pod -c sidecar -n prod -- cat /etc/config.yaml")
	if !detected {
		t.Fatal("expected detection")
	}
	if provider != Kubectl {
		t.Fatalf("expected Kubectl provider, got %s", provider)
	}
	if rewritten != "lily kubectl exec my-pod -c sidecar -n prod -- cat /etc/config.yaml" {
		t.Fatalf("unexpected rewrite: %s", rewritten)
	}
}

func TestDetectCloudSSH_KubectlExecWithGlobalFlags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "--kubeconfig flag before exec",
			input: "kubectl --kubeconfig /tmp/config exec my-pod -- ps aux",
			want:  "lily kubectl --kubeconfig /tmp/config exec my-pod -- ps aux",
		},
		{
			name:  "--context flag before exec",
			input: "kubectl --context prod exec my-pod -- uptime",
			want:  "lily kubectl --context prod exec my-pod -- uptime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, rewritten, detected := DetectCloudSSH(tt.input)
			if !detected {
				t.Fatal("expected detection")
			}
			if provider != Kubectl {
				t.Fatalf("expected Kubectl provider, got %s", provider)
			}
			if rewritten != tt.want {
				t.Fatalf("unexpected rewrite: %s", rewritten)
			}
		})
	}
}

func TestDetectCloudSSH_KubectlOtherCommand(t *testing.T) {
	tests := []string{
		"kubectl get pods",
		"kubectl describe pod my-pod",
		"kubectl logs my-pod",
		"kubectl apply -f deployment.yaml",
		"kubectl delete pod my-pod",
	}
	for _, cmd := range tests {
		_, _, detected := DetectCloudSSH(cmd)
		if detected {
			t.Fatalf("expected no detection for %q", cmd)
		}
	}
}

func TestDetectCloudSSH_KubectlWithPathPrefix(t *testing.T) {
	provider, rewritten, detected := DetectCloudSSH("/usr/local/bin/kubectl exec my-pod -- ps aux")
	if !detected {
		t.Fatal("expected detection with path prefix")
	}
	if provider != Kubectl {
		t.Fatalf("expected Kubectl, got %s", provider)
	}
	if rewritten != "lily /usr/local/bin/kubectl exec my-pod -- ps aux" {
		t.Fatalf("unexpected rewrite: %s", rewritten)
	}
}

func TestExtractIdentifier_Kubectl(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"pod name", []string{"exec", "my-pod", "--", "ps aux"}, "my-pod"},
		{"pod name with flags", []string{"exec", "my-pod", "-c", "sidecar", "-n", "prod", "--", "ps aux"}, "my-pod"},
		{"namespace fallback", []string{"exec", "-n", "prod"}, "prod"},
		{"fallback", []string{"exec"}, "kubectl"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractIdentifier(Kubectl, tt.args)
			if got != tt.want {
				t.Errorf("ExtractIdentifier(Kubectl, %v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
