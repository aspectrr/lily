package sshexec

import (
	"testing"
	"time"

	"github.com/aspectrr/lily/internal/sshconfig"
)

func TestNewExecutor(t *testing.T) {
	hosts := []sshconfig.Host{
		{Host: "web1", HostName: "192.168.1.10", User: "deploy", Port: "2222"},
	}
	exec := NewExecutor(hosts, 30*time.Second)
	if exec == nil {
		t.Fatal("expected non-nil executor")
	}
	if exec.timeout != 30*time.Second {
		t.Errorf("expected 30s timeout, got %s", exec.timeout)
	}
}

func TestNewExecutor_DefaultTimeout(t *testing.T) {
	exec := NewExecutor(nil, 0)
	if exec == nil {
		t.Fatal("expected non-nil executor")
	}
	if exec.timeout != 30*time.Second {
		t.Errorf("expected default 30s timeout, got %s", exec.timeout)
	}
}

func TestRun_MissingHost(t *testing.T) {
	exec := NewExecutor(nil, 5*time.Second)
	_, err := exec.Run(nil, "nonexistent", "echo hi")
	if err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestResolveAddress(t *testing.T) {
	tests := []struct {
		name     string
		host     sshconfig.Host
		expected string
	}{
		{
			name:     "hostname and port",
			host:     sshconfig.Host{Host: "web1", HostName: "192.168.1.10", Port: "2222"},
			expected: "192.168.1.10:2222",
		},
		{
			name:     "hostname default port",
			host:     sshconfig.Host{Host: "web1", HostName: "192.168.1.10"},
			expected: "192.168.1.10:22",
		},
		{
			name:     "fallback to host alias",
			host:     sshconfig.Host{Host: "myserver"},
			expected: "myserver:22",
		},
		{
			name:     "ipv6 hostname",
			host:     sshconfig.Host{Host: "host1", HostName: "::1", Port: "22"},
			expected: "[::1]:22",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := resolveAddress(&tt.host)
			if addr != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, addr)
			}
		})
	}
}

func TestResolveUser(t *testing.T) {
	tests := []struct {
		name     string
		host     sshconfig.Host
		expected string // empty means use $USER
	}{
		{"configured user", sshconfig.Host{User: "deploy"}, "deploy"},
		{"no user", sshconfig.Host{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := resolveUser(&tt.host)
			if tt.expected != "" && user != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, user)
			}
		})
	}
}
