package guard

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/aspectrr/lily/internal/cloud"
)

// RewriteResult is returned by Rewrite.
type RewriteResult struct {
	// Rewritten is the new command to execute. Empty if no rewrite applies.
	Rewritten string
	// Host is the extracted SSH host alias.
	Host string
	// RemoteCommand is the command that would run on the remote.
	RemoteCommand string
	// Decision indicates what action to take.
	// "rewrite" — rewrite the command to use lily run
	// "block" — the command is blocked (e.g., interactive SSH)
	// "passthrough" — no SSH detected, do nothing
	Decision string
	// Reason is a human-readable explanation.
	Reason string
}

// Rewrite inspects a bash command string for SSH usage patterns.
// If it detects SSH, it rewrites the command to use `lily run <host> <command>`.
// If no SSH pattern is found, it returns Decision "passthrough".
func Rewrite(command string) RewriteResult {
	command = strings.TrimSpace(command)
	if command == "" {
		return RewriteResult{Decision: "passthrough"}
	}

	// Skip if already using lily
	if strings.HasPrefix(command, "lily ") {
		return RewriteResult{Decision: "passthrough"}
	}

	// Split compound commands on shell operators and check each segment.
	for _, segment := range splitShellOperators(command) {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}

		firstCmd := extractFirstCommand(segment)
		if firstCmd == "" {
			continue
		}

		switch firstCmd {
		case "ssh":
			return rewriteSSH(segment)
		case "scp":
			return rewriteSCP(segment)
		default:
			// Check for cloud CLI SSH commands (aws, gcloud, az) and kubectl exec
			if provider, rewritten, detected := cloud.DetectCloudSSH(segment); detected {
				var desc string
				if provider == cloud.Kubectl {
					desc = fmt.Sprintf("%s exec command rewritten to lily (read-only command validation)", provider)
				} else {
					desc = fmt.Sprintf("%s SSH command rewritten to lily (read-only command validation)", provider)
				}
				return RewriteResult{
					Decision:  "rewrite",
					Rewritten: rewritten,
					Reason:    desc,
				}
			}

			// Check for rsync -e ssh
			if firstCmd == "rsync" && containsRsyncSSH(segment) {
				return rewriteRsync(segment)
			}
		}
	}

	return RewriteResult{Decision: "passthrough"}
}

// splitShellOperators splits a command string on shell operator boundaries
// (&&, ||, ;, |, &) while respecting single and double quotes.
func splitShellOperators(s string) []string {
	var segments []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(s); i++ {
		ch := s[i]

		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
			current.WriteByte(ch)
		case ch == '"' && !inSingle:
			inDouble = !inDouble
			current.WriteByte(ch)
		case !inSingle && !inDouble:
			// Check for shell operators
			if ch == '&' && i+1 < len(s) && s[i+1] == '&' {
				segments = append(segments, current.String())
				current.Reset()
				i++ // skip second '&'
			} else if ch == '|' && i+1 < len(s) && s[i+1] == '|' {
				segments = append(segments, current.String())
				current.Reset()
				i++ // skip second '|'
			} else if ch == ';' || ch == '|' || ch == '&' {
				segments = append(segments, current.String())
				current.Reset()
			} else {
				current.WriteByte(ch)
			}
		default:
			current.WriteByte(ch)
		}
	}

	if current.Len() > 0 {
		segments = append(segments, current.String())
	}

	return segments
}

// rewriteSSH handles ssh command patterns:
//
//	ssh [flags] host [command]
//	ssh [flags] user@host [command]
func rewriteSSH(command string) RewriteResult {
	tokens := tokenize(command)
	if len(tokens) == 0 {
		return RewriteResult{Decision: "passthrough"}
	}

	// Skip env var assignments (KEY=val) to find "ssh"
	envAssignRe := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	sshIdx := -1
	for idx, tok := range tokens {
		if envAssignRe.MatchString(tok) {
			continue
		}
		base := tok
		if i := strings.LastIndex(tok, "/"); i >= 0 {
			base = tok[i+1:]
		}
		// Strip leading backslash shell escapes (\ssh → ssh)
		base = strings.TrimLeft(base, "\\")
		if base == "ssh" {
			sshIdx = idx
		}
		break
	}
	if sshIdx < 0 {
		return RewriteResult{Decision: "passthrough"}
	}

	// Everything after the "ssh" token
	args := tokens[sshIdx+1:]

	host := ""
	remoteCmd := ""
	userPrefix := ""

	// Parse flags and find the host
	i := 0
	for i < len(args) {
		arg := args[i]
		switch {
		case arg == "-p" && i+1 < len(args):
			// -p port — skip both
			i += 2
		case strings.HasPrefix(arg, "-p"):
			// -pPORT (no space)
			i++
		case arg == "-i" && i+1 < len(args):
			// -i identity — skip both
			i += 2
		case arg == "-o" && i+1 < len(args):
			// -o option — skip both
			i += 2
		case arg == "-l" && i+1 < len(args):
			// -l login_name — skip both, remember user
			userPrefix = args[i+1] + "@"
			i += 2
		case strings.HasPrefix(arg, "-"):
			// Other flags (-v, -q, -T, -t, -n, -N, -f, etc.)
			// Some flags take arguments
			if takesSSHArg(arg) != "" && i+1 < len(args) {
				i += 2
			} else {
				i++
			}
		default:
			// First non-flag argument is the host
			host = arg
			i++
			goto foundHost
		}
	}

	// No host found (e.g., just `ssh`) — rewrite to bare lily ssh
	return RewriteResult{
		Decision:  "rewrite",
		Rewritten: "lily ssh",
		Reason:    "SSH session rewritten to lily ssh (restricted shell)",
	}

foundHost:
	// Clean up user@host — extract just the host alias
	cleanHost := host
	if idx := strings.Index(host, "@"); idx >= 0 {
		cleanHost = host[idx+1:]
	}
	if userPrefix != "" {
		// -l flag overrides @host user
		host = userPrefix + cleanHost
	}

	// Everything after host is the remote command
	if i < len(args) {
		remoteCmd = strings.Join(args[i:], " ")
	}

	// No remote command = interactive SSH session — rewrite to lily ssh
	if remoteCmd == "" {
		return RewriteResult{
			Decision:      "rewrite",
			Rewritten:     fmt.Sprintf("lily ssh %s", cleanHost),
			Host:          cleanHost,
			Reason:        fmt.Sprintf("SSH to %q rewritten to lily ssh (restricted shell)", cleanHost),
			RemoteCommand: "",
		}
	}

	// Rewrite to lily run
	return RewriteResult{
		Decision:      "rewrite",
		Rewritten:     fmt.Sprintf("lily run %s %s", cleanHost, shellArg(remoteCmd)),
		Host:          cleanHost,
		RemoteCommand: remoteCmd,
		Reason:        fmt.Sprintf("SSH to %q rewritten to lily run", cleanHost),
	}
}

// rewriteSCP blocks scp — lily is read-only.
func rewriteSCP(command string) RewriteResult {
	tokens := tokenize(command)
	host := ""
	for _, tok := range tokens[1:] {
		if strings.Contains(tok, ":") {
			// scp source or dest like host:path or user@host:path
			parts := strings.SplitN(tok, ":", 2)
			hostPart := parts[0]
			if idx := strings.Index(hostPart, "@"); idx >= 0 {
				hostPart = hostPart[idx+1:]
			}
			host = hostPart
			break
		}
	}
	reason := "scp is blocked by lily guard (lily is read-only — use lily run for remote commands)"
	if host != "" {
		reason = fmt.Sprintf("scp to %q is blocked by lily guard (lily is read-only)", host)
	}
	return RewriteResult{
		Decision: "block",
		Host:     host,
		Reason:   reason,
	}
}

// rewriteRsync blocks rsync -e ssh — lily is read-only.
func rewriteRsync(command string) RewriteResult {
	return RewriteResult{
		Decision: "block",
		Reason:   "rsync over SSH is blocked by lily guard (lily is read-only — use lily run for remote commands)",
	}
}

// containsRsyncSSH checks whether an rsync command uses SSH as the remote shell.
// It handles all flag forms: -e ssh, -essh, -e "ssh", --rsh=ssh, --rsh ssh.
func containsRsyncSSH(command string) bool {
	tokens := tokenize(command)
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		var val string
		switch {
		case tok == "-e" && i+1 < len(tokens):
			val = tokens[i+1]
		case strings.HasPrefix(tok, "-e"):
			val = strings.TrimPrefix(tok, "-e")
		case strings.HasPrefix(tok, "--rsh="):
			val = strings.TrimPrefix(tok, "--rsh=")
		case tok == "--rsh" && i+1 < len(tokens):
			val = tokens[i+1]
		default:
			continue
		}
		val = strings.Trim(val, "\"'")
		base := val
		if idx := strings.LastIndex(val, "/"); idx >= 0 {
			base = val[idx+1:]
		}
		if base == "ssh" {
			return true
		}
	}
	return false
}

// extractFirstCommand returns the first command token from a shell command,
// skipping any leading VAR=value environment variable assignments.
func extractFirstCommand(command string) string {
	tokens := tokenize(command)
	envAssignRe := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	for _, tok := range tokens {
		if envAssignRe.MatchString(tok) {
			continue
		}
		// Strip path prefix
		if idx := strings.LastIndex(tok, "/"); idx >= 0 {
			tok = tok[idx+1:]
		}
		// Strip leading backslash shell escapes (\ssh → ssh)
		tok = strings.TrimLeft(tok, "\\")
		return tok
	}
	return ""
}

// tokenize splits a shell command string into tokens, respecting single and
// double quotes. This is a simplified tokenizer — it doesn't handle all shell
// edge cases, just enough for SSH command parsing.
func tokenize(s string) []string {
	var tokens []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for _, ch := range s {
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
		case ch == '"' && !inSingle:
			inDouble = !inDouble
		case (ch == ' ' || ch == '\t') && !inSingle && !inDouble:
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(ch)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// takesSSHArg returns true if an SSH flag requires an argument.
func takesSSHArg(flag string) string {
	if !strings.HasPrefix(flag, "-") {
		return ""
	}
	// Long options
	if strings.HasPrefix(flag, "--") {
		name := flag[2:]
		// --option=value style
		if strings.Contains(name, "=") {
			return ""
		}
		switch name {
		case "bind-address", "config", "D", "E", "e", "F", "I", "i",
			"J", "L", "l", "M", "m", "O", "o", "p", "Q", "R",
			"S", "W", "w":
			return name
		}
		return ""
	}
	// Short options: handle combined flags like -vikey
	// The last character determines if it takes an arg
	cleanFlag := strings.TrimLeft(flag, "-")
	if len(cleanFlag) == 0 {
		return ""
	}
	lastChar := cleanFlag[len(cleanFlag)-1]
	switch lastChar {
	case 'b', 'B', 'c', 'D', 'E', 'e', 'F', 'I', 'i', 'J', 'L',
		'l', 'M', 'm', 'O', 'o', 'p', 'Q', 'R', 'S', 'W', 'w':
		return string(lastChar)
	}
	return ""
}

// shellArg quotes a string for safe shell use if needed.
func shellArg(s string) string {
	if s == "" {
		return "''"
	}
	// Check if it needs quoting
	needsQuote := false
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
			needsQuote = true
		}
	}
	if !needsQuote {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
