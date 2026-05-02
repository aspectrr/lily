package sshexec

import (
	"strings"
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

func TestResolveProxyChain_NoProxy(t *testing.T) {
	hosts := []sshconfig.Host{
		{Host: "web1", Names: []string{"web1"}, HostName: "10.0.0.5"},
	}
	exec := NewExecutor(hosts, 30*time.Second, 0)

	chain, err := exec.resolveProxyChain(&hosts[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 1 || chain[0].Host != "web1" {
		t.Errorf("expected [web1], got %v", hostNamesFromChain(chain))
	}
}

func TestResolveProxyChain_SingleProxy(t *testing.T) {
	hosts := []sshconfig.Host{
		{Host: "bastion", Names: []string{"bastion"}, HostName: "1.2.3.4"},
		{Host: "web1", Names: []string{"web1"}, HostName: "10.0.0.5", ProxyJump: "bastion"},
	}
	exec := NewExecutor(hosts, 30*time.Second, 0)

	chain, err := exec.resolveProxyChain(&hosts[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 {
		t.Fatalf("expected 2 hops, got %d", len(chain))
	}
	if chain[0].Host != "bastion" {
		t.Errorf("expected first hop bastion, got %s", chain[0].Host)
	}
	if chain[1].Host != "web1" {
		t.Errorf("expected second hop web1, got %s", chain[1].Host)
	}
}

func TestResolveProxyChain_RecursiveProxy(t *testing.T) {
	hosts := []sshconfig.Host{
		{Host: "gateway", Names: []string{"gateway"}, HostName: "203.0.113.1"},
		{Host: "bastion", Names: []string{"bastion"}, HostName: "1.2.3.4", ProxyJump: "gateway"},
		{Host: "web1", Names: []string{"web1"}, HostName: "10.0.0.5", ProxyJump: "bastion"},
	}
	exec := NewExecutor(hosts, 30*time.Second, 0)

	chain, err := exec.resolveProxyChain(&hosts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 {
		t.Fatalf("expected 3 hops, got %d", len(chain))
	}
	if chain[0].Host != "gateway" {
		t.Errorf("expected first hop gateway, got %s", chain[0].Host)
	}
	if chain[1].Host != "bastion" {
		t.Errorf("expected second hop bastion, got %s", chain[1].Host)
	}
	if chain[2].Host != "web1" {
		t.Errorf("expected third hop web1, got %s", chain[2].Host)
	}
}

func TestResolveProxyChain_CommaProxy(t *testing.T) {
	hosts := []sshconfig.Host{
		{Host: "jump1", Names: []string{"jump1"}, HostName: "1.1.1.1"},
		{Host: "jump2", Names: []string{"jump2"}, HostName: "2.2.2.2"},
		{Host: "web1", Names: []string{"web1"}, HostName: "10.0.0.5", ProxyJump: "jump1,jump2"},
	}
	exec := NewExecutor(hosts, 30*time.Second, 0)

	chain, err := exec.resolveProxyChain(&hosts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 {
		t.Fatalf("expected 3 hops, got %d", len(chain))
	}
	if chain[0].Host != "jump1" {
		t.Errorf("expected first hop jump1, got %s", chain[0].Host)
	}
	if chain[1].Host != "jump2" {
		t.Errorf("expected second hop jump2, got %s", chain[1].Host)
	}
	if chain[2].Host != "web1" {
		t.Errorf("expected third hop web1, got %s", chain[2].Host)
	}
}

func TestResolveProxyChain_Loop(t *testing.T) {
	hosts := []sshconfig.Host{
		{Host: "bastion", Names: []string{"bastion"}, HostName: "1.2.3.4", ProxyJump: "web1"},
		{Host: "web1", Names: []string{"web1"}, HostName: "10.0.0.5", ProxyJump: "bastion"},
	}
	exec := NewExecutor(hosts, 30*time.Second, 0)

	_, err := exec.resolveProxyChain(&hosts[1])
	if err == nil {
		t.Fatal("expected error for proxy loop")
	}
	if !strings.Contains(err.Error(), "loop") {
		t.Errorf("expected loop error, got: %s", err)
	}
}

func TestResolveProxyChain_MissingProxy(t *testing.T) {
	hosts := []sshconfig.Host{
		{Host: "web1", Names: []string{"web1"}, HostName: "10.0.0.5", ProxyJump: "nonexistent"},
	}
	exec := NewExecutor(hosts, 30*time.Second, 0)

	_, err := exec.resolveProxyChain(&hosts[0])
	if err == nil {
		t.Fatal("expected error for missing proxy")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not found error, got: %s", err)
	}
}

func TestSplitProxyChain(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"bastion", []string{"bastion"}},
		{"jump1,jump2", []string{"jump1", "jump2"}},
		{" a , b , c ", []string{"a", "b", "c"}},
		{"bastion,", []string{"bastion"}},
	}
	for _, tt := range tests {
		result := splitProxyChain(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("splitProxyChain(%q): expected %v, got %v", tt.input, tt.expected, result)
			continue
		}
		for i := range result {
			if result[i] != tt.expected[i] {
				t.Errorf("splitProxyChain(%q)[%d]: expected %q, got %q", tt.input, i, tt.expected[i], result[i])
			}
		}
	}
}

func hostNamesFromChain(chain []*sshconfig.Host) []string {
	names := make([]string, len(chain))
	for i, h := range chain {
		names[i] = h.Host
	}
	return names
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
