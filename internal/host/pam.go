package host

import (
	"errors"
	"os"
	"strings"
)

const (
	pamMarker   = "# pocketsudo"
	disabledTag = "#pocketsudo-disabled# "
)

func PAMLine(bin string) string {
	// seteuid: without it pam_exec runs the helper with sudo's real uid (the
	// user), which cannot read the root-only config.
	return "auth sufficient pam_exec.so quiet quiet_log seteuid stdout " + bin + " auth " + pamMarker
}

// InstallPAM inserts the helper as the first auth line. pam_fprintd lines are
// disabled because the helper already runs the reader in parallel with the
// phone; otherwise the user would be asked for the finger twice.
func InstallPAM(content, bin string, disableFprintd bool) (string, error) {
	content, _ = UninstallPAM(content)
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	var out []string
	inserted := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		// Debian/Ubuntu pull the auth stack in via "@include common-auth"
		isAuth := strings.HasPrefix(trimmed, "auth") || strings.HasPrefix(trimmed, "-auth") ||
			trimmed == "@include common-auth"
		if !inserted && isAuth {
			out = append(out, PAMLine(bin))
			inserted = true
		}
		if disableFprintd && isAuth && strings.Contains(trimmed, "pam_fprintd.so") {
			l = disabledTag + l
		}
		out = append(out, l)
	}
	if !inserted {
		return "", errors.New("no auth line found – refusing to touch an unexpected PAM file")
	}
	return strings.Join(out, "\n") + "\n", nil
}

// UninstallPAM removes the helper line and re-enables disabled lines.
func UninstallPAM(content string) (string, bool) {
	changed := false
	var out []string
	for _, l := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		if strings.HasSuffix(strings.TrimSpace(l), pamMarker) && strings.Contains(l, "pam_exec.so") {
			changed = true
			continue
		}
		if r, ok := strings.CutPrefix(l, disabledTag); ok {
			l, changed = r, true
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n") + "\n", changed
}

func PAMInstalled(content string) bool {
	for _, l := range strings.Split(content, "\n") {
		if strings.HasSuffix(strings.TrimSpace(l), pamMarker) && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			return true
		}
	}
	return false
}

// WritePAMFile replaces path atomically, keeping its mode.
func WritePAMFile(path, content string) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := path + ".pocketsudo.tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
