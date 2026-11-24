package deck

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Window is an open window of an app, as the phone shows it.
type Window struct {
	Address   string `json:"address"`
	Title     string `json:"title"`
	Workspace string `json:"workspace"`
	Monitor   string `json:"monitor"`
	class     string
	pid       int
}

type hyprClient struct {
	Address   string `json:"address"`
	Class     string `json:"class"`
	Title     string `json:"title"`
	PID       int    `json:"pid"`
	Monitor   int    `json:"monitor"`
	Workspace struct {
		Name string `json:"name"`
	} `json:"workspace"`
}

type hyprMonitor struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Active struct {
		ID int `json:"id"`
	} `json:"activeWorkspace"`
}

func (a *Agent) hyprMonitors(ctx context.Context) []hyprMonitor {
	out, err := a.sys.Run(ctx, "hyprctl", "monitors", "-j")
	var ms []hyprMonitor
	if err == nil {
		json.Unmarshal([]byte(out), &ms)
	}
	return ms
}

func (a *Agent) monitorNames(ctx context.Context) map[int]string {
	names := map[int]string{}
	for _, m := range a.hyprMonitors(ctx) {
		names[m.ID] = m.Name
	}
	return names
}

func (a *Agent) windows(ctx context.Context) []Window {
	out, err := a.sys.Run(ctx, "hyprctl", "clients", "-j")
	if err != nil {
		return nil
	}
	var cs []hyprClient
	json.Unmarshal([]byte(out), &cs)
	names := a.monitorNames(ctx)
	ws := make([]Window, 0, len(cs))
	for _, c := range cs {
		ws = append(ws, Window{Address: c.Address, Title: c.Title, Workspace: c.Workspace.Name,
			Monitor: names[c.Monitor], class: c.Class, pid: c.PID})
	}
	return ws
}

// attachWindows assigns each window to an app: first by StartupWMClass or desktop
// id equal to the window class, then by binary name or the last part of a
// reverse-DNS id (org.kde.ark -> ark). Case-insensitive.
func attachWindows(apps []App, wins []Window) {
	exact := func(app App, cls string) bool {
		return strings.EqualFold(app.WMClass, cls) || strings.EqualFold(app.ID, cls)
	}
	loose := func(app App, cls string) bool {
		id := app.ID[strings.LastIndex(app.ID, ".")+1:]
		return strings.EqualFold(app.Bin, cls) || strings.EqualFold(id, cls)
	}
	for _, w := range wins {
		if w.class == "" {
			continue
		}
		for _, match := range []func(App, string) bool{exact, loose} {
			i := slices.IndexFunc(apps, func(app App) bool { return match(app, w.class) })
			if i >= 0 {
				apps[i].Running = append(apps[i].Running, w)
				break
			}
		}
	}
}

// window finds a real open window by address; the address later goes into a Lua string.
func (a *Agent) window(ctx context.Context, body json.RawMessage) (Window, error) {
	var p struct{ Address string }
	if err := decode(body, &p); err != nil {
		return Window{}, err
	}
	if _, err := strconv.ParseUint(strings.TrimPrefix(p.Address, "0x"), 16, 64); err != nil || !strings.HasPrefix(p.Address, "0x") {
		return Window{}, fmt.Errorf("no window %q", p.Address)
	}
	for _, w := range a.windows(ctx) {
		if w.Address == p.Address {
			return w, nil
		}
	}
	return Window{}, fmt.Errorf("window %s is gone", p.Address)
}

// windowMove puts a window on the workspace a screen shows right now, without moving the view.
func (a *Agent) windowMove(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	w, err := a.window(ctx, body)
	if err != nil {
		return nil, err
	}
	var p struct{ Monitor string }
	decode(body, &p)
	for _, m := range a.hyprMonitors(ctx) {
		if m.Name == p.Monitor {
			_, err := a.sys.Run(ctx, "hyprctl", "dispatch", fmt.Sprintf(
				`hl.dsp.window.move({ workspace = "%d", window = "address:%s", follow = false })`, m.Active.ID, w.Address))
			return nil, err
		}
	}
	return nil, fmt.Errorf("no screen %q", p.Monitor)
}

// windowClose asks the app to close the window, like Alt+F4 (it may ask to save).
func (a *Agent) windowClose(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	w, err := a.window(ctx, body)
	if err != nil {
		return nil, err
	}
	_, err = a.sys.Run(ctx, "hyprctl", "dispatch", fmt.Sprintf(`hl.dsp.window.close({ window = "address:%s" })`, w.Address))
	return nil, err
}

// windowKill ends the window's process at once, for apps that hang.
func (a *Agent) windowKill(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	w, err := a.window(ctx, body)
	if err != nil {
		return nil, err
	}
	if w.pid <= 1 {
		return nil, fmt.Errorf("window %s has no process", w.Address)
	}
	_, err = a.sys.Run(ctx, "kill", "-KILL", strconv.Itoa(w.pid))
	return nil, err
}
