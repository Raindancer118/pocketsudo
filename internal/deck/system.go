package deck

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Desktop runs real commands with the Wayland/Hyprland environment of the
// user's session, which a systemd user service does not always inherit.
type Desktop struct{ env []string }

func NewDesktop() *Desktop { return &Desktop{env: sessionEnv()} }

func sessionEnv() []string {
	env := os.Environ()
	have := func(k string) bool { return os.Getenv(k) != "" }
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = fmt.Sprintf("/run/user/%d", os.Getuid())
		env = append(env, "XDG_RUNTIME_DIR="+rt)
	}
	if !have("HYPRLAND_INSTANCE_SIGNATURE") {
		if sig := newestDir(filepath.Join(rt, "hypr")); sig != "" {
			env = append(env, "HYPRLAND_INSTANCE_SIGNATURE="+sig)
		}
	}
	if !have("WAYLAND_DISPLAY") {
		if socks, _ := filepath.Glob(filepath.Join(rt, "wayland-[0-9]")); len(socks) > 0 {
			sort.Strings(socks)
			env = append(env, "WAYLAND_DISPLAY="+filepath.Base(socks[len(socks)-1]))
		}
	}
	if !have("DBUS_SESSION_BUS_ADDRESS") {
		env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(rt, "bus"))
	}
	if !have("YDOTOOL_SOCKET") {
		env = append(env, "YDOTOOL_SOCKET="+filepath.Join(rt, ".ydotool_socket"))
	}
	return env
}

func newestDir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best, bestT := "", int64(0)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && e.IsDir() && fi.ModTime().UnixNano() > bestT {
			best, bestT = e.Name(), fi.ModTime().UnixNano()
		}
	}
	return best
}

func (d *Desktop) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = d.env
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return string(out), fmt.Errorf("%s: %s", name, strings.TrimSpace(string(ee.Stderr)))
		}
		return string(out), fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}

// Start launches a GUI program detached from the agent (own session, no
// output), so it survives agent restarts.
func (d *Desktop) Start(_ context.Context, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = d.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	go cmd.Wait()
	return nil
}

func (d *Desktop) Stream(ctx context.Context, onLine func(string), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = d.env
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		onLine(sc.Text())
	}
	return cmd.Wait()
}
