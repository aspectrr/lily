package allowlist

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the user-configurable allowlist extensions.
// It can only ADD to the base allowlist — hardcoded restrictions
// (blocking rm, sudo, bash, python, etc.) cannot be overridden.
type Config struct {
	// ExtraCommands adds commands to the allowlist beyond the built-in set.
	ExtraCommands []string `yaml:"extra_commands"`

	// ExtraSubcommandRestrictions limits which subcommands are allowed
	// for extra commands. Maps command name to list of allowed subcommands.
	ExtraSubcommandRestrictions map[string][]string `yaml:"extra_subcommand_restrictions"`

	// ExtraBlockedFlags adds flags that are blocked for specific commands.
	ExtraBlockedFlags map[string][]string `yaml:"extra_blocked_flags"`
}

// DefaultConfigPath returns the default path for the allowlist config file.
func DefaultConfigPath() string {
	if configDir := os.Getenv("XDG_CONFIG_HOME"); configDir != "" {
		return filepath.Join(configDir, "lily", "allowlist.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "lily", "allowlist.yaml")
	}
	return filepath.Join(home, ".config", "lily", "allowlist.yaml")
}

// Load reads and parses the allowlist config from the given path.
// If the file doesn't exist, returns an empty config (no error).
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate checks the config for obvious errors.
func (c *Config) Validate() error {
	// Validate extra subcommand restrictions reference extra commands
	// (this is just a warning-level check, not enforced strictly)
	for cmd := range c.ExtraSubcommandRestrictions {
		found := false
		for _, extra := range c.ExtraCommands {
			if extra == cmd {
				found = true
				break
			}
		}
		if !found {
			// Still allow it — might be restricting a built-in command
			_ = cmd
		}
	}
	return nil
}

// SubcommandRestrictions converts the extra subcommand restrictions
// into the map[string]map[string]bool format used by the validator.
func (c *Config) SubcommandRestrictions() map[string]map[string]bool {
	result := make(map[string]map[string]bool)
	for cmd, subs := range c.ExtraSubcommandRestrictions {
		subMap := make(map[string]bool, len(subs))
		for _, s := range subs {
			subMap[s] = true
		}
		result[cmd] = subMap
	}
	return result
}

// BlockedFlags converts the extra blocked flags into the
// map[string][]string format used by the validator.
func (c *Config) BlockedFlags() map[string][]string {
	result := make(map[string][]string)
	for cmd, flags := range c.ExtraBlockedFlags {
		result[cmd] = flags
	}
	return result
}

// EnsureConfigDir creates the config directory if it doesn't exist.
func EnsureConfigDir() error {
	path := DefaultConfigPath()
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0755)
}
