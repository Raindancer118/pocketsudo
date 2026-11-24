package deck

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Raindancer118/pocketsudo/protocol"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type action struct {
	auth    bool // needs the phone's biometric signature on this very message
	session bool // needs a remote session opened with the fingerprint (or auth)
	fn      func(a *Agent, ctx context.Context, body json.RawMessage) (json.RawMessage, error)
}

var actions map[string]action

func init() {
	actions = map[string]action{
		"state":           {fn: (*Agent).state},
		"lock":            {fn: (*Agent).lock},
		"unlock":          {auth: true, fn: (*Agent).unlock},
		"volume.set":      {fn: (*Agent).volumeSet},
		"volume.mute":     {fn: (*Agent).volumeMute},
		"brightness.set":  {fn: (*Agent).brightnessSet},
		"media":           {fn: (*Agent).media},
		"workspace":       {fn: (*Agent).workspace},
		"apps.list":       {fn: (*Agent).appsList},
		"apps.icons":      {fn: (*Agent).appsIcons},
		"settings":        {fn: (*Agent).settings},
		"wifi.list":       {fn: (*Agent).wifiList},
		"wifi.set":        {fn: (*Agent).wifiSet},
		"wifi.connect":    {fn: (*Agent).wifiConnect},
		"wifi.disconnect": {fn: (*Agent).wifiDisconnect},
		"bt.list":         {fn: (*Agent).btList},
		"bt.set":          {fn: (*Agent).btSet},
		"bt.connect":      {fn: (*Agent).btConnect},
		"bt.disconnect":   {fn: (*Agent).btDisconnect},
		"night.set":       {fn: (*Agent).nightSet},
		"power.set":       {fn: (*Agent).powerSet},
		"audio.outputs":   {fn: (*Agent).audioOutputList},
		"audio.output":    {fn: (*Agent).audioOutputSet},
		"window.move":     {session: true, fn: (*Agent).windowMove},
		"window.close":    {session: true, fn: (*Agent).windowClose},
		"window.kill":     {session: true, fn: (*Agent).windowKill},
		"app.launch":      {fn: (*Agent).appLaunch},
		"open.url":        {fn: (*Agent).openURL},
		"clipboard.get":   {fn: (*Agent).clipboardGet},
		"clipboard.set":   {fn: (*Agent).clipboardSet},
		"dnd":             {fn: (*Agent).dnd},
		"command":         {auth: true, fn: (*Agent).command},
		"session.open":    {auth: true, fn: (*Agent).sessionOpen},
		"session.close":   {fn: (*Agent).sessionClose},
		"session.status":  {fn: (*Agent).sessionStatus},
		"key":             {session: true, fn: (*Agent).pressKey},
		"type":            {session: true, fn: (*Agent).typeText},
		"screenshot":      {session: true, fn: (*Agent).screenshot},
		"file.put":        {fn: (*Agent).filePut},
		"fs.list":         {session: true, fn: (*Agent).fsList},
		"fs.get":          {session: true, fn: (*Agent).fsGet},
		"fs.search":       {session: true, fn: (*Agent).fsSearch},
		"fs.zip":          {session: true, fn: (*Agent).fsZip},
		"stream.start":    {session: true, fn: (*Agent).streamStart},
		"stream.stop":     {fn: (*Agent).streamStop},
		"monitors.list":   {fn: (*Agent).monitorsList},
		"cameras.list":    {fn: (*Agent).camerasList},
		"stream.switch":   {session: true, fn: (*Agent).streamSwitch},
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
	Icon string `json:"-"` // Icon= of the desktop entry: a theme name or an absolute path
	// for matching open windows to the app
	WMClass string   `json:"-"`
	Bin     string   `json:"-"`
	Running []Window `json:"running,omitempty"`
}

func defaultDataDirs() []string {
	dirs := []string{}
	if d, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(d, ".local/share"), filepath.Join(d, ".local/share/flatpak/exports/share"))
	}
	return append(dirs, "/usr/share", "/var/lib/flatpak/exports/share")
}

func installedApps(dataDirs []string) []App {
	seen := map[string]bool{}
	var apps []App
	for _, base := range dataDirs {
		files, _ := filepath.Glob(filepath.Join(base, "applications", "*.desktop"))
		for _, f := range files {
			id := strings.TrimSuffix(filepath.Base(f), ".desktop")
			if seen[id] {
				continue
			}
			seen[id] = true
			e := readDesktop(f)
			if !e.hidden && e.name != "" {
				apps = append(apps, App{ID: id, Name: e.name, Icon: e.icon, WMClass: e.wmClass, Bin: e.bin})
			}
		}
	}
	sort.Slice(apps, func(i, j int) bool { return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name) })
	return apps
}

type desktopEntry struct {
	name, icon, wmClass, bin string
	hidden                   bool
}

func readDesktop(path string) (e desktopEntry) {
	f, err := os.Open(path)
	if err != nil {
		e.hidden = true
		return e
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
		k, v, _ := strings.Cut(l, "=")
		switch {
		case k == "Name" && e.name == "":
			e.name = v
		case k == "Icon" && e.icon == "":
			e.icon = v
		case k == "StartupWMClass":
			e.wmClass = v
		case k == "Exec" && e.bin == "":
			if fields := strings.Fields(v); len(fields) > 0 {
				e.bin = filepath.Base(fields[0])
			}
		case l == "NoDisplay=true", l == "Hidden=true":
			e.hidden = true
		case k == "Type" && v != "Application":
			e.hidden = true
		}
	}
	return e
}

func (a *Agent) appsList(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	apps := installedApps(a.dataDirs)
	attachWindows(apps, a.windows(ctx))
	return ok(apps)
}

func (a *Agent) appLaunch(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ ID string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	// only installed desktop entries: the phone can't smuggle in a command line here
	for _, app := range installedApps(a.dataDirs) {
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
	a.mu.Lock()
	a.lastClip = p.Text
	a.mu.Unlock()
	return nil, a.sys.Start(ctx, "wl-copy", "--", p.Text)
}

func (a *Agent) dnd(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ On bool }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	if handled, err := a.setVelaToggle(ctx, "dnd", p.On); handled {
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

// ---- remote session, keyboard ----

// SessionTTL is how long one fingerprint unlocks keyboard and screen control.
const SessionTTL = 15 * time.Minute

func (a *Agent) sessionOpen(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	a.mu.Lock()
	a.sessionUntil = time.Now().Add(SessionTTL)
	until := a.sessionUntil
	a.mu.Unlock()
	return ok(map[string]int64{"until": until.Unix()})
}

func (a *Agent) sessionClose(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	a.mu.Lock()
	a.sessionUntil = time.Time{}
	a.mu.Unlock()
	return nil, nil
}

// sessionStatus tells the phone whether it still has to ask for the finger.
func (a *Agent) sessionStatus(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	a.mu.Lock()
	until := a.sessionUntil
	a.mu.Unlock()
	if time.Now().After(until) {
		return ok(map[string]int64{"until": 0})
	}
	return ok(map[string]int64{"until": until.Unix()})
}

func (a *Agent) sessionActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Now().Before(a.sessionUntil)
}

var modifiers = map[string]string{"ctrl": "ctrl", "control": "ctrl", "strg": "ctrl", "alt": "alt",
	"shift": "shift", "super": "logo", "win": "logo", "meta": "logo", "logo": "logo", "altgr": "altgr"}

var namedKeys = map[string]string{"enter": "Return", "return": "Return", "esc": "Escape", "escape": "Escape",
	"tab": "Tab", "backspace": "BackSpace", "delete": "Delete", "del": "Delete", "entf": "Delete", "space": "space",
	"up": "Up", "down": "Down", "left": "Left", "right": "Right", "home": "Home", "end": "End",
	"pageup": "Prior", "pagedown": "Next", "print": "Print", "insert": "Insert", "menu": "Menu",
	"volumeup": "XF86AudioRaiseVolume", "volumedown": "XF86AudioLowerVolume", "mute": "XF86AudioMute",
	"capslock": "Caps_Lock",
	// German layout and punctuation, as xkb keysym names (wtype maps them itself)
	"ssharp": "ssharp", "adiaeresis": "adiaeresis", "odiaeresis": "odiaeresis", "udiaeresis": "udiaeresis",
	"asciicircum": "asciicircum", "less": "less", "numbersign": "numbersign", "plus": "plus",
	"comma": "comma", "period": "period", "minus": "minus", "dead_acute": "dead_acute"}

func keysym(name string) (string, bool) {
	n := strings.ToLower(name)
	if k, ok := namedKeys[n]; ok {
		return k, true
	}
	if len(n) == 1 && ((n[0] >= 'a' && n[0] <= 'z') || (n[0] >= '0' && n[0] <= '9')) {
		return n, true
	}
	if len(n) >= 2 && len(n) <= 3 && n[0] == 'f' {
		if f, err := strconv.Atoi(n[1:]); err == nil && f >= 1 && f <= 24 {
			return "F" + n[1:], true
		}
	}
	return "", false
}

// pressKey presses a combination like "ctrl+alt+t" or a single key ("super", "f5").
func (a *Agent) pressKey(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Combo string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	parts := strings.Split(strings.ReplaceAll(p.Combo, " ", ""), "+")
	var mods []string
	key := ""
	for i, part := range parts {
		last := i == len(parts)-1
		if m, ok := modifiers[strings.ToLower(part)]; ok && !(last && len(parts) == 1) {
			if last {
				return nil, fmt.Errorf("combo %q ends with a modifier", p.Combo)
			}
			mods = append(mods, m)
			continue
		}
		if !last {
			return nil, fmt.Errorf("unknown modifier %q", part)
		}
		if m, ok := modifiers[strings.ToLower(part)]; ok && len(parts) == 1 {
			// Super alone means "launcher": vela directly when it runs
			if m == "logo" {
				if _, err := a.sys.Run(ctx, "vela", "status"); err == nil {
					_, err = a.sys.Run(ctx, "vela", "toggle")
					return nil, err
				}
			}
			// otherwise the real evdev key: wtype's virtual keymap has other keycodes,
			// and tap-Super binds match keycodes
			code := map[string]string{"logo": "125", "ctrl": "29", "alt": "56", "shift": "42", "altgr": "100"}[m]
			_, err := a.sys.Run(ctx, "ydotool", "key", code+":1", code+":0")
			return nil, err
		}
		k, ok := keysym(part)
		if !ok {
			return nil, fmt.Errorf("unknown key %q", part)
		}
		key = k
	}
	args := []string{}
	for _, m := range mods {
		args = append(args, "-M", m)
	}
	args = append(args, "-k", key)
	for i := len(mods) - 1; i >= 0; i-- {
		args = append(args, "-m", mods[i])
	}
	_, err := a.sys.Run(ctx, "wtype", args...)
	return nil, err
}

func (a *Agent) typeText(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Text string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	if p.Text == "" || len(p.Text) > 4000 {
		return nil, errors.New("text must be 1–4000 characters")
	}
	_, err := a.sys.Run(ctx, "wtype", "--", p.Text)
	return nil, err
}

// ---- files ----

func defaultDownloadDir() string {
	home, _ := os.UserHomeDir()
	if out, err := exec.Command("xdg-user-dir", "DOWNLOAD").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		return filepath.Join(strings.TrimSpace(string(out)), "PocketDeck")
	}
	return filepath.Join(home, "Downloads", "PocketDeck")
}

// screenshot seals one JPEG per monitor (left to right) into blobs.
func (a *Agent) screenshot(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	type screen struct {
		Name   string `json:"name"`
		Blob   string `json:"blob"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
		X      int    `json:"-"`
	}
	var mons []screen
	if out, err := a.sys.Run(ctx, "hyprctl", "monitors", "-j"); err == nil {
		json.Unmarshal([]byte(out), &mons)
	}
	sort.Slice(mons, func(i, j int) bool { return mons[i].X < mons[j].X })
	if len(mons) == 0 {
		mons = []screen{{Name: ""}} // not Hyprland: one image of everything
	}
	var shots []screen
	for _, m := range mons {
		args := []string{"-t", "jpeg", "-q", "85"}
		if m.Name != "" {
			args = append(args, "-o", m.Name)
		}
		img, err := a.sys.Run(ctx, "grim", append(args, "-")...)
		if err != nil || len(img) == 0 {
			continue
		}
		id, err := a.upload(ctx, []byte(img))
		if err != nil {
			return nil, err
		}
		m.Blob = id
		shots = append(shots, m)
	}
	if len(shots) == 0 {
		return nil, errors.New("screenshot failed")
	}
	return ok(map[string]any{"screens": shots})
}

func (a *Agent) upload(ctx context.Context, plain []byte) (string, error) {
	box, err := protocol.SealBytes(a.key, plain)
	if err != nil {
		return "", err
	}
	var up protocol.BlobResp
	if _, err := a.client.Raw(ctx, "PUT", "/v1/channels/"+a.cfg.Channel+"/blobs", box, &up); err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	return up.ID, nil
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N} ._()\-]+`)

// cleanName keeps a file name inside the download folder.
func cleanName(name string) string {
	n := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	n = strings.TrimSpace(unsafeName.ReplaceAllString(n, "_"))
	n = strings.TrimLeft(n, ".")
	if n == "" || n == "_" {
		n = "datei"
	}
	if len(n) > 120 {
		n = n[len(n)-120:]
	}
	return n
}

func freePath(dir, name string) string {
	p := filepath.Join(dir, name)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
}

func (a *Agent) filePut(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Blob, Name, Dir string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	target := a.downloadDir
	if p.Dir != "" {
		if !a.sessionActive() {
			return nil, errNeedsSession
		}
		dir, err := a.inHome(p.Dir)
		if err != nil {
			return nil, err
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return nil, errors.New("not a folder")
		}
		target = dir
	}
	var box []byte
	if _, err := a.client.Raw(ctx, "GET", "/v1/channels/"+a.cfg.Channel+"/blobs/"+p.Blob, nil, &box); err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	data, err := protocol.OpenBytes(a.key, box)
	if err != nil {
		return nil, errors.New("file could not be decrypted")
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, err
	}
	path := freePath(target, cleanName(p.Name))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	a.sys.Start(ctx, "notify-send", "-a", "PocketDeck", "Datei empfangen", filepath.Base(path))
	return ok(map[string]string{"path": path})
}

// ---- file browser (home directory only) ----

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// inHome resolves a path relative to home (symlinks included) and refuses
// anything that ends up outside of it.
func (a *Agent) inHome(rel string) (string, error) {
	home, err := filepath.EvalSymlinks(a.home)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("paths are relative to your home folder")
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == ".." {
			return "", errors.New("outside your home folder")
		}
	}
	p, err := filepath.EvalSymlinks(filepath.Join(home, filepath.Clean("/"+rel)))
	if err != nil {
		return "", errors.New("not found")
	}
	if p != home && !strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "", errors.New("outside your home folder")
	}
	return p, nil
}

type fsEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

func (a *Agent) fsList(_ context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Path   string
		Hidden bool
	}
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	dir, err := a.inHome(p.Path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []fsEntry{}
	for _, e := range entries {
		if !p.Hidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, e.Name())) // follows links for type and size
		if err != nil {
			continue
		}
		out = append(out, fsEntry{Name: e.Name(), Dir: fi.IsDir(), Size: fi.Size(), MTime: fi.ModTime().Unix()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	home, _ := filepath.EvalSymlinks(a.home)
	rel, _ := filepath.Rel(home, dir)
	if rel == "." {
		rel = ""
	}
	return ok(map[string]any{"path": rel, "entries": out})
}

const maxDownload = 24 << 20

func (a *Agent) fsGet(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Path string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	path, err := a.inHome(p.Path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return nil, errors.New("not a file")
	}
	if fi.Size() > maxDownload {
		return nil, fmt.Errorf("file too large (%d MB, max 24 MB)", fi.Size()>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	id, err := a.upload(ctx, data)
	if err != nil {
		return nil, err
	}
	return ok(map[string]any{"blob": id, "name": filepath.Base(path), "size": len(data)})
}

const maxSearchResults = 200

// fsSearch finds names containing the query below path (hidden folders skipped).
func (a *Agent) fsSearch(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Path, Query string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(p.Query))
	if len(q) < 2 {
		return nil, errors.New("type at least 2 characters")
	}
	root, err := a.inHome(p.Path)
	if err != nil {
		return nil, err
	}
	home, _ := filepath.EvalSymlinks(a.home)
	var out []map[string]any
	deadline := time.Now().Add(8 * time.Second)
	errStop := errors.New("stop")
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) || len(out) >= maxSearchResults {
			return errStop
		}
		name := d.Name()
		if path != root && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && (name == "node_modules" || name == "target" || name == "build") && path != root {
			return filepath.SkipDir
		}
		if path != root && strings.Contains(strings.ToLower(name), q) {
			rel, _ := filepath.Rel(home, path)
			e := map[string]any{"path": rel, "name": name, "dir": d.IsDir()}
			if fi, err := d.Info(); err == nil {
				e["size"], e["mtime"] = fi.Size(), fi.ModTime().Unix()
			}
			out = append(out, e)
		}
		return nil
	})
	if out == nil {
		out = []map[string]any{}
	}
	return ok(map[string]any{"results": out, "truncated": len(out) >= maxSearchResults})
}

// fsZip packs files and folders (relative to home) into one zip for the phone.
func (a *Agent) fsZip(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Paths []string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	if len(p.Paths) == 0 || len(p.Paths) > 500 {
		return nil, errors.New("select 1–500 entries")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	total := int64(0)
	add := func(base, path string, fi os.FileInfo) error {
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		if !fi.Mode().IsRegular() {
			return nil // sockets, devices, broken links
		}
		total += fi.Size()
		if total > maxDownload {
			return fmt.Errorf("selection too large (max %d MB)", maxDownload>>20)
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	}
	for _, rel := range p.Paths {
		path, err := a.inHome(rel)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		base := filepath.Dir(path)
		err = filepath.Walk(path, func(p string, fi os.FileInfo, err error) error {
			if err != nil || ctx.Err() != nil {
				return err
			}
			if fi.IsDir() && p != path && strings.HasPrefix(fi.Name(), ".") {
				return filepath.SkipDir
			}
			return add(base, p, fi)
		})
		if err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	id, err := a.upload(ctx, buf.Bytes())
	if err != nil {
		return nil, err
	}
	name := "pocketdeck.zip"
	if len(p.Paths) == 1 {
		name = filepath.Base(p.Paths[0]) + ".zip"
	}
	return ok(map[string]any{"blob": id, "name": name, "size": buf.Len()})
}
