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
	exec := NewExecutor(hosts, 30*time.Second, 0)
	if exec == nil {
		t.Fatal("expected non-nil executor")
	}
	if exec.timeout != 30*time.Second {
		t.Errorf("expected 30s timeout, got %s", exec.timeout)
	}
	if exec.maxOutputBytes != DefaultMaxOutputBytes {
		t.Errorf("expected default max output %d, got %d", DefaultMaxOutputBytes, exec.maxOutputBytes)
	}
}

func TestNewExecutor_DefaultTimeout(t *testing.T) {
	exec := NewExecutor(nil, 0, 0)
	if exec == nil {
		t.Fatal("expected non-nil executor")
	}
	if exec.timeout != 30*time.Second {
		t.Errorf("expected default 30s timeout, got %s", exec.timeout)
	}
}

func TestRun_MissingHost(t *testing.T) {
	exec := NewExecutor(nil, 5*time.Second, 0)
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

func TestLimitedBuffer(t *testing.T) {
	var lb limitedBuffer
	lb.limit = 10

	// Write within limit
	n, err := lb.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("expected 5 bytes written, got %d", n)
	}
	if lb.truncated {
		t.Error("should not be truncated yet")
	}

	// Write over limit
	n, err = lb.Write([]byte(" world this is too long"))
	if err != nil {
		t.Fatal(err)
	}
	if !lb.truncated {
		t.Error("expected truncated after exceeding limit")
	}
	if lb.Len() != 10 {
		t.Errorf("expected buffer capped at 10 bytes, got %d", lb.Len())
	}
	if lb.String() != "hello worl" {
		t.Errorf("unexpected buffer contents: %q", lb.String())
	}
}

func TestLimitedBuffer_NoLimit(t *testing.T) {
	var lb limitedBuffer
	lb.limit = 0 // no limit

	data := make([]byte, 100)
	for i := range data {
		data[i] = 'x'
	}
	n, err := lb.Write(data)
	if err != nil {
		t.Fatal(err)
	}
	if n != 100 {
		t.Errorf("expected 100 bytes written, got %d", n)
	}
	if lb.truncated {
		t.Error("should not be truncated with no limit")
	}
}
