package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
)

// Host represents a parsed SSH config host entry.
type Host struct {
	Host string   // The Host pattern from the config
	Names []string // All Host patterns for this block
	HostName string // Hostname or IP address
	User     string
	Port     string
	IdentityFile string
	ProxyCommand string
	ProxyJump    string
}

// Parse reads an SSH config file and returns matching host entries.
// If path is empty, it defaults to ~/.ssh/config.
func Parse(path string) ([]Host, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".ssh", "config")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseConfig(string(data)), nil
}

func parseConfig(content string) []Host {
	var hosts []Host
	var current *Host

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split into keyword and value
		keyword, value, ok := parseLine(line)
		if !ok {
			continue
		}

		switch strings.ToLower(keyword) {
		case "host":
			if current != nil {
				hosts = append(hosts, *current)
			}
			names := strings.Fields(value)
			current = &Host{
				Host:  names[0],
				Names: names,
			}
		case "match":
			// Match blocks are not host blocks; flush current if any
			if current != nil {
				hosts = append(hosts, *current)
				current = nil
			}
		default:
			if current == nil {
				continue
			}
			switch strings.ToLower(keyword) {
			case "hostname":
				current.HostName = value
			case "user":
				current.User = value
			case "port":
				current.Port = value
			case "identityfile":
				current.IdentityFile = expandTilde(value)
			case "proxycommand":
				current.ProxyCommand = value
			case "proxyjump":
				current.ProxyJump = value
			}
		}
	}
	if current != nil {
		hosts = append(hosts, *current)
	}
	return hosts
}

func parseLine(line string) (keyword, value string, ok bool) {
	// Handle "Keyword Value" and "Keyword=Value"
	line = strings.TrimSpace(line)

	// Try space separator first
	idx := strings.IndexFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '='
	})
	if idx < 0 {
		return strings.ToLower(line), "", true
	}

	keyword = strings.ToLower(strings.TrimSpace(line[:idx]))
	rest := strings.TrimSpace(line[idx+1:])
	// Remove leading = if present (for Key=Value format)
	rest = strings.TrimPrefix(rest, "=")
	value = strings.TrimSpace(rest)
	return keyword, value, true
}

func expandTilde(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// LookupHost finds a host entry by name or alias.
func LookupHost(hosts []Host, name string) *Host {
	for i := range hosts {
		for _, alias := range hosts[i].Names {
			if alias == name {
				return &hosts[i]
			}
		}
		if hosts[i].HostName == name {
			return &hosts[i]
		}
	}
	return nil
}
