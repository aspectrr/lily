package mcp

import (
	"testing"

	"github.com/aspectrr/lily/internal/allowlist"
	"github.com/aspectrr/lily/internal/sshconfig"
)

func TestHostNames(t *testing.T) {
	hosts := []sshconfig.Host{
		{Host: "web1", HostName: "192.168.1.10"},
		{Host: "db1", HostName: "db.example.com"},
		{Host: "*", HostName: ""},
		{Host: "test?", HostName: ""},
	}

	names := hostNames(hosts)

	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d: %v", len(names), names)
	}
	if names[0] != "web1" {
		t.Errorf("expected web1, got %s", names[0])
	}
	if names[1] != "db1" {
		t.Errorf("expected db1, got %s", names[1])
	}
}

func TestNewServer(t *testing.T) {
	hosts := []sshconfig.Host{
		{Host: "web1", HostName: "192.168.1.10", User: "deploy"},
	}

	cfg := &allowlist.Config{}
	server := NewServer(hosts, 30, cfg)

	if server == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestNewServer_WithConfig(t *testing.T) {
	hosts := []sshconfig.Host{
		{Host: "web1", HostName: "192.168.1.10"},
	}

	cfg := &allowlist.Config{
		ExtraCommands: []string{"docker"},
		ExtraSubcommandRestrictions: map[string][]string{
			"docker": {"ps", "logs"},
		},
	}

	server := NewServer(hosts, 30, cfg)
	if server == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestNewServer_EmptyHosts(t *testing.T) {
	cfg := &allowlist.Config{}
	server := NewServer(nil, 30, cfg)
	if server == nil {
		t.Fatal("expected non-nil server even with empty hosts")
	}
}
