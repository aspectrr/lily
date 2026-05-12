package allowlist

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultRateLimit is the minimum interval between commands.
	DefaultRateLimit = 1 * time.Second

	// DefaultMaxOutputBytes is the maximum output captured per command.
	DefaultMaxOutputBytes = 1024 * 1024 // 1 MB
)

// Config represents the user-configurable settings for Lily.
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

	// RateLimit is the minimum duration between command executions.
	// Defaults to "1s". Set to "500ms" for faster, or "5s" for stricter.
	RateLimit string `yaml:"rate_limit"`

	// MaxOutputBytes caps the total output (stdout + stderr) per command.
	// Defaults to 1048576 (1 MB). Minimum is 1024 (1 KB).
	MaxOutputBytes int `yaml:"max_output_bytes"`

	// Memory controls the automatic investigation memory feature.
	// When enabled, Lily silently tracks debugging sessions and surfaces
	// relevant past investigations when similar issues arise.
	Memory MemoryConfig `yaml:"memory"`
}

// MemoryConfig holds settings for the investigation memory feature.
type MemoryConfig struct {
	// Enabled turns on automatic investigation tracking.
	Enabled bool `yaml:"enabled"`

	// SessionTimeout is the duration with no activity before an investigation
	// is considered complete and flushed to disk. Default: "10m".
	SessionTimeout string `yaml:"session_timeout"`

	// MaxInvestigationsPerHost is the maximum number of past investigations
	// to keep per host. Older investigations are auto-pruned. Default: 50.
	MaxInvestigationsPerHost int `yaml:"max_investigations_per_host"`
}

// GetRateLimit parses the rate limit string into a duration.
func (c *Config) GetRateLimit() time.Duration {
	if c.RateLimit == "" {
		return DefaultRateLimit
	}
	d, err := time.ParseDuration(c.RateLimit)
	if err != nil {
		return DefaultRateLimit
	}
	return d
}

// GetMaxOutputBytes returns the configured max output size with a minimum of 1KB.
func (c *Config) GetMaxOutputBytes() int64 {
	if c.MaxOutputBytes <= 0 {
		return DefaultMaxOutputBytes
	}
	min := int64(1024) // 1 KB minimum
	val := int64(c.MaxOutputBytes)
	if val < min {
		return min
	}
	return val
}

// DefaultConfigPath returns the default path for the lily config file.
func DefaultConfigPath() string {
	if configDir := os.Getenv("XDG_CONFIG_HOME"); configDir != "" {
		return filepath.Join(configDir, "lily", "lily.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "lily", "lily.yaml")
	}
	return filepath.Join(home, ".config", "lily", "lily.yaml")
}

// Load reads and parses the config from the given path.
// If the file doesn't exist, returns an empty config (no error).
// Falls back to the legacy allowlist.yaml path if lily.yaml doesn't exist.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Try legacy allowlist.yaml path
			if path == DefaultConfigPath() {
				legacyPath := legacyConfigPath()
				if legacyData, legacyErr := os.ReadFile(legacyPath); legacyErr == nil {
					return parseConfig(legacyData)
				}
			}
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	return parseConfig(data)
}

func parseConfig(data []byte) (*Config, error) {
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
	// Validate rate limit format
	if c.RateLimit != "" {
		if _, err := time.ParseDuration(c.RateLimit); err != nil {
			return fmt.Errorf("invalid rate_limit %q: %w", c.RateLimit, err)
		}
	}

	// Validate max output bytes
	if c.MaxOutputBytes < 0 {
		return fmt.Errorf("max_output_bytes must be non-negative, got %d", c.MaxOutputBytes)
	}

	// Validate memory config
	if c.Memory.Enabled && c.Memory.SessionTimeout != "" {
		if _, err := time.ParseDuration(c.Memory.SessionTimeout); err != nil {
			return fmt.Errorf("invalid memory.session_timeout %q: %w", c.Memory.SessionTimeout, err)
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

// legacyConfigPath returns the pre-v0.2 config path for migration.
func legacyConfigPath() string {
	if configDir := os.Getenv("XDG_CONFIG_HOME"); configDir != "" {
		return filepath.Join(configDir, "lily", "allowlist.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "lily", "allowlist.yaml")
	}
	return filepath.Join(home, ".config", "lily", "allowlist.yaml")
}
