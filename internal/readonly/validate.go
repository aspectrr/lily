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
	"date": true, "which": true,
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
	"find": {"-exec", "-execdir", "-ok", "-okdir"},
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
	allowed          map[string]bool
	blockedFlags     map[string][]string
	subcommandRestrs map[string]map[string]bool
	argValidators    map[string]func(tokens []string, allowed map[string]bool) error
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
			"awk":     validateAwkArgs,
			"sed":     validateSedArgs,
			"curl":    validateCurlArgs,
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

	// Block environment variable assignments (e.g., FOO=bar cmd)
	// These can alter command behavior in unpredictable ways (LD_PRELOAD, PAGER, etc.)
	if containsEnvAssignment(command) {
		return fmt.Errorf("environment variable assignments are not allowed in read-only mode")
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

// SanitizeCommand reconstructs a validated command with safe shell quoting
// to prevent the remote shell from interpreting any special characters.
// This is a defense-in-depth measure: even if the validator has a blind spot,
// the reconstructed command is safe because all arguments are single-quoted.
//
// Must be called AFTER ValidateCommand succeeds.
func (v *Validator) SanitizeCommand(command string) (string, error) {
	segments := splitPipelineWithOps(command)
	var parts []string

	for _, seg := range segments {
		text := strings.TrimSpace(seg.text)
		if text == "" {
			continue
		}

		tokens := tokenize(text)
		if len(tokens) == 0 {
			continue
		}

		var safeTokens []string
		for _, tok := range tokens {
			// Skip env assignments (should have been rejected by validator, but defense in depth)
			if envAssignRe.MatchString(tok) {
				continue
			}
			if len(safeTokens) == 0 {
				// First non-env token is the command — keep as-is
				safeTokens = append(safeTokens, tok)
			} else {
				// All arguments: quote if they contain any shell-special characters
				safeTokens = append(safeTokens, shellQuote(tok))
			}
		}

		if len(safeTokens) > 0 {
			parts = append(parts, strings.Join(safeTokens, " "))
			if seg.operator != "" {
				parts = append(parts, seg.operator)
			}
		}
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("empty command after sanitization")
	}

	return strings.Join(parts, " "), nil
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

// SanitizeCommand reconstructs a validated command with safe shell quoting (default validator).
func SanitizeCommand(command string) (string, error) {
	return defaultValidator.SanitizeCommand(command)
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

func validateAwkArgs(tokens []string, allowed map[string]bool) error {
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		lower := strings.ToLower(tok)
		// Block command execution constructs in awk programs
		if strings.Contains(lower, "system(") || strings.Contains(lower, "system (") {
			return fmt.Errorf("awk system() is not allowed in read-only mode")
		}
		// Block piping from awk to other commands
		if strings.Contains(tok, "| ") || strings.Contains(tok, "|\"") {
			return fmt.Errorf("awk pipe to command is not allowed in read-only mode")
		}
		// Block coproc
		if strings.Contains(lower, "coproc") {
			return fmt.Errorf("awk coproc is not allowed in read-only mode")
		}
	}
	return nil
}

// sedWriteRe matches the sed `w` (write-to-file) command when `w` appears
// immediately after a line-number address or closing brace, with no space separator.
// For example: `1wfoo`, `$wout.txt`, `2w.ssh/keys`.
// sedSubstWriteRe matches `w` used as a substitution write flag: `s/a/b/wfile`
// (w after the closing delimiter of a substitution command).
// These complement the strings.Contains checks which handle `w /path` etc.
var sedWriteRe = regexp.MustCompile(`[0-9$\}]w[^\s\;/\}]+`)
var sedSubstWriteRe = regexp.MustCompile(`s/[^/]*/[^/]*/w\S`)

func validateSedArgs(tokens []string, allowed map[string]bool) error {
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		// Block sed write-to-file commands:
		// - w /path, w\t/path (w followed by whitespace)
		// - w/out (w followed by /)
		// - 1wfoo, $wout.txt (w after line-number address with no separator)
		// - s/a/b/wbar (w as substitution write flag)
		if strings.Contains(tok, "w ") || strings.Contains(tok, "w\t") || strings.Contains(tok, "w/") {
			return fmt.Errorf("sed file write is not allowed in read-only mode")
		}
		if sedWriteRe.MatchString(tok) {
			return fmt.Errorf("sed file write is not allowed in read-only mode")
		}
		if sedSubstWriteRe.MatchString(tok) {
			return fmt.Errorf("sed file write is not allowed in read-only mode")
		}
	}
	return nil
}

// blockedCurlHosts are hostnames that curl must never reach (cloud metadata services).
var blockedCurlHosts = map[string]bool{
	"169.254.169.254":          true, // AWS/GCP/Azure/OpenStack metadata
	"100.100.100.200":          true, // Alibaba Cloud metadata
	"metadata.google.internal": true, // GCP metadata
	"metadata.internal":        true, // Generic cloud metadata
}

func validateCurlArgs(tokens []string, allowed map[string]bool) error {
	// Extract URL from curl arguments: first non-flag token
	var urlStr string
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if strings.HasPrefix(tok, "-") {
			// Skip flags that take a value argument
			switch tok {
			case "-H", "-u", "-A", "-e", "--cacert", "--capath", "--cert", "--key",
				"--connect-timeout", "--max-time", "--retry", "-m",
				"--interface", "--resolve", "--limit-rate":
				i++ // skip next token (flag value)
			}
			continue
		}
		urlStr = tok
		break
	}

	if urlStr == "" {
		return nil // No URL found
	}

	// Parse host from URL
	host := extractHostFromURL(urlStr)
	if host == "" {
		return nil
	}

	// Check blocked hostnames
	lowerHost := strings.ToLower(host)
	if blockedCurlHosts[lowerHost] || blockedCurlHosts[host] {
		return fmt.Errorf("curl to cloud metadata endpoint %q is not allowed in read-only mode", host)
	}

	// Check blocked IP ranges (169.254.0.0/16 link-local, includes cloud metadata)
	if strings.HasPrefix(host, "169.254.") {
		return fmt.Errorf("curl to link-local/metadata IP %q is not allowed in read-only mode", host)
	}
	if strings.HasPrefix(host, "100.100.") {
		return fmt.Errorf("curl to cloud metadata IP %q is not allowed in read-only mode", host)
	}

	return nil
}

// extractHostFromURL extracts the hostname from a URL string.
func extractHostFromURL(urlStr string) string {
	s := urlStr
	// Remove scheme
	if strings.Contains(s, "://") {
		_, after, ok := strings.Cut(s, "://")
		if ok {
			s = after
		}
	}
	// Extract host:port part (everything before first /)
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	// Handle [ipv6]:port
	if strings.HasPrefix(s, "[") {
		if end := strings.Index(s, "]"); end >= 0 {
			return s[1:end]
		}
	}
	// Remove :port
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		s = s[:idx]
	}
	// Remove userinfo (user:pass@host)
	if idx := strings.LastIndex(s, "@"); idx >= 0 {
		s = s[idx+1:]
	}
	return s
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

// isShellVarChar returns true for characters that can appear in a shell variable name
// or represent a special shell variable ($0-$9, $$, $!, $?, $#, $@, $*, $-).
func isShellVarChar(ch rune) bool {
	switch {
	case ch >= 'a' && ch <= 'z':
	case ch >= 'A' && ch <= 'Z':
	case ch >= '0' && ch <= '9':
	case ch == '_' || ch == '!' || ch == '?' || ch == '#':
	case ch == '@' || ch == '*' || ch == '-' || ch == '$':
	default:
		return false
	}
	return true
}

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
			// Unquoted context: block all dangerous constructs
			if ch == '$' && i+1 < len(runes) {
				next := runes[i+1]
				switch {
				case next == '(':
					return fmt.Errorf("command substitution $(...) is not allowed in read-only mode")
				case next == '{':
					return fmt.Errorf("parameter expansion ${...} is not allowed in read-only mode")
				case isShellVarChar(next):
					return fmt.Errorf("variable expansion is not allowed in read-only mode")
				}
			}
			if ch == '`' {
				return fmt.Errorf("backtick command substitution is not allowed in read-only mode")
			}
			if (ch == '<' || ch == '>') && i+1 < len(runes) && runes[i+1] == '(' {
				return fmt.Errorf("process substitution is not allowed in read-only mode")
			}
			// Block input redirection <, here-docs <<, here-strings <<<
			if ch == '<' {
				if i+1 < len(runes) && runes[i+1] == '<' {
					return fmt.Errorf("here-doc/here-string is not allowed in read-only mode")
				}
				return fmt.Errorf("input redirection is not allowed in read-only mode")
			}
			// Block subshell parentheses
			if ch == '(' || ch == ')' {
				return fmt.Errorf("subshells are not allowed in read-only mode")
			}
			// Block backslash before shell-meaningful characters
			if ch == '\\' && i+1 < len(runes) {
				next := runes[i+1]
				switch next {
				case ' ', '\t', ';', '|', '&', '(', ')', '<', '>', '`', '$', '"', '\'', '\\', '\n', '#', '?', '*', '~':
					return fmt.Errorf("backslash escaping is not allowed in read-only mode")
				}
			}
			if ch == '\n' || ch == '\r' {
				return fmt.Errorf("newline characters are not allowed in read-only mode")
			}
		case !inSingle && inDouble:
			// Inside double quotes: $(...), ${...}, backticks, and $var are STILL expanded by the shell.
			if ch == '$' && i+1 < len(runes) {
				next := runes[i+1]
				switch {
				case next == '(':
					return fmt.Errorf("command substitution $(...) inside double quotes is not allowed in read-only mode")
				case next == '{':
					return fmt.Errorf("parameter expansion ${...} inside double quotes is not allowed in read-only mode")
				case isShellVarChar(next):
					return fmt.Errorf("variable expansion inside double quotes is not allowed in read-only mode")
				}
			}
			if ch == '`' {
				return fmt.Errorf("backtick command substitution inside double quotes is not allowed in read-only mode")
			}
		default:
			// Inside single quotes: everything is literal, nothing is expanded. Safe.
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

// containsEnvAssignment checks if the command contains VAR=value assignments
// outside of quotes. These can alter command behavior (LD_PRELOAD, PAGER, etc.)
func containsEnvAssignment(s string) bool {
	tokens := tokenize(s)
	for _, tok := range tokens {
		if envAssignRe.MatchString(tok) {
			return true
		}
		// Once we hit a non-assignment, non-flag token, stop checking
		if !strings.HasPrefix(tok, "-") {
			break
		}
	}
	return false
}

// pipelineSegment represents a command segment and the operator that follows it.
type pipelineSegment struct {
	text     string
	operator string // "|", ";", "&&", "||", or "" for the last segment
}

func splitPipelineWithOps(s string) []pipelineSegment {
	var segments []pipelineSegment
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
				segments = append(segments, pipelineSegment{text: current.String(), operator: "||"})
				current.Reset()
				i++
			} else {
				segments = append(segments, pipelineSegment{text: current.String(), operator: "|"})
				current.Reset()
			}
		case ch == ';' && !inSingle && !inDouble:
			segments = append(segments, pipelineSegment{text: current.String(), operator: ";"})
			current.Reset()
		case ch == '&' && !inSingle && !inDouble:
			if i+1 < len(runes) && runes[i+1] == '&' {
				segments = append(segments, pipelineSegment{text: current.String(), operator: "&&"})
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
		segments = append(segments, pipelineSegment{text: current.String(), operator: ""})
	}
	return segments
}

func splitPipeline(s string) []string {
	ops := splitPipelineWithOps(s)
	result := make([]string, len(ops))
	for i, seg := range ops {
		result[i] = seg.text
	}
	return result
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

// shellQuote wraps a string in single quotes, escaping any embedded single quotes.
// In single quotes, the shell treats EVERYTHING as literal — no expansion at all.
func shellQuote(s string) string {
	if isSafeShellArg(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isSafeShellArg returns true if the string contains only characters that
// the shell treats as literal in unquoted context.
func isSafeShellArg(s string) bool {
	for _, ch := range s {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '-' || ch == '_' || ch == '.' || ch == '/':
		case ch == ':' || ch == '=' || ch == '@' || ch == '+':
		case ch == '[' || ch == ']' || ch == ',' || ch == '%':
		case ch == '?' || ch == '#':
		default:
			return false
		}
	}
	return true
}
