package host

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// SessionFromPAM reads the pam_exec environment plus the calling process
// (pam_exec's parent is sudo itself) to describe what is being authorized.
func SessionFromPAM() Session {
	host, _ := os.Hostname()
	s := Session{
		Hostname: host,
		Service:  os.Getenv("PAM_SERVICE"),
		User:     os.Getenv("PAM_USER"),
		TTY:      os.Getenv("PAM_TTY"),
		RHost:    os.Getenv("PAM_RHOST"),
	}
	ppid := os.Getppid()
	s.Command = processCommand(ppid)
	if cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", ppid)); err == nil {
		s.Cwd = cwd
	}
	s.Remote = s.RHost != "" || isSSHSession(ppid)
	return s
}

func processCommand(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	for i, a := range args {
		if strings.ContainsAny(a, " \t'\"$") {
			args[i] = strconv.Quote(a)
		}
	}
	if len(args) > 0 {
		args[0] = filepath.Base(args[0])
	}
	return strings.Join(args, " ")
}

// isSSHSession walks up from pid looking for SSH_CONNECTION in an ancestor's
// environment or an sshd ancestor.
func isSSHSession(pid int) bool {
	for i := 0; i < 32 && pid > 1; i++ {
		if env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)); err == nil {
			if bytes.Contains(env, []byte("\x00SSH_CONNECTION=")) || bytes.HasPrefix(env, []byte("SSH_CONNECTION=")) {
				return true
			}
		}
		comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if strings.HasPrefix(strings.TrimSpace(string(comm)), "sshd") {
			return true
		}
		pid = parentPID(pid)
	}
	return false
}

func parentPID(pid int) int {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PPid:"); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return 0
}

// FprintdVerifier uses fprintd-verify, giving the user up to three tries like
// pam_fprintd does. It returns nil only on a match.
func FprintdVerifier() LocalVerifier {
	bin, err := exec.LookPath("fprintd-verify")
	if err != nil {
		return nil
	}
	return func(ctx context.Context, user string) error {
		var last string
		for try := 0; try < 3; try++ {
			cmd := exec.CommandContext(ctx, bin, user)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()
			if err == nil && strings.Contains(out.String(), "verify-match") {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			text := out.String()
			switch {
			case strings.Contains(text, "verify-no-match"):
				last = "no match"
			case strings.Contains(text, "No devices available"), strings.Contains(text, "no enrolled"),
				strings.Contains(text, "NoEnrolledPrints"), strings.Contains(text, "verify-disconnected"):
				return errors.New("unavailable")
			default:
				return errors.New("unavailable")
			}
		}
		return errors.New(last)
	}
}

// TTYSkip returns a channel that fires when the user presses Enter on the
// controlling terminal, or nil when there is none (e.g. GUI sudo).
func TTYSkip() <-chan struct{} {
	f, err := os.Open("/dev/tty")
	if err != nil {
		return nil
	}
	ch := make(chan struct{})
	go func() {
		defer f.Close()
		buf := make([]byte, 256)
		for {
			n, err := f.Read(buf)
			if err != nil {
				return
			}
			if bytes.ContainsAny(buf[:n], "\n\r") {
				close(ch)
				return
			}
		}
	}()
	return ch
}
