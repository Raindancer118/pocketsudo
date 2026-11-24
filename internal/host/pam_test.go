package host

import (
	"strings"
	"testing"
)

const cachySudo = `#%PAM-1.0
auth sufficient pam_fprintd.so # chwd-fprintd
auth		include		system-auth
account		include		system-auth
session		include		system-auth
session		optional	pam_systemd.so class=none
`

func TestInstallPAM(t *testing.T) {
	got, err := InstallPAM(cachySudo, "/usr/local/bin/pocketsudo", true)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	if lines[0] != "#%PAM-1.0" || !strings.Contains(lines[1], "pam_exec.so") || !strings.Contains(lines[1], "seteuid") {
		t.Fatalf("helper not first auth line:\n%s", got)
	}
	if !strings.HasPrefix(lines[2], disabledTag+"auth sufficient pam_fprintd.so") {
		t.Fatalf("fprintd not disabled:\n%s", got)
	}
	if !strings.Contains(got, "auth\t\tinclude\t\tsystem-auth") || !PAMInstalled(got) {
		t.Fatalf("password fallback lost:\n%s", got)
	}
	again, _ := InstallPAM(got, "/usr/local/bin/pocketsudo", true)
	if again != got {
		t.Fatalf("install not idempotent:\n%s", again)
	}
	back, changed := UninstallPAM(got)
	if !changed || back != cachySudo {
		t.Fatalf("uninstall did not restore original:\n%q", back)
	}
}

func TestInstallPAMKeepsFprintdWhenAsked(t *testing.T) {
	got, _ := InstallPAM(cachySudo, "/x", false)
	if strings.Contains(got, disabledTag) {
		t.Fatal("fprintd disabled although local_fingerprint is off")
	}
}

func TestInstallPAMRefusesOddFiles(t *testing.T) {
	if _, err := InstallPAM("#%PAM-1.0\n", "/x", true); err == nil {
		t.Fatal("installed into a file without auth lines")
	}
}

const debianSudo = `#%PAM-1.0

# Set up user limits from /etc/security/limits.conf.
session    required   pam_limits.so

@include common-auth
@include common-account
@include common-session-noninteractive
`

func TestInstallPAMDebianInclude(t *testing.T) {
	got, err := InstallPAM(debianSudo, "/usr/local/bin/pocketsudo", true)
	if err != nil {
		t.Fatal(err)
	}
	i, j := strings.Index(got, "pam_exec.so"), strings.Index(got, "@include common-auth")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("helper must come right before common-auth:\n%s", got)
	}
	if back, _ := UninstallPAM(got); back != debianSudo {
		t.Fatalf("uninstall did not restore:\n%q", back)
	}
}
