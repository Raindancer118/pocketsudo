package deck

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type action struct {
	auth bool // needs the phone's biometric signature
	fn   func(a *Agent, ctx context.Context, body json.RawMessage) (json.RawMessage, error)
}

var actions map[string]action

func init() {
	actions = map[string]action{
		"state":          {fn: (*Agent).state},
		"lock":           {fn: (*Agent).lock},
		"unlock":         {auth: true, fn: (*Agent).unlock},
		"volume.set":     {fn: (*Agent).volumeSet},
		"volume.mute":    {fn: (*Agent).volumeMute},
		"brightness.set": {fn: (*Agent).brightnessSet},
		"media":          {fn: (*Agent).media},
		"workspace":      {fn: (*Agent).workspace},
		"apps.list":      {fn: (*Agent).appsList},
		"app.launch":     {fn: (*Agent).appLaunch},
		"open.url":       {fn: (*Agent).openURL},
		"clipboard.get":  {fn: (*Agent).clipboardGet},
		"clipboard.set":  {fn: (*Agent).clipboardSet},
		"dnd":            {fn: (*Agent).dnd},
		"command":        {auth: true, fn: (*Agent).command},
	}
}

func decode(body json.RawMessage, v any) error {
	if len(body) == 0 {
		body = []byte("{}")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

func ok(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	return b, err
}

// ---- state ----

type Media struct {
	Player  string `json:"player,omitempty"`
	Playing bool   `json:"playing"`
	Title   string `json:"title,omitempty"`
	Artist  string `json:"artist,omitempty"`
	Album   string `json:"album,omitempty"`
	ArtURL  string `json:"art_url,omitempty"`
}

type State struct {
	Hostname   string `json:"hostname"`
	Locked     bool   `json:"locked"`
	Battery    int    `json:"battery"` // -1 without battery
	Charging   bool   `json:"charging"`
	Volume     int    `json:"volume"`
	Muted      bool   `json:"muted"`
	Brightness int    `json:"brightness"`
	Workspace  int    `json:"workspace,omitempty"`
	Media      *Media `json:"media,omitempty"`
}

func (a *Agent) state(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	st := State{Hostname: a.hostname, Battery: -1}
	st.Locked = a.locked(ctx)
	st.Battery, st.Charging = a.battery()
	if out, err := a.sys.Run(ctx, "wpctl", "get-volume", "@DEFAULT_AUDIO_SINK@"); err == nil {
		st.Volume, st.Muted = parseVolume(out)
	}
	if out, err := a.sys.Run(ctx, "brightnessctl", "-m"); err == nil {
		st.Brightness = parseBrightness(out)
	}
	if out, err := a.sys.Run(ctx, "hyprctl", "activeworkspace", "-j"); err == nil {
		var ws struct{ ID int }
		if json.Unmarshal([]byte(out), &ws) == nil {
			st.Workspace = ws.ID
		}
	}
	st.Media = a.media_(ctx)
	return ok(st)
}

func (a *Agent) locked(ctx context.Context) bool {
	out, err := a.sys.Run(ctx, "pidof", "hyprlock")
	return err == nil && strings.TrimSpace(out) != ""
}

func (a *Agent) battery() (int, bool) {
	dirs, _ := filepath.Glob(filepath.Join(a.batteryDir, "BAT*"))
	for _, d := range dirs {
		capb, err := os.ReadFile(filepath.Join(d, "capacity"))
		if err != nil {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(capb)))
		if err != nil {
			continue
		}
		status, _ := os.ReadFile(filepath.Join(d, "status"))
		s := strings.TrimSpace(string(status))
		return n, s == "Charging" || s == "Full"
	}
	return -1, false
}

// "Volume: 0.62 [MUTED]"
func parseVolume(out string) (int, bool) {
	f := strings.Fields(out)
	if len(f) < 2 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[1], 64)
	if err != nil {
		return 0, false
	}
	return int(v*100 + 0.5), strings.Contains(out, "MUTED")
}

// "intel_backlight,backlight,176,35%,496"
func parseBrightness(out string) int {
	f := strings.Split(strings.TrimSpace(strings.Split(out, "\n")[0]), ",")
	if len(f) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSuffix(f[3], "%"))
	return n
}

func (a *Agent) media_(ctx context.Context) *Media {
	out, err := a.sys.Run(ctx, "playerctl", "metadata", "--format",
		"{{status}}\t{{playerName}}\t{{title}}\t{{artist}}\t{{album}}\t{{mpris:artUrl}}")
	if err != nil || strings.TrimSpace(out) == "" {
		return nil
	}
	f := strings.Split(strings.TrimRight(out, "\n"), "\t")
	for len(f) < 6 {
		f = append(f, "")
	}
	m := &Media{Playing: f[0] == "Playing", Player: f[1], Title: f[2], Artist: f[3], Album: f[4]}
	// only remote covers are useful to the phone; file:// art stays on the notebook
	if strings.HasPrefix(f[5], "https://") {
		m.ArtURL = f[5]
	}
	return m
}

// ---- lock ----

func (a *Agent) lock(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	_, err := a.sys.Run(ctx, "loginctl", "lock-session")
	return nil, err
}

func (a *Agent) unlock(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	if !a.locked(ctx) {
		return nil, errors.New("not locked")
	}
	// hyprlock unlocks on SIGUSR1 – that is why this action needs the finger
	_, err := a.sys.Run(ctx, "pkill", "-USR1", "hyprlock")
	return nil, err
}

// ---- audio, light, media ----

func (a *Agent) volumeSet(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Percent int }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	if p.Percent < 0 || p.Percent > 150 {
		return nil, errors.New("volume must be 0–150 %")
	}
	_, err := a.sys.Run(ctx, "wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", fmt.Sprintf("%.2f", float64(p.Percent)/100))
	return nil, err
}

func (a *Agent) volumeMute(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Mute bool }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	v := "0"
	if p.Mute {
		v = "1"
	}
	_, err := a.sys.Run(ctx, "wpctl", "set-mute", "@DEFAULT_AUDIO_SINK@", v)
	return nil, err
}

func (a *Agent) brightnessSet(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Percent int }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	if p.Percent < 1 || p.Percent > 100 {
		return nil, errors.New("brightness must be 1–100 %")
	}
	_, err := a.sys.Run(ctx, "brightnessctl", "set", strconv.Itoa(p.Percent)+"%")
	return nil, err
}

func (a *Agent) media(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Action string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	cmd := map[string]string{"play_pause": "play-pause", "next": "next", "previous": "previous", "play": "play", "pause": "pause"}[p.Action]
	if cmd == "" {
		return nil, fmt.Errorf("unknown media action %q", p.Action)
	}
	_, err := a.sys.Run(ctx, "playerctl", cmd)
	return nil, err
}

// ---- hyprland, apps, links ----

func (a *Agent) workspace(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ ID int }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	if p.ID < 1 || p.ID > 50 {
		return nil, errors.New("workspace must be 1–50")
	}
	// Hyprland ≥ 0.55 takes Lua dispatchers; older versions the classic syntax
	out, err := a.sys.Run(ctx, "hyprctl", "dispatch", fmt.Sprintf("hl.dsp.focus({ workspace = %d })", p.ID))
	if err != nil || strings.Contains(strings.ToLower(out), "error") {
		_, err = a.sys.Run(ctx, "hyprctl", "dispatch", "workspace", strconv.Itoa(p.ID))
	}
	return nil, err
}

type App struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func desktopDirs() []string {
	dirs := []string{}
	if d, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(d, ".local/share/applications"))
	}
	return append(dirs, "/usr/share/applications", "/var/lib/flatpak/exports/share/applications")
}

func installedApps() []App {
	seen := map[string]bool{}
	var apps []App
	for _, dir := range desktopDirs() {
		files, _ := filepath.Glob(filepath.Join(dir, "*.desktop"))
		for _, f := range files {
			id := strings.TrimSuffix(filepath.Base(f), ".desktop")
			if seen[id] {
				continue
			}
			seen[id] = true
			name, hidden := readDesktop(f)
			if !hidden && name != "" {
				apps = append(apps, App{ID: id, Name: name})
			}
		}
	}
	sort.Slice(apps, func(i, j int) bool { return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name) })
	return apps
}

func readDesktop(path string) (name string, hidden bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", true
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	inEntry := false
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "[") {
			inEntry = l == "[Desktop Entry]"
			continue
		}
		if !inEntry {
			continue
		}
		switch {
		case strings.HasPrefix(l, "Name=") && name == "":
			name = strings.TrimPrefix(l, "Name=")
		case l == "NoDisplay=true", l == "Hidden=true":
			hidden = true
		case strings.HasPrefix(l, "Type=") && l != "Type=Application":
			hidden = true
		}
	}
	return name, hidden
}

func (a *Agent) appsList(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return ok(installedApps())
}

func (a *Agent) appLaunch(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ ID string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	// only installed desktop entries: the phone can't smuggle in a command line here
	for _, app := range installedApps() {
		if app.ID == p.ID {
			return nil, a.sys.Start(ctx, "gtk-launch", app.ID)
		}
	}
	return nil, fmt.Errorf("no installed app %q", p.ID)
}

func (a *Agent) openURL(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ URL string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("only http(s) links")
	}
	return nil, a.sys.Start(ctx, "xdg-open", u.String())
}

// ---- clipboard, notifications ----

const maxClip = 100 << 10

func (a *Agent) clipboardGet(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	out, err := a.sys.Run(ctx, "wl-paste", "--no-newline", "--type", "text")
	if err != nil {
		return nil, errors.New("clipboard is empty or holds no text")
	}
	if len(out) > maxClip {
		out = out[:maxClip]
	}
	return ok(map[string]string{"text": out})
}

func (a *Agent) clipboardSet(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Text string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	if len(p.Text) > maxClip {
		return nil, errors.New("text too long for the clipboard")
	}
	return nil, a.sys.Start(ctx, "wl-copy", "--", p.Text)
}

func (a *Agent) dnd(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ On bool }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	flag := "-r"
	if p.On {
		flag = "-a"
	}
	if _, err := a.sys.Run(ctx, "makoctl", "mode", flag, "do-not-disturb"); err != nil {
		return nil, errors.New("no notification daemon with do-not-disturb (mako) running")
	}
	return nil, nil
}

func (a *Agent) command(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Cmd string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Cmd) == "" {
		return nil, errors.New("empty command")
	}
	out, err := a.sys.Run(ctx, "sh", "-c", p.Cmd)
	if len(out) > 8<<10 {
		out = out[len(out)-8<<10:]
	}
	if err != nil {
		return ok(map[string]string{"output": out, "error": err.Error()})
	}
	return ok(map[string]string{"output": out})
}
