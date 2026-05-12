// Package memory provides automatic investigation tracking for Lily.
// It silently captures debugging sessions, extracts keywords from command
// output, and surfaces relevant past investigations when similar issues arise.
//
// No daemon is required. Each Lily invocation checks a session lock file
// to determine if it's continuing an active session or starting a new one.
// Investigations are flushed to disk when a session times out (no commands
// for a configurable period, default 10 minutes).
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultSessionTimeout is the default time with no activity before an
// investigation is considered complete and flushed to disk.
const DefaultSessionTimeout = 10 * time.Minute

// DefaultMaxInvestigations is the max investigations kept per host pair.
const DefaultMaxInvestigations = 50

// Config holds the memory feature configuration.
type Config struct {
	Enabled                  bool          `yaml:"enabled"`
	SessionTimeout           string        `yaml:"session_timeout"`
	MaxInvestigationsPerHost int           `yaml:"max_investigations_per_host"`
	sessionTimeout           time.Duration `yaml:"-"`
}

// ParseSessionTimeout parses and returns the session timeout duration.
func (c *Config) ParseSessionTimeout() time.Duration {
	if c.sessionTimeout != 0 {
		return c.sessionTimeout
	}
	if c.SessionTimeout == "" {
		return DefaultSessionTimeout
	}
	d, err := time.ParseDuration(c.SessionTimeout)
	if err != nil {
		return DefaultSessionTimeout
	}
	return d
}

// MemoryDir returns the directory where memory files are stored.
func MemoryDir() string {
	if configDir := os.Getenv("XDG_CONFIG_HOME"); configDir != "" {
		return filepath.Join(configDir, "lily", "memory")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "lily", "memory")
	}
	return filepath.Join(home, ".config", "lily", "memory")
}

// CommandEntry records a single command run during an investigation.
type CommandEntry struct {
	Command   string   `yaml:"command"`
	Host      string   `yaml:"host"`
	Keywords  []string `yaml:"keywords,omitempty"`
	Timestamp string   `yaml:"timestamp"`
}

// Investigation represents a completed debugging session.
type Investigation struct {
	Hosts          []string       `yaml:"hosts"`
	Trigger        string         `yaml:"trigger_command"`
	TriggerSignals []string       `yaml:"trigger_signals,omitempty"`
	Commands       []CommandEntry `yaml:"commands"`
	Keywords       []string       `yaml:"keywords"`
	RootCauseHint  string         `yaml:"root_cause_hint,omitempty"`
	StartTime      time.Time      `yaml:"start_time"`
	EndTime        time.Time      `yaml:"end_time"`
}

// Session tracks an active investigation in progress.
// It is written to a lock file on every command so that state
// survives across process invocations (Lily is stateless CLI).
type Session struct {
	StartTime      time.Time      `yaml:"start_time"`
	LastCommand    time.Time      `yaml:"last_command"`
	Hosts          []string       `yaml:"hosts"`
	Trigger        string         `yaml:"trigger_command"`
	TriggerSignals []string       `yaml:"trigger_signals,omitempty"`
	Commands       []CommandEntry `yaml:"commands"`
	Keywords       []string       `yaml:"keywords"`
}

// Tracker manages session tracking and investigation persistence.
type Tracker struct {
	mu      sync.Mutex
	config  *Config
	dir     string
	session *Session
}

// NewTracker creates a new memory tracker.
func NewTracker(cfg *Config) *Tracker {
	if cfg == nil {
		cfg = &Config{Enabled: false}
	}
	return &Tracker{
		config: cfg,
		dir:    MemoryDir(),
	}
}

// RecordCommand records a command execution in the current session.
// If no active session exists or the session has timed out, the previous
// session is flushed as a completed investigation and a new session starts.
// Returns past investigation hints relevant to the current context.
func (t *Tracker) RecordCommand(host, command, output string) *Hint {
	if !t.config.Enabled {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	keywords := ExtractKeywords(output)

	// Check for existing session
	sessionPath := t.sessionLockPath()
	session := t.loadSession(sessionPath)
	timeout := t.config.ParseSessionTimeout()

	if session != nil && now.Sub(session.LastCommand) > timeout {
		// Session timed out — flush it as a completed investigation
		t.flushInvestigation(session)
		session = nil
	}

	if session == nil {
		// Start new session
		session = &Session{
			StartTime:      now,
			LastCommand:    now,
			Trigger:        command,
			TriggerSignals: keywords,
		}
	}

	// Update session
	session.LastCommand = now
	session.Commands = append(session.Commands, CommandEntry{
		Command:   command,
		Host:      host,
		Keywords:  keywords,
		Timestamp: now.Format(time.RFC3339),
	})

	// Track keywords at session level
	session.Keywords = mergeKeywords(session.Keywords, keywords)

	// Track host
	if !containsString(session.Hosts, host) {
		session.Hosts = append(session.Hosts, host)
	}

	// Persist session
	t.saveSession(sessionPath, session)
	t.session = session

	// Look up similar past investigations
	return t.findHint(host, command, keywords)
}

// Flush forces the current session to be flushed as a completed investigation.
// Useful for testing or explicit session end.
func (t *Tracker) Flush() {
	t.mu.Lock()
	defer t.mu.Unlock()

	sessionPath := t.sessionLockPath()
	session := t.loadSession(sessionPath)
	if session != nil {
		t.flushInvestigation(session)
		os.Remove(sessionPath)
		t.session = nil
	}
}

// Hint represents a relevant past investigation surfaced to the agent.
type Hint struct {
	Investigation *Investigation
	Similarity    float64
}

// FormatHint formats a hint for display in a tool response.
func (h *Hint) FormatHint() string {
	if h == nil || h.Investigation == nil || h.Similarity < 0.3 {
		return ""
	}

	inv := h.Investigation
	pct := int(h.Similarity * 100)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n━━ Past Investigation (%s, %d%% similar) ━━\n",
		inv.StartTime.Format("Jan 2, 2006 15:04"), pct))

	if inv.RootCauseHint != "" {
		b.WriteString(fmt.Sprintf("Root cause: %s\n", inv.RootCauseHint))
	}

	if len(inv.Hosts) > 1 {
		b.WriteString(fmt.Sprintf("Hosts involved: %s\n", strings.Join(inv.Hosts, " → ")))
	}

	// Show the investigation path (commands, deduplicated by host)
	b.WriteString("Investigation path:")
	seen := make(map[string]bool)
	for _, cmd := range inv.Commands {
		key := cmd.Host + ":" + cmd.Command
		if !seen[key] {
			seen[key] = true
			b.WriteString(fmt.Sprintf("\n  %s: %s", cmd.Host, cmd.Command))
		}
	}

	b.WriteString("\n")

	return b.String()
}

// findHint searches past investigations for ones similar to the current context.
func (t *Tracker) findHint(host, command string, keywords []string) *Hint {
	investigations := t.loadInvestigations()
	if len(investigations) == 0 {
		return nil
	}

	var best *Hint
	bestScore := 0.0

	cmdBase := extractCommandBase(command)

	for _, inv := range investigations {
		score := computeSimilarity(host, command, cmdBase, keywords, inv)
		if score > bestScore && score >= 0.3 {
			bestScore = score
			best = &Hint{
				Investigation: inv,
				Similarity:    score,
			}
		}
	}

	return best
}

// computeSimilarity calculates how similar the current context is to a past investigation.
// Uses weighted heuristic: host match (40%) + trigger overlap (35%) + keyword overlap (25%).
func computeSimilarity(host, command, cmdBase string, keywords []string, inv *Investigation) float64 {
	// Host match (0 or 1)
	hostScore := 0.0
	if containsString(inv.Hosts, host) {
		hostScore = 1.0
	}

	// Trigger overlap: does the current command base match the trigger command base?
	triggerScore := 0.0
	invTriggerBase := extractCommandBase(inv.Trigger)
	if cmdBase == invTriggerBase && cmdBase != "" {
		triggerScore = 1.0
	} else if cmdBase != "" && invTriggerBase != "" {
		// Partial match on command name
		if strings.HasPrefix(cmdBase, invTriggerBase) || strings.HasPrefix(invTriggerBase, cmdBase) {
			triggerScore = 0.5
		}
	}

	// Keyword overlap: Jaccard similarity
	keywordScore := 0.0
	if len(keywords) > 0 && len(inv.Keywords) > 0 {
		intersection := 0
		for _, k := range keywords {
			if containsString(inv.Keywords, k) {
				intersection++
			}
		}
		union := len(keywords) + len(inv.Keywords) - intersection
		if union > 0 {
			keywordScore = float64(intersection) / float64(union)
		}
	}

	return 0.40*hostScore + 0.35*triggerScore + 0.25*keywordScore
}

// flushInvestigation saves a session as a completed investigation.
func (t *Tracker) flushInvestigation(session *Session) {
	if len(session.Commands) < 2 {
		// Not enough commands to be a meaningful investigation
		return
	}

	inv := &Investigation{
		Hosts:          session.Hosts,
		Trigger:        session.Trigger,
		TriggerSignals: session.TriggerSignals,
		Commands:       session.Commands,
		Keywords:       session.Keywords,
		RootCauseHint:  extractRootCause(session),
		StartTime:      session.StartTime,
		EndTime:        session.LastCommand,
	}

	// Save to disk
	t.saveInvestigation(inv)

	// Prune old investigations
	t.pruneInvestigations()
}

// extractRootCause attempts to identify the root cause from the last
// "interesting" command in a session. Uses keyword signals rather than AI.
func extractRootCause(session *Session) string {
	// Walk backwards through commands to find the last one with error keywords
	for i := len(session.Commands) - 1; i >= 0; i-- {
		cmd := session.Commands[i]
		if len(cmd.Keywords) > 0 {
			// Join the most relevant keywords as a hint
			hint := strings.Join(cmd.Keywords[:min(len(cmd.Keywords), 5)], ", ")
			return hint
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Session lock file operations

func (t *Tracker) sessionLockPath() string {
	return filepath.Join(t.dir, ".session.lock")
}

func (t *Tracker) loadSession(path string) *Session {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var session Session
	if err := yaml.Unmarshal(data, &session); err != nil {
		return nil
	}
	return &session
}

func (t *Tracker) saveSession(path string, session *Session) {
	// Ensure directory exists
	os.MkdirAll(t.dir, 0700)

	data, err := yaml.Marshal(session)
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0600)
}

// Investigation persistence

func (t *Tracker) investigationsDir() string {
	return filepath.Join(t.dir, "investigations")
}

func (t *Tracker) saveInvestigation(inv *Investigation) {
	dir := t.investigationsDir()
	os.MkdirAll(dir, 0700)

	// Generate filename from timestamp and hosts
	ts := inv.StartTime.Format("2006-01-02-150405")
	hostPart := strings.Join(inv.Hosts, "-")
	filename := fmt.Sprintf("inv-%s-%s.yaml", ts, sanitizeFilename(hostPart))
	path := filepath.Join(dir, filename)

	data, err := yaml.Marshal(inv)
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0600)
}

func (t *Tracker) loadInvestigations() []*Investigation {
	dir := t.investigationsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var investigations []*Investigation
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var inv Investigation
		if err := yaml.Unmarshal(data, &inv); err != nil {
			continue
		}
		investigations = append(investigations, &inv)
	}
	return investigations
}

func (t *Tracker) pruneInvestigations() {
	maxInv := t.config.MaxInvestigationsPerHost
	if maxInv <= 0 {
		maxInv = DefaultMaxInvestigations
	}

	investigations := t.loadInvestigations()
	if len(investigations) <= maxInv {
		return
	}

	// Sort by start time (newest first)
	sorted := make([]*Investigation, len(investigations))
	copy(sorted, investigations)
	sortByTime(sorted)

	// Keep only the newest maxInv
	dir := t.investigationsDir()
	for i := maxInv; i < len(sorted); i++ {
		// Find and remove the file for this investigation
		pattern := fmt.Sprintf("inv-%s-*.yaml", sorted[i].StartTime.Format("2006-01-02-150405"))
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, match := range matches {
			os.Remove(match)
		}
	}
}

// sortByTime sorts investigations by start time, newest first.
func sortByTime(invs []*Investigation) {
	for i := 0; i < len(invs); i++ {
		for j := i + 1; j < len(invs); j++ {
			if invs[j].StartTime.After(invs[i].StartTime) {
				invs[i], invs[j] = invs[j], invs[i]
			}
		}
	}
}

// Keyword extraction

var (
	// Error-related patterns to extract from command output
	errorPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(failed|failure|error|fatal|critical|panic|crashed|killed|timeout|timed?\s*out|refused|unreachable|denied|unauthorized|forbidden)\b`),
		regexp.MustCompile(`\b([45]\d{2})\b`), // HTTP 4xx/5xx status codes
		regexp.MustCompile(`(?i)\b(inactive|stopped|dead|masked|not found|no such|does not exist|cannot|could not|unable to)\b`),
		regexp.MustCompile(`(?i)\b(OOM|out of memory|segfault|core dump|signal\s*\d+)\b`),
		regexp.MustCompile(`(?i)\b(exhausted|depleted|overflow|full|disk\s+full|no\s+space)\b`),
		regexp.MustCompile(`(?i)\b(disconnected|connection\s+(reset|dropped|lost|refused|timed?\s*out))\b`),
	}

	// Service/unit name patterns
	servicePattern = regexp.MustCompile(`(?i)\b([a-zA-Z0-9_-]+)\.(service|socket|timer|target|mount)\b`)
)

// ExtractKeywords pulls meaningful diagnostic keywords from command output.
// Returns deduplicated, lowercased keywords.
func ExtractKeywords(output string) []string {
	if output == "" {
		return nil
	}

	// Limit output size for keyword extraction
	if len(output) > 10000 {
		output = output[:10000]
	}

	var keywords []string
	seen := make(map[string]bool)

	// Extract error-related keywords
	for _, pattern := range errorPatterns {
		matches := pattern.FindAllString(output, -1)
		for _, m := range matches {
			k := strings.ToLower(strings.TrimSpace(m))
			if k != "" && !seen[k] {
				seen[k] = true
				keywords = append(keywords, k)
			}
		}
	}

	// Extract service/unit names
	serviceMatches := servicePattern.FindAllString(output, -1)
	for _, m := range serviceMatches {
		k := strings.ToLower(strings.TrimSpace(m))
		if k != "" && !seen[k] {
			seen[k] = true
			keywords = append(keywords, k)
		}
	}

	// Cap at 20 keywords
	if len(keywords) > 20 {
		keywords = keywords[:20]
	}

	return keywords
}

// extractCommandBase returns the base command name (first word, no path).
func extractCommandBase(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	// Split on space to get the command
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return ""
	}
	base := parts[0]
	// Strip path prefix
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	return base
}

// mergeKeywords merges two keyword lists, deduplicating.
func mergeKeywords(existing, new []string) []string {
	seen := make(map[string]bool)
	for _, k := range existing {
		seen[k] = true
	}
	result := make([]string, len(existing))
	copy(result, existing)
	for _, k := range new {
		if !seen[k] {
			seen[k] = true
			result = append(result, k)
		}
	}
	return result
}

// sanitizeFilename makes a string safe for use as a filename component.
func sanitizeFilename(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, s)
	// Collapse multiple dashes
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	// Trim dashes from ends
	s = strings.Trim(s, "-")
	// Cap length
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// ListInvestigations returns all stored investigations, newest first.
func (t *Tracker) ListInvestigations() []*Investigation {
	invs := t.loadInvestigations()
	sortByTime(invs)
	return invs
}

// ClearAll removes all stored investigations and session data.
func (t *Tracker) ClearAll() error {
	// Remove session lock
	os.Remove(t.sessionLockPath())

	// Remove all investigation files
	dir := t.investigationsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		os.Remove(filepath.Join(dir, entry.Name()))
	}
	return nil
}
