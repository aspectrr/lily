package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{
			name:   "empty output",
			output: "",
			want:   nil,
		},
		{
			name:   "nginx failure",
			output: "● nginx.service - A high performance web server\n   Active: failed (Result: exit-code)",
			want:   []string{"failed", "nginx.service"},
		},
		{
			name:   "HTTP 502 error",
			output: "HTTP/1.1 502 Bad Gateway\nupstream timed out (110: Connection timed out)",
			want:   []string{"timed out", "502", "connection timed out"},
		},
		{
			name:   "OOM killer",
			output: "Out of memory: Killed process 1234 (nginx)",
			want:   []string{"out of memory", "killed"},
		},
		{
			name:   "connection refused",
			output: "Connection refused\nError: could not connect to database",
			want:   []string{"refused", "error", "could not", "connection refused"},
		},
		{
			name:   "no errors",
			output: "Active: active (running)\nMain PID: 1234",
			want:   nil,
		},
		{
			name:   "disk full",
			output: "No space left on device (28)\n disk full on /var",
			want:   []string{"no space", "disk full"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractKeywords(tt.output)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			// Just check that expected keywords are present
			for _, w := range tt.want {
				found := false
				for _, g := range got {
					if g == w {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("ExtractKeywords(%q) missing keyword %q, got %v", tt.output, w, got)
				}
			}
		})
	}
}

func TestExtractCommandBase(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"systemctl status nginx", "systemctl"},
		{"ps aux", "ps"},
		{"/usr/bin/ls -la", "ls"},
		{"", ""},
		{"  echo ok  ", "echo"},
	}

	for _, tt := range tests {
		got := extractCommandBase(tt.command)
		if got != tt.want {
			t.Errorf("extractCommandBase(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

func TestSessionTracking(t *testing.T) {
	// Use a temp directory for isolation
	tmpDir := t.TempDir()
	tracker := &Tracker{
		config: &Config{
			Enabled:                  true,
			SessionTimeout:           "10m",
			MaxInvestigationsPerHost: 50,
		},
		dir: tmpDir,
	}

	// Record first command — starts a new session
	hint := tracker.RecordCommand("web1", "systemctl status nginx",
		"● nginx.service - A high performance web server\n   Active: failed (Result: exit-code)")

	// First command in a session — no past investigations to hint
	if hint != nil {
		t.Error("expected no hint on first command in fresh session")
	}

	// Verify session file exists
	sessionPath := filepath.Join(tmpDir, ".session.lock")
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		t.Error("session lock file should exist after recording a command")
	}

	// Record second command
	hint = tracker.RecordCommand("web1", "journalctl -u nginx --no-pager -n 20",
		"-- Logs begin at ...\nupstream timed out")

	if hint != nil {
		t.Error("expected no hint — no past investigations exist yet")
	}
}

func TestSessionTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := &Tracker{
		config: &Config{
			Enabled:                  true,
			SessionTimeout:           "1ms", // Very short timeout for testing
			MaxInvestigationsPerHost: 50,
		},
		dir: tmpDir,
	}

	// Record two commands (need >= 2 for a valid investigation)
	tracker.RecordCommand("web1", "systemctl status nginx",
		"Active: failed")
	tracker.RecordCommand("web1", "journalctl -u nginx",
		"error: upstream timed out")

	// Wait for timeout
	time.Sleep(10 * time.Millisecond)

	// Record another command — should flush old session, start new one
	tracker.RecordCommand("web1", "systemctl status nginx",
		"Active: failed")

	// Check that an investigation was saved
	investigations := tracker.loadInvestigations()
	if len(investigations) == 0 {
		t.Error("expected at least one investigation to be saved after timeout")
	}

	if len(investigations) > 0 {
		inv := investigations[0]
		if len(inv.Hosts) != 1 || inv.Hosts[0] != "web1" {
			t.Errorf("investigation hosts = %v, want [web1]", inv.Hosts)
		}
		if inv.Trigger != "systemctl status nginx" {
			t.Errorf("investigation trigger = %q, want %q", inv.Trigger, "systemctl status nginx")
		}
	}
}

func TestMultiHostInvestigation(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := &Tracker{
		config: &Config{
			Enabled:                  true,
			SessionTimeout:           "1ms",
			MaxInvestigationsPerHost: 50,
		},
		dir: tmpDir,
	}

	// Record commands on multiple hosts
	tracker.RecordCommand("web1", "systemctl status nginx",
		"Active: failed")
	tracker.RecordCommand("db1", "systemctl status mysql",
		"Active: failed")

	// Flush by waiting and triggering a new command
	time.Sleep(10 * time.Millisecond)
	tracker.RecordCommand("web1", "echo ok", "ok")

	// Check investigation was saved with both hosts
	investigations := tracker.loadInvestigations()
	if len(investigations) == 0 {
		t.Fatal("expected at least one investigation")
	}

	inv := investigations[0]
	if len(inv.Hosts) != 2 {
		t.Errorf("expected 2 hosts, got %d: %v", len(inv.Hosts), inv.Hosts)
	}
	if !containsString(inv.Hosts, "web1") || !containsString(inv.Hosts, "db1") {
		t.Errorf("expected hosts [web1 db1], got %v", inv.Hosts)
	}
}

func TestSimilarityMatching(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := &Tracker{
		config: &Config{
			Enabled:                  true,
			SessionTimeout:           "1ms",
			MaxInvestigationsPerHost: 50,
		},
		dir: tmpDir,
	}

	// Simulate a past investigation
	pastInv := &Investigation{
		Hosts:   []string{"web1"},
		Trigger: "systemctl status nginx",
		Commands: []CommandEntry{
			{Command: "systemctl status nginx", Host: "web1"},
			{Command: "journalctl -u nginx", Host: "web1"},
			{Command: "systemctl status php-fpm", Host: "web1", Keywords: []string{"pool exhausted"}},
		},
		Keywords:      []string{"failed", "nginx.service", "502", "pool exhausted"},
		RootCauseHint: "php-fpm pool exhaustion causing nginx 502",
		StartTime:     time.Now().Add(-24 * time.Hour),
		EndTime:       time.Now().Add(-24 * time.Hour).Add(5 * time.Minute),
	}
	tracker.saveInvestigation(pastInv)

	// Now start a new session with similar symptoms
	hint := tracker.RecordCommand("web1", "systemctl status nginx",
		"● nginx.service\n   Active: failed")

	if hint == nil {
		t.Fatal("expected a hint from similar past investigation")
	}
	if hint.Similarity < 0.3 {
		t.Errorf("similarity = %.2f, expected >= 0.3", hint.Similarity)
	}
	if hint.Investigation.RootCauseHint != "php-fpm pool exhaustion causing nginx 502" {
		t.Errorf("unexpected root cause hint: %q", hint.Investigation.RootCauseHint)
	}

	// Format the hint
	formatted := hint.FormatHint()
	if formatted == "" {
		t.Error("FormatHint() returned empty string")
	}
	if !contains(formatted, "php-fpm") {
		t.Errorf("FormatHint() missing root cause: %q", formatted)
	}
}

func TestNoHintOnDifferentHost(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := &Tracker{
		config: &Config{
			Enabled:                  true,
			SessionTimeout:           "1ms",
			MaxInvestigationsPerHost: 50,
		},
		dir: tmpDir,
	}

	pastInv := &Investigation{
		Hosts:     []string{"web1"},
		Trigger:   "systemctl status nginx",
		Keywords:  []string{"failed", "nginx.service"},
		StartTime: time.Now().Add(-24 * time.Hour),
		EndTime:   time.Now().Add(-24 * time.Hour),
	}
	tracker.saveInvestigation(pastInv)

	// Query with completely different host and different command
	hint := tracker.RecordCommand("db999", "systemctl status mysql",
		"Active: failed")

	// Similarity should be low (no host match, no trigger match, only keyword overlap)
	if hint != nil && hint.Similarity >= 0.5 {
		t.Errorf("similarity = %.2f for completely different context, expected low", hint.Similarity)
	}
}

func TestHintFormatting(t *testing.T) {
	hint := &Hint{
		Similarity: 0.87,
		Investigation: &Investigation{
			Hosts:         []string{"web1", "db1"},
			RootCauseHint: "mysql slow queries causing nginx 502 upstream timeouts",
			Commands: []CommandEntry{
				{Command: "systemctl status nginx", Host: "web1"},
				{Command: "systemctl status mysql", Host: "db1"},
			},
			StartTime: time.Date(2025, 5, 10, 14, 23, 0, 0, time.UTC),
		},
	}

	formatted := hint.FormatHint()
	if !contains(formatted, "87%") {
		t.Errorf("hint missing similarity percentage: %q", formatted)
	}
	if !contains(formatted, "web1") || !contains(formatted, "db1") {
		t.Errorf("hint missing hosts: %q", formatted)
	}
	if !contains(formatted, "mysql slow queries") {
		t.Errorf("hint missing root cause: %q", formatted)
	}

	// Test low similarity produces empty hint
	lowHint := &Hint{Similarity: 0.1, Investigation: &Investigation{}}
	if lowHint.FormatHint() != "" {
		t.Error("expected empty hint for low similarity")
	}
}

func TestDisabledMemory(t *testing.T) {
	tracker := NewTracker(&Config{Enabled: false})
	hint := tracker.RecordCommand("web1", "systemctl status nginx", "failed")
	if hint != nil {
		t.Error("expected nil hint when memory is disabled")
	}
}

func TestConfigParsing(t *testing.T) {
	cfg := &Config{
		Enabled:        true,
		SessionTimeout: "5m",
	}
	if cfg.ParseSessionTimeout() != 5*time.Minute {
		t.Errorf("ParseSessionTimeout() = %v, want 5m", cfg.ParseSessionTimeout())
	}

	cfg = &Config{Enabled: true}
	if cfg.ParseSessionTimeout() != DefaultSessionTimeout {
		t.Errorf("default ParseSessionTimeout() = %v, want %v", cfg.ParseSessionTimeout(), DefaultSessionTimeout)
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"web1", "web1"},
		{"web1.example.com", "web1-example-com"},
		{"web 1", "web-1"},
		{"WEB1", "web1"},
		{"", ""},
	}

	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMergeKeywords(t *testing.T) {
	result := mergeKeywords([]string{"a", "b"}, []string{"b", "c"})
	if len(result) != 3 {
		t.Errorf("mergeKeywords() = %v, want 3 elements", result)
	}
}

func TestFlush(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := &Tracker{
		config: &Config{
			Enabled:                  true,
			SessionTimeout:           "10m",
			MaxInvestigationsPerHost: 50,
		},
		dir: tmpDir,
	}

	tracker.RecordCommand("web1", "systemctl status nginx", "failed")
	tracker.RecordCommand("web1", "journalctl -u nginx", "error")

	tracker.Flush()

	investigations := tracker.loadInvestigations()
	if len(investigations) == 0 {
		t.Error("expected investigation to be saved after Flush()")
	}

	// Session lock should be cleaned up
	sessionPath := filepath.Join(tmpDir, ".session.lock")
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Error("session lock file should be removed after Flush()")
	}
}

func TestClearAll(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := &Tracker{
		config: &Config{
			Enabled:                  true,
			SessionTimeout:           "1ms",
			MaxInvestigationsPerHost: 50,
		},
		dir: tmpDir,
	}

	tracker.RecordCommand("web1", "systemctl status nginx", "failed")
	time.Sleep(10 * time.Millisecond)
	tracker.RecordCommand("web1", "echo ok", "ok")

	if err := tracker.ClearAll(); err != nil {
		t.Fatalf("ClearAll() error: %v", err)
	}

	investigations := tracker.loadInvestigations()
	if len(investigations) != 0 {
		t.Errorf("expected no investigations after ClearAll(), got %d", len(investigations))
	}
}

func TestListInvestigations(t *testing.T) {
	tmpDir := t.TempDir()
	tracker := &Tracker{
		config: &Config{Enabled: true, SessionTimeout: "10m", MaxInvestigationsPerHost: 50},
		dir:    tmpDir,
	}

	// Save multiple investigations
	tracker.saveInvestigation(&Investigation{
		Hosts:     []string{"web1"},
		Trigger:   "cmd1",
		StartTime: time.Now().Add(-2 * time.Hour),
	})
	tracker.saveInvestigation(&Investigation{
		Hosts:     []string{"db1"},
		Trigger:   "cmd2",
		StartTime: time.Now().Add(-1 * time.Hour),
	})

	invs := tracker.ListInvestigations()
	if len(invs) != 2 {
		t.Fatalf("expected 2 investigations, got %d", len(invs))
	}
	// Should be newest first
	if invs[0].Trigger != "cmd2" {
		t.Errorf("expected newest first, got %q", invs[0].Trigger)
	}
}

func TestKeywordExtractionOutputTruncation(t *testing.T) {
	// Very long output should be truncated for keyword extraction
	// Keywords in the first 10KB should still be found
	longOutput := make([]byte, 10000)
	for i := range longOutput {
		longOutput[i] = 'a'
	}
	// Insert error keywords within the first 10KB
	copy(longOutput[5000:], []byte(" FAILED error "))

	keywords := ExtractKeywords(string(longOutput))
	found := false
	for _, k := range keywords {
		if k == "failed" || k == "error" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find error keywords in first 10KB of output")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
