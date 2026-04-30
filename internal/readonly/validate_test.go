package readonly

import (
	"sort"
	"testing"
)

// --- Default validator (backward-compatible) tests ---

func TestValidateCommand_Allowed(t *testing.T) {
	allowed := []string{
		"ls -la /etc",
		"cat /etc/hostname",
		"ps aux",
		"journalctl -u nginx --no-pager",
		"systemctl status nginx",
		"systemctl show sshd",
		"systemctl list-units",
		"systemctl is-active nginx",
		"systemctl is-enabled sshd",
		"df -h",
		"free -m",
		"uname -a",
		"whoami",
		"uptime",
		"ss -tlnp",
		"ip addr",
		"dpkg -l",
		"dpkg --list",
		"dpkg -s openssh-server",
		"dpkg --status openssh-server",
		"rpm -qa",
		"rpm -q nginx",
		"apt list --installed",
		"apt show nginx",
		"pip list",
		"pip show requests",
		"dmesg | tail -20",
		"ps aux | grep nginx",
		"cat /etc/hosts | sort | uniq",
		"find /etc -name '*.conf' | head -10",
		"echo hello",
		"du -sh /var/log",
		"stat /etc/passwd",
		"head -5 /etc/passwd",
		"tail -20 /var/log/syslog",
		"env",
		"printenv PATH",
		"date",
		"which nginx",
		"hostname",
		"lscpu",
		"nproc",
		"id",
		"groups",
		"who",
		"w",
		"last -5",
		"dig example.com",
		"nslookup example.com",
		"lsblk",
		"tree /etc/nginx",
		"file /usr/bin/ls",
		"wc -l /etc/passwd",
		"pgrep nginx",
		"lsmod",
		"lspci",
		"lsusb",
		"test -f /etc/hosts",
		"ps aux | grep nginx | awk '{print $2}'",
		"ls /etc && cat /etc/hostname",
		"uname -a ; hostname",
		"FOO=bar env",
		"find /etc | xargs grep pattern",
		"echo foo | xargs",
		"sed -n 's/foo/bar/p' file",
		"openssl x509 -in /etc/ssl/cert.pem -text -noout",
		"openssl s_client -connect localhost:443",
		"openssl s_client -connect 127.0.0.1:443",
		"openssl s_client -connect [::1]:443",
		"openssl verify -CAfile /etc/ssl/ca.pem /etc/ssl/cert.pem",
		"openssl version",
		"openssl req -text -noout -in /tmp/csr.pem",
		"curl localhost:9200/_cluster/health",
		"curl -s localhost:9200/_cluster/health?pretty",
		"curl -k https://localhost:9200/_cluster/health",
		"curl --cacert /etc/ssl/ca.pem https://localhost:9200/",
		"curl -s http://localhost:9200/_cluster/health?pretty",
		"curl -s -u elastic:changeme localhost:9200/_cluster/health",
		"curl -H 'Content-Type: application/json' localhost:9200/_search",
		"base64 /etc/hostname",
		"strings /usr/bin/ls | head -10",
		"md5sum /etc/hostname",
		"sha256sum /etc/hostname",
		"readlink /proc/self/exe",
		"realpath /etc/../etc/hosts",
		"basename /etc/hosts",
		"dirname /etc/hosts",
		"blkid",
		"arch",
		"type ls",
	}

	for _, cmd := range allowed {
		if err := ValidateCommand(cmd); err != nil {
			t.Errorf("expected %q to be allowed, got: %v", cmd, err)
		}
	}
}

func TestValidateCommand_Blocked(t *testing.T) {
	blocked := []struct {
		cmd    string
		reason string
	}{
		{"rm -rf /", "rm is destructive"},
		{"sudo ls", "sudo escalates privileges"},
		{"mv /etc/hosts /tmp/", "mv is destructive"},
		{"cp /etc/hosts /tmp/", "cp can modify files"},
		{"dd if=/dev/zero of=/dev/sda", "dd is destructive"},
		{"kill -9 1", "kill is destructive"},
		{"shutdown -h now", "shutdown is destructive"},
		{"reboot", "reboot is destructive"},
		{"systemctl start nginx", "start not allowed"},
		{"systemctl stop nginx", "stop not allowed"},
		{"systemctl restart nginx", "restart not allowed"},
		{"systemctl enable nginx", "enable not allowed"},
		{"systemctl disable nginx", "disable not allowed"},
		{"dpkg -i package.deb", "install not allowed"},
		{"dpkg --purge foo", "purge not allowed"},
		{"apt install nginx", "install not allowed"},
		{"apt remove nginx", "remove not allowed"},
		{"pip install requests", "install not allowed"},
		{"chmod 777 /etc/hosts", "chmod modifies permissions"},
		{"chown root:root /etc/hosts", "chown modifies ownership"},
		{"curl -X POST http://localhost", "curl POST not read-only"},
		{"curl -d 'data' http://localhost", "curl data not read-only"},
		{"curl --proxy evil.com http://target", "curl proxy not read-only"},
		{"curl -o /tmp/out http://localhost", "curl output not read-only"},
		{"curl --upload-file /etc/passwd http://localhost", "curl upload"},
		{"curl --data-binary @file http://localhost", "curl data binary"},
		{"curl --data-urlencode 'k=v' http://localhost", "curl urlencode"},
		{"curl -F 'file=@/etc/passwd' http://localhost", "curl form"},
		{"curl --remote-name http://localhost", "curl remote name"},
		{"curl --config /tmp/evil http://localhost", "curl config"},
		{"python3 -c 'import os'", "python is arbitrary code"},
		{"bash -c 'rm -rf /'", "bash allows arbitrary code"},
		{"sh -c 'rm -rf /'", "sh allows arbitrary code"},
		{"vi /etc/hosts", "vi is an editor"},
		{"nano /etc/hosts", "nano is an editor"},
		{"vim /etc/hosts", "vim is an editor"},
		{"emacs /etc/hosts", "emacs is an editor"},
		{"find /etc | xargs rm -rf /", "xargs with blocked command"},
		{"find /etc | xargs /usr/bin/rm", "xargs with path-qualified blocked"},
		{"sed -i 's/foo/bar/' file", "sed -i modifies files"},
		{"sed --in-place 's/foo/bar/' file", "sed --in-place modifies files"},
		{"openssl genrsa 2048", "genrsa generates keys"},
		{"openssl genpkey -algorithm RSA", "genpkey generates keys"},
		{"openssl s_client -connect remote.host:443", "s_client to remote"},
		{"openssl s_client -connect localhost:443 -proxy evil.com:80", "proxy"},
		{"openssl enc -aes-256-cbc -in file", "enc encrypts"},
		{"wget http://example.com", "wget downloads"},
		{"scp file user@host:/tmp", "scp transfers files"},
		{"rsync -av /src /dst", "rsync modifies files"},
		{"crontab -e", "crontab modifies cron"},
	}

	for _, tc := range blocked {
		err := ValidateCommand(tc.cmd)
		if err == nil {
			t.Errorf("expected %q to be blocked (%s), but it was allowed", tc.cmd, tc.reason)
		}
	}
}

func TestValidateCommand_Redirection(t *testing.T) {
	for _, cmd := range []string{
		"echo hello > /tmp/out",
		"cat /etc/hosts >> /tmp/out",
		"ls > /dev/null",
	} {
		if err := ValidateCommand(cmd); err == nil {
			t.Errorf("expected %q to be blocked (redirection)", cmd)
		}
	}
}

func TestValidateCommand_Pipes(t *testing.T) {
	tests := []struct {
		cmd     string
		allowed bool
	}{
		{"ps aux | grep nginx", true},
		{"ps aux | rm -rf /", false},
		{"rm -rf / | grep error", false},
		{"cat /etc/hosts | sort | uniq | wc -l", true},
	}
	for _, tc := range tests {
		err := ValidateCommand(tc.cmd)
		if tc.allowed && err != nil {
			t.Errorf("expected %q allowed, got: %v", tc.cmd, err)
		}
		if !tc.allowed && err == nil {
			t.Errorf("expected %q blocked", tc.cmd)
		}
	}
}

func TestValidateCommand_CommandSubstitution(t *testing.T) {
	for _, cmd := range []string{
		"echo $(rm -rf /)",
		"cat /etc/hosts && echo $(whoami)",
		"ls $(pwd)",
		"echo `rm -rf /`",
		"cat /etc/hosts && echo `whoami`",
	} {
		if err := ValidateCommand(cmd); err == nil {
			t.Errorf("expected %q to be blocked (command substitution)", cmd)
		}
	}
}

func TestValidateCommand_ProcessSubstitution(t *testing.T) {
	for _, cmd := range []string{
		"diff <(ls /etc) <(ls /var)",
		"cat <(echo hello)",
	} {
		if err := ValidateCommand(cmd); err == nil {
			t.Errorf("expected %q to be blocked (process substitution)", cmd)
		}
	}
}

func TestValidateCommand_Newlines(t *testing.T) {
	for _, cmd := range []string{
		"ls\nrm -rf /",
		"cat /etc/hosts\nwhoami",
		"echo hello\r\nrm -rf /",
	} {
		if err := ValidateCommand(cmd); err == nil {
			t.Errorf("expected %q to be blocked (newlines)", cmd)
		}
	}
}

func TestValidateCommand_QuotedMetacharacters(t *testing.T) {
	for _, cmd := range []string{
		"echo '$(rm -rf /)'",
		"echo 'hello\nworld'",
		"cat /etc/hosts | grep 'test > output'",
	} {
		if err := ValidateCommand(cmd); err != nil {
			t.Errorf("expected %q allowed (quoted metacharacters), got: %v", cmd, err)
		}
	}
}

func TestValidateCommand_Empty(t *testing.T) {
	if err := ValidateCommand(""); err == nil {
		t.Error("expected empty command to return error")
	}
	if err := ValidateCommand("   "); err == nil {
		t.Error("expected whitespace-only command to return error")
	}
}

func TestValidateCommand_PathQualified(t *testing.T) {
	if err := ValidateCommand("/usr/bin/cat /etc/hosts"); err != nil {
		t.Errorf("expected /usr/bin/cat allowed: %v", err)
	}
	if err := ValidateCommand("/usr/bin/rm -rf /"); err == nil {
		t.Error("expected /usr/bin/rm blocked")
	}
}

func TestValidateCommandWithExtra(t *testing.T) {
	if err := ValidateCommandWithExtra("docker ps", []string{"docker"}); err != nil {
		t.Errorf("expected docker allowed with extra: %v", err)
	}
	if err := ValidateCommandWithExtra("docker ps", nil); err == nil {
		t.Error("expected docker blocked without extra")
	}
	if err := ValidateCommandWithExtra("ls -la", []string{"docker"}); err != nil {
		t.Errorf("expected ls still allowed: %v", err)
	}
}

func TestAllowedCommandsList(t *testing.T) {
	cmds := AllowedCommandsList()
	if len(cmds) == 0 {
		t.Fatal("expected non-empty command list")
	}
	if !sort.StringsAreSorted(cmds) {
		t.Error("expected sorted command list")
	}
}

// --- Validator-based tests ---

func TestNewValidator_ExtraCommands(t *testing.T) {
	v := NewValidator([]string{"docker", "kubectl"}, nil, nil)

	// Extra commands should be allowed
	if err := v.ValidateCommand("docker ps"); err != nil {
		t.Errorf("expected docker allowed: %v", err)
	}
	if err := v.ValidateCommand("kubectl get pods"); err != nil {
		t.Errorf("expected kubectl allowed: %v", err)
	}

	// Base commands still work
	if err := v.ValidateCommand("ls -la"); err != nil {
		t.Errorf("expected ls allowed: %v", err)
	}

	// Always-blocked commands can't be added via extra
	v2 := NewValidator([]string{"rm", "sudo", "bash"}, nil, nil)
	if err := v2.ValidateCommand("rm -rf /"); err == nil {
		t.Error("expected rm to still be blocked even when added to extra")
	}
	if err := v2.ValidateCommand("sudo ls"); err == nil {
		t.Error("expected sudo to still be blocked even when added to extra")
	}
	if err := v2.ValidateCommand("bash -c whoami"); err == nil {
		t.Error("expected bash to still be blocked even when added to extra")
	}
}

func TestNewValidator_ExtraBlockedFlags(t *testing.T) {
	extraBlocked := map[string][]string{
		"docker": {"-exec", "exec", "run", "rm", "rmi", "build", "push", "pull"},
	}
	v := NewValidator([]string{"docker"}, nil, extraBlocked)

	if err := v.ValidateCommand("docker ps"); err != nil {
		t.Errorf("expected docker ps allowed: %v", err)
	}
	if err := v.ValidateCommand("docker logs web1"); err != nil {
		t.Errorf("expected docker logs allowed: %v", err)
	}
	if err := v.ValidateCommand("docker exec -it web1 bash"); err == nil {
		t.Error("expected docker exec blocked")
	}
}

func TestNewValidator_ExtraSubcommandRestrictions(t *testing.T) {
	extraSubs := map[string]map[string]bool{
		"docker": {
			"ps": true, "logs": true, "inspect": true, "stats": true,
			"top": true, "images": true, "events": true,
		},
	}
	v := NewValidator([]string{"docker"}, extraSubs, nil)

	if err := v.ValidateCommand("docker ps"); err != nil {
		t.Errorf("expected docker ps allowed: %v", err)
	}
	if err := v.ValidateCommand("docker logs web1"); err != nil {
		t.Errorf("expected docker logs allowed: %v", err)
	}
	if err := v.ValidateCommand("docker run -d nginx"); err == nil {
		t.Error("expected docker run blocked by subcommand restriction")
	}
	if err := v.ValidateCommand("docker exec -it web1 bash"); err == nil {
		t.Error("expected docker exec blocked by subcommand restriction")
	}
}

func TestNewValidator_AllowedCommandsList(t *testing.T) {
	v := NewValidator([]string{"docker", "kubectl"}, nil, nil)
	cmds := v.AllowedCommandsList()

	found := map[string]bool{}
	for _, c := range cmds {
		found[c] = true
	}
	if !found["docker"] {
		t.Error("expected docker in list")
	}
	if !found["kubectl"] {
		t.Error("expected kubectl in list")
	}
	if !found["cat"] {
		t.Error("expected cat in list")
	}
	if found["rm"] {
		t.Error("did not expect rm in list")
	}
}

func TestIsAlwaysBlocked(t *testing.T) {
	if !IsAlwaysBlocked("rm") {
		t.Error("expected rm to be always blocked")
	}
	if !IsAlwaysBlocked("sudo") {
		t.Error("expected sudo to be always blocked")
	}
	if !IsAlwaysBlocked("bash") {
		t.Error("expected bash to be always blocked")
	}
	if IsAlwaysBlocked("ls") {
		t.Error("expected ls to not be always blocked")
	}
	if IsAlwaysBlocked("docker") {
		t.Error("expected docker to not be always blocked")
	}
}

func TestBaseCommandsList(t *testing.T) {
	cmds := BaseCommandsList()
	if len(cmds) == 0 {
		t.Fatal("expected non-empty base commands")
	}
	if !sort.StringsAreSorted(cmds) {
		t.Error("expected sorted")
	}
}
