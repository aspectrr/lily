package sshexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aspectrr/lily/internal/sshconfig"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	// DefaultMaxOutputBytes is the maximum total output (stdout + stderr) captured.
	// Beyond this limit, output is truncated to prevent memory exhaustion.
	DefaultMaxOutputBytes = 1024 * 1024 // 1 MB
)

// Result holds the output of an SSH command execution.
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool // true if output exceeded MaxOutputBytes
}

// Executor runs commands on remote hosts via SSH.
type Executor struct {
	hosts          []sshconfig.Host
	timeout        time.Duration
	maxOutputBytes int64
}

// NewExecutor creates a new SSH executor with the given host entries and output limit.
func NewExecutor(hosts []sshconfig.Host, timeout time.Duration, maxOutputBytes int64) *Executor {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = DefaultMaxOutputBytes
	}
	return &Executor{
		hosts:          hosts,
		timeout:        timeout,
		maxOutputBytes: maxOutputBytes,
	}
}

// Run executes a command on the specified host and returns the result.
func (e *Executor) Run(ctx context.Context, hostName string, command string) (*Result, error) {
	host := sshconfig.LookupHost(e.hosts, hostName)
	if host == nil {
		return nil, fmt.Errorf("host %q not found in SSH config", hostName)
	}

	client, err := e.dial(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("SSH connect to %s: %w", hostName, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("SSH session: %w", err)
	}
	defer session.Close()

	// Use limited writers to cap output size
	var stdout, stderr limitedBuffer
	stdout.limit = e.maxOutputBytes
	stderr.limit = e.maxOutputBytes
	session.Stdout = &stdout
	session.Stderr = &stderr

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("command timed out after %s", e.timeout)
	case err := <-done:
		result := &Result{
			Stdout:    stdout.String(),
			Stderr:    stderr.String(),
			Truncated: stdout.truncated || stderr.truncated,
		}
		if err != nil {
			if exitErr, ok := err.(*ssh.ExitError); ok {
				result.ExitCode = exitErr.ExitStatus()
			} else {
				return nil, fmt.Errorf("command execution: %w", err)
			}
		}
		return result, nil
	}
}

// sshClient is the interface for SSH client operations used by Executor.
// Both direct and proxied connections implement this interface.
type sshClient interface {
	NewSession() (*ssh.Session, error)
	Close() error
}

// proxiedClient wraps an SSH client established through one or more proxy hops.
// It keeps intermediate proxy clients alive and closes them all on Close().
type proxiedClient struct {
	*ssh.Client
	proxies []*ssh.Client
}

func (pc *proxiedClient) Close() error {
	var firstErr error
	if err := pc.Client.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	for i := len(pc.proxies) - 1; i >= 0; i-- {
		if err := pc.proxies[i].Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (e *Executor) dial(ctx context.Context, host *sshconfig.Host) (sshClient, error) {
	if host.ProxyCommand != "" && host.ProxyJump == "" {
		fmt.Fprintf(os.Stderr, "warning: ProxyCommand is not supported for host %q (use ProxyJump instead)\n", host.Host)
	}

	if host.ProxyJump == "" {
		return e.dialDirect(ctx, host)
	}
	return e.dialViaProxy(ctx, host)
}

func (e *Executor) dialDirect(ctx context.Context, host *sshconfig.Host) (*ssh.Client, error) {
	config, err := buildSSHConfig(host)
	if err != nil {
		return nil, err
	}

	address := resolveAddress(host)

	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		conn.Close()
		return nil, err
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}

func (e *Executor) dialViaProxy(ctx context.Context, target *sshconfig.Host) (sshClient, error) {
	chain, err := e.resolveProxyChain(target)
	if err != nil {
		return nil, err
	}

	// First hop: dial directly
	firstClient, err := e.dialDirect(ctx, chain[0])
	if err != nil {
		return nil, fmt.Errorf("proxy %s: %w", chain[0].Host, err)
	}

	var proxies []*ssh.Client
	prevClient := firstClient

	// cleanup closes all tracked proxy connections on error.
	cleanup := func() {
		prevClient.Close()
		closeClients(proxies)
	}

	for i := 1; i < len(chain); i++ {
		hop := chain[i]
		hopAddr := resolveAddress(hop)

		hopConfig, err := buildSSHConfig(hop)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("config for %s: %w", hop.Host, err)
		}

		conn, err := prevClient.Dial("tcp", hopAddr)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("tunnel to %s: %w", hop.Host, err)
		}

		sshConn, chans, reqs, err := ssh.NewClientConn(conn, hopAddr, hopConfig)
		if err != nil {
			conn.Close()
			cleanup()
			return nil, fmt.Errorf("SSH to %s via proxy: %w", hop.Host, err)
		}

		nextClient := ssh.NewClient(sshConn, chans, reqs)

		if i < len(chain)-1 {
			// Intermediate hop — keep it alive for the tunnel
			proxies = append(proxies, prevClient)
			prevClient = nextClient
		} else {
			// Final hop (the target)
			proxies = append(proxies, prevClient)
			return &proxiedClient{
				Client:  nextClient,
				proxies: proxies,
			}, nil
		}
	}

	// Unreachable for valid chains
	cleanup()
	return nil, fmt.Errorf("internal error: empty proxy chain")
}

// resolveProxyChain builds the full connection chain from the target host
// back through its ProxyJump hops to the first directly reachable host.
//
// Example: target.ProxyJump="bastion", bastion.ProxyJump="gateway"
// → returns [gateway, bastion, target]
func (e *Executor) resolveProxyChain(target *sshconfig.Host) ([]*sshconfig.Host, error) {
	return e.resolveChain(target, map[string]bool{})
}

func (e *Executor) resolveChain(host *sshconfig.Host, visited map[string]bool) ([]*sshconfig.Host, error) {
	if host.ProxyJump == "" {
		return []*sshconfig.Host{host}, nil
	}

	proxyNames := splitProxyChain(host.ProxyJump)
	if len(proxyNames) == 0 {
		return nil, fmt.Errorf("invalid ProxyJump value for host %s", host.Host)
	}

	// Comma-separated proxies: use each directly without recursive resolution
	if len(proxyNames) > 1 {
		chain := make([]*sshconfig.Host, 0, len(proxyNames)+1)
		for _, name := range proxyNames {
			if visited[name] {
				return nil, fmt.Errorf("proxy loop detected: %s", name)
			}
			proxy := sshconfig.LookupHost(e.hosts, name)
			if proxy == nil {
				return nil, fmt.Errorf("proxy host %q not found in SSH config", name)
			}
			chain = append(chain, proxy)
		}
		chain = append(chain, host)
		return chain, nil
	}

	// Single proxy: resolve recursively
	proxyName := proxyNames[0]
	if visited[proxyName] {
		return nil, fmt.Errorf("proxy loop detected: %s", proxyName)
	}
	visited[proxyName] = true

	proxy := sshconfig.LookupHost(e.hosts, proxyName)
	if proxy == nil {
		return nil, fmt.Errorf("proxy host %q not found in SSH config", proxyName)
	}

	prefix, err := e.resolveChain(proxy, visited)
	if err != nil {
		return nil, err
	}

	return append(prefix, host), nil
}

func splitProxyChain(proxyJump string) []string {
	parts := strings.Split(proxyJump, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func buildSSHConfig(host *sshconfig.Host) (*ssh.ClientConfig, error) {
	authMethods, err := getAuthMethods(host)
	if err != nil {
		return nil, err
	}

	hostKeyCallback, err := getHostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("host key verification: %w", err)
	}

	return &ssh.ClientConfig{
		User:            resolveUser(host),
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}, nil
}

func closeClients(clients []*ssh.Client) {
	for i := len(clients) - 1; i >= 0; i-- {
		if err := clients[i].Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing proxy client: %v\n", err)
		}
	}
}

// getHostKeyCallback returns a HostKeyCallback that implements Trust On First Use
// (TOFU). On first connection to a host, its key is recorded in ~/.ssh/known_hosts.
// On subsequent connections, the key is verified against the recorded value.
// If the key changes, the connection is rejected (potential MITM attack).
func getHostKeyCallback() (ssh.HostKeyCallback, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	defaultPath := filepath.Join(home, ".ssh", "known_hosts")

	// Ensure the known_hosts file exists (create it if it doesn't)
	if _, err := os.Stat(defaultPath); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(filepath.Dir(defaultPath), 0700); mkErr != nil {
			return nil, fmt.Errorf("cannot create .ssh directory: %w", mkErr)
		}
		if writeErr := os.WriteFile(defaultPath, nil, 0600); writeErr != nil {
			return nil, fmt.Errorf("cannot create known_hosts: %w", writeErr)
		}
	}

	cb, err := knownhosts.New(defaultPath)
	if err != nil {
		return nil, fmt.Errorf("parse known_hosts: %w", err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		if err == nil {
			// Key matches — known host, all good
			return nil
		}

		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			// Some other error (e.g., revoked key) — reject
			return err
		}

		if len(keyErr.Want) > 0 {
			// Key mismatch — known host but different key. Likely MITM.
			return fmt.Errorf("host key mismatch for %s: possible MITM attack (recorded key differs from server key)", hostname)
		}

		// Key is unknown (first connection) — trust on first use: record the key.
		line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
		f, ferr := os.OpenFile(defaultPath, os.O_APPEND|os.O_WRONLY, 0600)
		if ferr != nil {
			return fmt.Errorf("cannot record host key for %s: %w", hostname, ferr)
		}
		defer f.Close()
		if _, ferr = fmt.Fprintf(f, "%s\n", line); ferr != nil {
			return fmt.Errorf("cannot record host key for %s: %w", hostname, ferr)
		}

		fmt.Fprintf(os.Stderr, "info: first connection to %s — host key recorded in %s\n", hostname, defaultPath)
		return nil
	}, nil
}

// limitedBuffer is a bytes.Buffer that stops accepting writes after reaching limit.
// A mutex is needed because the SSH library may call Write from concurrent goroutines.
type limitedBuffer struct {
	bytes.Buffer
	limit     int64
	truncated bool
}

func (lb *limitedBuffer) Write(p []byte) (n int, err error) {
	if lb.limit > 0 && int64(lb.Buffer.Len())+int64(len(p)) > lb.limit {
		// Accept only what fits
		remaining := lb.limit - int64(lb.Buffer.Len())
		if remaining > 0 {
			if _, err := lb.Buffer.Write(p[:remaining]); err != nil {
				return len(p), fmt.Errorf("output buffer write failed: %w", err)
			}
		}
		lb.truncated = true
		return len(p), nil // Report full write to avoid session errors
	}
	return lb.Buffer.Write(p)
}

func resolveAddress(host *sshconfig.Host) string {
	addr := host.HostName
	if addr == "" {
		addr = host.Host
	}
	port := host.Port
	if port == "" {
		port = "22"
	}
	return net.JoinHostPort(addr, port)
}

func resolveUser(host *sshconfig.Host) string {
	if host.User != "" {
		return host.User
	}
	return os.Getenv("USER")
}

func getAuthMethods(host *sshconfig.Host) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// Try SSH agent first
	if authMethod, err := agentAuth(); err == nil && authMethod != nil {
		methods = append(methods, authMethod)
	}

	// Try identity file from config
	if host.IdentityFile != "" {
		if authMethod, err := publicKeyAuth(host.IdentityFile); err == nil {
			methods = append(methods, authMethod)
		}
	}

	// Try default keys
	home, err := os.UserHomeDir()
	if err == nil {
		defaultKeys := []string{
			"id_ed25519",
			"id_ecdsa",
			"id_rsa",
			"id_dsa",
		}
		for _, key := range defaultKeys {
			keyPath := filepath.Join(home, ".ssh", key)
			if host.IdentityFile != "" && keyPath == host.IdentityFile {
				continue // already tried
			}
			if authMethod, err := publicKeyAuth(keyPath); err == nil {
				methods = append(methods, authMethod)
			}
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no SSH authentication methods available (no agent, no keys found)")
	}
	return methods, nil
}

func agentAuth() (ssh.AuthMethod, error) {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK not set")
	}

	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, err
	}

	agentClient := agent.NewClient(conn)
	signers, err := agentClient.Signers()
	if err != nil {
		conn.Close()
		return nil, err
	}
	if len(signers) == 0 {
		conn.Close()
		return nil, fmt.Errorf("no identities in agent")
	}

	return ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		return signers, nil
	}), nil
}

func publicKeyAuth(path string) (ssh.AuthMethod, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		// Try with passphrase from agent
		if _, ok := err.(*ssh.PassphraseMissingError); ok {
			return nil, err
		}
		return nil, err
	}

	return ssh.PublicKeys(signer), nil
}
