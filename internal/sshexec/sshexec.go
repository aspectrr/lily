package sshexec

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/aspectrr/lily/internal/sshconfig"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Result holds the output of an SSH command execution.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Executor runs commands on remote hosts via SSH.
type Executor struct {
	hosts   []sshconfig.Host
	timeout time.Duration
}

// NewExecutor creates a new SSH executor with the given host entries.
func NewExecutor(hosts []sshconfig.Host, timeout time.Duration) *Executor {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Executor{hosts: hosts, timeout: timeout}
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

	var stdout, stderr bytes.Buffer
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
			Stdout: stdout.String(),
			Stderr: stderr.String(),
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

func (e *Executor) dial(ctx context.Context, host *sshconfig.Host) (*ssh.Client, error) {
	authMethods, err := getAuthMethods(host)
	if err != nil {
		return nil, err
	}

	sshConfig := &ssh.ClientConfig{
		User:            resolveUser(host),
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	address := resolveAddress(host)

	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, address, sshConfig)
	if err != nil {
		conn.Close()
		return nil, err
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
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
