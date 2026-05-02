package sshconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	content := `
Host web1
    HostName 192.168.1.10
    User deploy
    Port 2222
    IdentityFile ~/.ssh/web1_key

Host db1
    HostName db.example.com
    User postgres

Host *
    User defaultuser
    IdentityFile ~/.ssh/id_ed25519
`

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	hosts, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(hosts) != 3 {
		t.Fatalf("expected 3 hosts, got %d", len(hosts))
	}

	// Check web1
	web1 := LookupHost(hosts, "web1")
	if web1 == nil {
		t.Fatal("expected web1")
	}
	if web1.HostName != "192.168.1.10" {
		t.Errorf("expected HostName 192.168.1.10, got %s", web1.HostName)
	}
	if web1.User != "deploy" {
		t.Errorf("expected User deploy, got %s", web1.User)
	}
	if web1.Port != "2222" {
		t.Errorf("expected Port 2222, got %s", web1.Port)
	}
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".ssh", "web1_key")
	if web1.IdentityFile != expected {
		t.Errorf("expected IdentityFile %s, got %s", expected, web1.IdentityFile)
	}

	// Check db1
	db1 := LookupHost(hosts, "db1")
	if db1 == nil {
		t.Fatal("expected db1")
	}
	if db1.HostName != "db.example.com" {
		t.Errorf("expected HostName db.example.com, got %s", db1.HostName)
	}
	if db1.User != "postgres" {
		t.Errorf("expected User postgres, got %s", db1.User)
	}

	// Check wildcard
	star := LookupHost(hosts, "*")
	if star == nil {
		t.Fatal("expected wildcard host")
	}

	// LookupHost by hostname
	web1ByIP := LookupHost(hosts, "192.168.1.10")
	if web1ByIP == nil || web1ByIP.Host != "web1" {
		t.Error("expected to find web1 by IP")
	}

	// Unknown host
	if LookupHost(hosts, "nonexistent") != nil {
		t.Error("expected nil for unknown host")
	}
}

func TestParseEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	hosts, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 0 {
		t.Errorf("expected 0 hosts, got %d", len(hosts))
	}
}

func TestParseComments(t *testing.T) {
	content := `
# This is a comment
Host web1
    # inline comment
    HostName 192.168.1.10
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	hosts, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host, got %d", len(hosts))
	}
	if hosts[0].HostName != "192.168.1.10" {
		t.Errorf("expected HostName 192.168.1.10, got %s", hosts[0].HostName)
	}
}

func TestParseEqualsSyntax(t *testing.T) {
	content := `
Host web1
    HostName=192.168.1.10
    User=deploy
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	hosts, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host, got %d", len(hosts))
	}
	if hosts[0].HostName != "192.168.1.10" {
		t.Errorf("expected HostName 192.168.1.10, got %s", hosts[0].HostName)
	}
	if hosts[0].User != "deploy" {
		t.Errorf("expected User deploy, got %s", hosts[0].User)
	}
}

func TestParseProxyJump(t *testing.T) {
	content := `
Host bastion
    HostName 1.2.3.4
    User admin

Host web1
    HostName 10.0.0.5
    User deploy
    ProxyJump bastion

Host db1
    HostName 10.0.0.6
    ProxyJump jump1,jump2

Host gateway
    HostName 203.0.113.1
    ProxyCommand ssh -W %h:%p firewall
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	hosts, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}

	web1 := LookupHost(hosts, "web1")
	if web1 == nil {
		t.Fatal("expected web1")
	}
	if web1.ProxyJump != "bastion" {
		t.Errorf("expected ProxyJump bastion, got %q", web1.ProxyJump)
	}

	db1 := LookupHost(hosts, "db1")
	if db1 == nil {
		t.Fatal("expected db1")
	}
	if db1.ProxyJump != "jump1,jump2" {
		t.Errorf("expected ProxyJump jump1,jump2, got %q", db1.ProxyJump)
	}

	gateway := LookupHost(hosts, "gateway")
	if gateway == nil {
		t.Fatal("expected gateway")
	}
	if gateway.ProxyCommand != "ssh -W %h:%p firewall" {
		t.Errorf("expected ProxyCommand, got %q", gateway.ProxyCommand)
	}
}

func TestParseMultipleAliases(t *testing.T) {
	content := `
Host web web1 production
    HostName 192.168.1.10
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	hosts, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host, got %d", len(hosts))
	}
	if len(hosts[0].Names) != 3 {
		t.Errorf("expected 3 names, got %d", len(hosts[0].Names))
	}
	if LookupHost(hosts, "web") == nil {
		t.Error("expected to find host by alias 'web'")
	}
	if LookupHost(hosts, "production") == nil {
		t.Error("expected to find host by alias 'production'")
	}
}
