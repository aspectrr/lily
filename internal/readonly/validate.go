package readonly

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// baseCommands is the hardcoded set of commands that are always available.
// This cannot be overridden by user config.
var baseCommands = map[string]bool{
	// File inspection
	"cat": true, "ls": true, "find": true, "head": true, "tail": true,
	"stat": true, "file": true, "wc": true, "du": true, "tree": true,
	"strings": true, "md5sum": true, "sha256sum": true, "readlink": true,
	"realpath": true, "basename": true, "dirname": true, "base64": true,

	// Process/system
	"ps": true, "top": true, "pgrep": true,
	"systemctl": true, "journalctl": true, "dmesg": true,

	// Network
	"ss": true, "netstat": true, "ip": true, "ifconfig": true,
	"dig": true, "nslookup": true, "ping": true,
	"curl": true,

	// TLS/cert diagnostics
	"openssl": true,

	// Disk
	"df": true, "lsblk": true, "blkid": true,

	// Package query
	"dpkg": true, "rpm": true, "apt": true, "pip": true,

	// System info
	"uname": true, "hostname": true, "uptime": true, "free": true,
	"lscpu": true, "lsmod": true, "lspci": true, "lsusb": true,
	"arch": true, "nproc": true,

	// User
	"whoami": true, "id": true, "groups": true, "who": true,
	"w": true, "last": true,

	// Misc
	"env": true, "printenv": true, "date": true, "which": true,
	"type": true, "echo": true, "test": true,

	// Pipe targets
	"grep": true, "awk": true, "sed": true, "sort": true, "uniq": true,
	"cut": true, "tr": true, "xargs": true,
}

// alwaysBlockedCommands are commands that can NEVER be allowed, even via config.
var alwaysBlockedCommands = map[string]bool{
	"rm": true, "rmdir": true, "mv": true, "cp": true, "dd": true,
	"chmod": true, "chown": true, "chgrp": true, "chattr": true,
	"sudo": true, "su": true, "pkexec": true,
	"kill": true, "killall": true, "pkill": true,
	"shutdown": true, "reboot": true, "halt": true, "poweroff": true,
	"init": true, "telinit": true,
	"useradd": true, "userdel": true, "usermod": true,
	"groupadd": true, "groupdel": true, "groupmod": true,
	"passwd": true, "chpasswd": true,
	"mkfs": true, "mount": true, "umount": true, "fdisk": true, "parted": true,
	"bash": true, "sh": true, "zsh": true, "dash": true, "csh": true, "tcsh": true, "fish": true,
	"python": true, "python3": true, "python2": true,
	"perl": true, "ruby": true, "node": true, "php": true, "lua": true,
	"vi": true, "vim": true, "nano": true, "emacs": true, "pico": true,
	"tee": true, "install": true,
	"make": true, "gcc": true, "g++": true, "cc": true,
	"iptables": true, "ip6tables": true, "nft": true,
	"scp": true, "rsync": true, "sftp": true, "ftp": true, "wget": true,
	"crontab": true, "at": true, "batch": true,
	"systemctl-start": true, "systemctl-stop": true, "systemctl-restart": true,
}

// baseBlockedFlags are hardcoded blocked flags that can't be overridden.
var baseBlockedFlags = map[string][]string{
	"sed":  {"-i", "--in-place"},
	"curl": {"-X", "--request", "-d", "--data", "--data-raw", "--data-binary", "--data-urlencode", "-F", "--form", "-T", "--upload-file", "-o", "--output", "-O", "--remote-name", "-K", "--config", "-x", "--proxy"},
}

// baseSubcommandRestrictions are hardcoded restrictions that can't be removed.
var baseSubcommandRestrictions = map[string]map[string]bool{
	"systemctl": {
		"status": true, "show": true, "list-units": true,
		"is-active": true, "is-enabled": true,
	},
	"dpkg": {
		"-l": true, "--list": true, "-s": true, "--status": true,
	},
	"rpm": {
		"-qa": true, "-q": true,
	},
	"apt": {
		"list": true, "show": true,
	},
	"pip": {
		"list": true, "show": true,
	},
	"openssl": {
		"x509": true, "verify": true, "s_client": true, "crl": true,
		"version": true, "ciphers": true, "req": true,
	},
}

// Validator holds the merged allowlist configuration and validates commands.
type Validator struct {
	allowed            map[string]bool
	blockedFlags       map[string][]string
	subcommandRestrs   map[string]map[string]bool
	argValidators      map[string]func(tokens []string, allowed map[string]bool) error
}

// NewValidator creates a validator with the base rules plus optional user config.
// extraCommands are additional commands to allow (still subject to alwaysBlocked).
// extraSubcommandRestrictions adds subcommand limits for extra commands.
// extraBlockedFlags adds additional blocked flags.
func NewValidator(
	extraCommands []string,
	extraSubcommandRestrictions map[string]map[string]bool,
	extraBlockedFlags map[string][]string,
) *Validator {
	// Merge allowed commands: base + extras (minus always-blocked)
	allowed := make(map[string]bool, len(baseCommands)+len(extraCommands))
	for k, v := range baseCommands {
		allowed[k] = v
	}
	for _, cmd := range extraCommands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		if alwaysBlockedCommands[cmd] {
			continue // can't allow these
		}
		allowed[cmd] = true
	}

	// Merge blocked flags: base + extras
	blockedFlags := make(map[string][]string)
	for k, v := range baseBlockedFlags {
		blockedFlags[k] = append([]string{}, v...)
	}
	for k, v := range extraBlockedFlags {
		blockedFlags[k] = append(blockedFlags[k], v...)
	}

	// Merge subcommand restrictions: base + extras
	subcommandRestrs := make(map[string]map[string]bool)
	for k, v := range baseSubcommandRestrictions {
		subMap := make(map[string]bool, len(v))
		for sk, sv := range v {
			subMap[sk] = sv
		}
		subcommandRestrs[k] = subMap
	}
	for k, v := range extraSubcommandRestrictions {
		if _, exists := subcommandRestrs[k]; exists {
			// Adding restrictions to a base command: merge (restrict further)
			for sk, sv := range v {
				subcommandRestrs[k][sk] = sv
			}
		} else {
			// New command from config: set its restrictions
			subMap := make(map[string]bool, len(v))
			for sk, sv := range v {
				subMap[sk] = sv
			}
			subcommandRestrs[k] = subMap
		}
	}

	v := &Validator{
		allowed:          allowed,
		blockedFlags:     blockedFlags,
		subcommandRestrs: subcommandRestrs,
		argValidators: map[string]func(tokens []string, allowed map[string]bool) error{
			"xargs":   validateXargsCommand,
			"openssl": validateOpenSSLArgs,
		},
	}

	return v
}

// DefaultValidator returns a validator with only the base rules (no config).
func DefaultValidator() *Validator {
	return NewValidator(nil, nil, nil)
}

// ValidateCommand checks that every command in a pipeline is allowed.
func (v *Validator) ValidateCommand(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("empty command")
	}

	if err := checkDangerousMetacharacters(command); err != nil {
		return err
	}
	if containsUnquotedRedirection(command) {
		return fmt.Errorf("output redirection is not allowed in read-only mode")
	}

	segments := splitPipeline(command)
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}

		baseCmd := extractBaseCommand(seg)
		if baseCmd == "" {
			continue
		}

		// Check always-blocked first (hardcoded, can't be overridden)
		if alwaysBlockedCommands[baseCmd] {
			return fmt.Errorf("command %q is not allowed in read-only mode", baseCmd)
		}

		if !v.allowed[baseCmd] {
			return fmt.Errorf("command %q is not allowed in read-only mode", baseCmd)
		}

		if flags, ok := v.blockedFlags[baseCmd]; ok {
			tokens := tokenize(seg)
			for _, tok := range tokens[1:] {
				for _, blocked := range flags {
					if tok == blocked || strings.HasPrefix(tok, blocked) {
						return fmt.Errorf("%s flag %q is not allowed in read-only mode", baseCmd, blocked)
					}
				}
			}
		}

		if restrictions, ok := v.subcommandRestrs[baseCmd]; ok {
			subCmd := extractSubcommand(seg, baseCmd)
			if subCmd != "" && !restrictions[subCmd] {
				return fmt.Errorf("%s subcommand %q is not allowed in read-only mode", baseCmd, subCmd)
			}
		}

		if validator, ok := v.argValidators[baseCmd]; ok {
			tokens := tokenize(seg)
			if err := validator(tokens, v.allowed); err != nil {
				return err
			}
		}
	}
	return nil
}

// AllowedCommandsList returns a sorted slice of all allowed command names.
func (v *Validator) AllowedCommandsList() []string {
	cmds := make([]string, 0, len(v.allowed))
	for k := range v.allowed {
		cmds = append(cmds, k)
	}
	sort.Strings(cmds)
	return cmds
}

// IsAlwaysBlocked returns true if a command is in the hardcoded blocklist.
func IsAlwaysBlocked(cmd string) bool {
	return alwaysBlockedCommands[cmd]
}

// BaseCommandsList returns the hardcoded base command list.
func BaseCommandsList() []string {
	cmds := make([]string, 0, len(baseCommands))
	for k := range baseCommands {
		cmds = append(cmds, k)
	}
	sort.Strings(cmds)
	return cmds
}

// Standalone functions for backward compatibility

var defaultValidator = DefaultValidator()

// ValidateCommand checks a command using the default validator.
func ValidateCommand(command string) error {
	return defaultValidator.ValidateCommand(command)
}

// ValidateCommandWithExtra checks with additional commands using a temporary validator.
func ValidateCommandWithExtra(command string, extraAllowed []string) error {
	v := NewValidator(extraAllowed, nil, nil)
	return v.ValidateCommand(command)
}

// AllowedCommandsList returns the default allowed commands.
func AllowedCommandsList() []string {
	return defaultValidator.AllowedCommandsList()
}

// Internal validation functions

func validateXargsCommand(tokens []string, allowed map[string]bool) error {
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		base := tok
		if idx := strings.LastIndex(tok, "/"); idx >= 0 {
			base = tok[idx+1:]
		}
		if !allowed[base] {
			return fmt.Errorf("xargs command %q is not allowed in read-only mode", base)
		}
		return nil
	}
	return nil
}

func validateOpenSSLArgs(tokens []string, allowed map[string]bool) error {
	var subCmd string
	for _, tok := range tokens[1:] {
		if !strings.HasPrefix(tok, "-") {
			subCmd = tok
			break
		}
	}
	if subCmd == "req" {
		for _, tok := range tokens {
			if tok == "-new" || tok == "-signkey" || tok == "-x509" {
				return fmt.Errorf("openssl req %s is not allowed in read-only mode", tok)
			}
		}
	}
	if subCmd == "s_client" {
		for i, tok := range tokens {
			if tok == "-proxy" {
				return fmt.Errorf("openssl s_client -proxy is not allowed in read-only mode")
			}
			if tok == "-connect" && i+1 < len(tokens) {
				hostPort := tokens[i+1]
				var host string
				if strings.HasPrefix(hostPort, "[") {
					if end := strings.Index(hostPort, "]"); end >= 0 {
						host = hostPort[1:end]
					} else {
						host = strings.TrimPrefix(hostPort, "[")
					}
				} else {
					host = hostPort
					if idx := strings.LastIndex(hostPort, ":"); idx >= 0 {
						host = hostPort[:idx]
					}
				}
				if host != "localhost" && host != "127.0.0.1" && host != "::1" && host != "" {
					return fmt.Errorf("openssl s_client -connect only allowed to localhost, got %q", host)
				}
			}
		}
	}
	return nil
}

var envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func checkDangerousMetacharacters(s string) error {
	inSingle := false
	inDouble := false
	prev := rune(0)
	runes := []rune(s)

	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		switch {
		case ch == '\'' && !inDouble && prev != '\\':
			inSingle = !inSingle
		case ch == '"' && !inSingle && prev != '\\':
			inDouble = !inDouble
		case !inSingle && !inDouble:
			if ch == '$' && i+1 < len(runes) && runes[i+1] == '(' {
				return fmt.Errorf("command substitution $(...) is not allowed in read-only mode")
			}
			if ch == '`' {
				return fmt.Errorf("backtick command substitution is not allowed in read-only mode")
			}
			if (ch == '<' || ch == '>') && i+1 < len(runes) && runes[i+1] == '(' {
				return fmt.Errorf("process substitution is not allowed in read-only mode")
			}
			if ch == '\n' || ch == '\r' {
				return fmt.Errorf("newline characters are not allowed in read-only mode")
			}
		}
		prev = ch
	}
	return nil
}

func containsUnquotedRedirection(s string) bool {
	inSingle := false
	inDouble := false
	prev := rune(0)
	for _, ch := range s {
		switch {
		case ch == '\'' && !inDouble && prev != '\\':
			inSingle = !inSingle
		case ch == '"' && !inSingle && prev != '\\':
			inDouble = !inDouble
		case ch == '>' && !inSingle && !inDouble:
			return true
		}
		prev = ch
	}
	return false
}

func splitPipeline(s string) []string {
	var segments []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	prev := rune(0)
	runes := []rune(s)

	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		switch {
		case ch == '\'' && !inDouble && prev != '\\':
			inSingle = !inSingle
			current.WriteRune(ch)
		case ch == '"' && !inSingle && prev != '\\':
			inDouble = !inDouble
			current.WriteRune(ch)
		case ch == '|' && !inSingle && !inDouble:
			if i+1 < len(runes) && runes[i+1] == '|' {
				segments = append(segments, current.String())
				current.Reset()
				i++
			} else {
				segments = append(segments, current.String())
				current.Reset()
			}
		case ch == ';' && !inSingle && !inDouble:
			segments = append(segments, current.String())
			current.Reset()
		case ch == '&' && !inSingle && !inDouble:
			if i+1 < len(runes) && runes[i+1] == '&' {
				segments = append(segments, current.String())
				current.Reset()
				i++
			} else {
				current.WriteRune(ch)
			}
		default:
			current.WriteRune(ch)
		}
		prev = ch
	}
	if current.Len() > 0 {
		segments = append(segments, current.String())
	}
	return segments
}

func extractBaseCommand(seg string) string {
	tokens := tokenize(seg)
	for _, tok := range tokens {
		if envAssignRe.MatchString(tok) {
			continue
		}
		base := tok
		if idx := strings.LastIndex(tok, "/"); idx >= 0 {
			base = tok[idx+1:]
		}
		return base
	}
	return ""
}

func extractSubcommand(seg, baseCmd string) string {
	tokens := tokenize(seg)
	foundBase := false
	for _, tok := range tokens {
		if !foundBase {
			if envAssignRe.MatchString(tok) {
				continue
			}
			base := tok
			if idx := strings.LastIndex(tok, "/"); idx >= 0 {
				base = tok[idx+1:]
			}
			if base == baseCmd {
				foundBase = true
				continue
			}
		} else {
			return tok
		}
	}
	return ""
}

func tokenize(s string) []string {
	var tokens []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	prev := rune(0)

	for _, ch := range s {
		switch {
		case ch == '\'' && !inDouble && prev != '\\':
			inSingle = !inSingle
		case ch == '"' && !inSingle && prev != '\\':
			inDouble = !inDouble
		case (ch == ' ' || ch == '\t') && !inSingle && !inDouble:
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(ch)
		}
		prev = ch
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}
