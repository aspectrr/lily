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
