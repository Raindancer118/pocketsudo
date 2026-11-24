package deck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Settings is the quick-settings overview on the phone's start screen.
type Settings struct {
	WiFi struct {
		On   bool   `json:"on"`
		SSID string `json:"ssid,omitempty"`
	} `json:"wifi"`
	Bluetooth struct {
		On     bool   `json:"on"`
		Device string `json:"device,omitempty"` // first connected device
	} `json:"bluetooth"`
	DND        bool   `json:"dnd"`
	NightLight bool   `json:"night_light"`
	Power      string `json:"power,omitempty"`  // power-saver, balanced, performance
	Output     string `json:"output,omitempty"` // description of the default sink
}

type WifiNet struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal"`
	Secure bool   `json:"secure"`
	Known  bool   `json:"known"` // saved profile, connects without a password
	Active bool   `json:"active"`
}

type BTDevice struct {
	MAC       string `json:"mac"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

type AudioOutput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Default     bool   `json:"default"`
}

var powerProfiles = []string{"power-saver", "balanced", "performance"}

func (a *Agent) settings(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	var s Settings
	if out, err := a.sys.Run(ctx, "nmcli", "radio", "wifi"); err == nil {
		s.WiFi.On = strings.TrimSpace(out) == "enabled"
	}
	for _, d := range a.netDevices(ctx) {
		if d.typ == "wifi" && d.state == "connected" {
			s.WiFi.SSID = d.conn
		}
	}
	if out, err := a.sys.Run(ctx, "bluetoothctl", "show"); err == nil {
		s.Bluetooth.On = strings.Contains(out, "Powered: yes")
	}
	for _, d := range a.btDevices(ctx) {
		if d.Connected {
			s.Bluetooth.Device = d.Name
			break
		}
	}
	s.DND, s.NightLight = a.shellToggles(ctx)
	if out, err := a.sys.Run(ctx, "powerprofilesctl", "get"); err == nil {
		s.Power = strings.TrimSpace(out)
	}
	for _, o := range a.audioOutputs(ctx) {
		if o.Default {
			s.Output = o.Description
		}
	}
	return ok(s)
}

// ---- WLAN ----

type netDevice struct{ dev, typ, state, conn string }

func (a *Agent) netDevices(ctx context.Context) []netDevice {
	out, err := a.sys.Run(ctx, "nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "dev")
	if err != nil {
		return nil
	}
	var ds []netDevice
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := nmFields(l)
		if len(f) == 4 {
			ds = append(ds, netDevice{f[0], f[1], f[2], f[3]})
		}
	}
	return ds
}

// nmFields splits nmcli -t output: ':' separates, '\:' is a colon inside a value.
func nmFields(line string) []string {
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
		case line[i] == ':':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	return append(fields, cur.String())
}

func (a *Agent) wifiList(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	out, err := a.sys.Run(ctx, "nmcli", "-t", "-f", "IN-USE,SSID,SIGNAL,SECURITY", "dev", "wifi", "list")
	if err != nil {
		return nil, errors.New("no WLAN (NetworkManager) available")
	}
	known := map[string]bool{}
	if saved, err := a.sys.Run(ctx, "nmcli", "-t", "-f", "NAME,TYPE", "connection", "show"); err == nil {
		for _, l := range strings.Split(saved, "\n") {
			if f := nmFields(l); len(f) == 2 && f[1] == "802-11-wireless" {
				known[f[0]] = true
			}
		}
	}
	best := map[string]WifiNet{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := nmFields(l)
		if len(f) != 4 || f[1] == "" {
			continue // hidden networks have no name to show
		}
		sig, _ := strconv.Atoi(f[2])
		n := WifiNet{SSID: f[1], Signal: sig, Secure: f[3] != "" && f[3] != "--", Known: known[f[1]], Active: f[0] == "*"}
		// one row per name: several access points of one network show up separately
		if old, seen := best[n.SSID]; !seen || n.Active || (!old.Active && n.Signal > old.Signal) {
			best[n.SSID] = n
		}
	}
	nets := make([]WifiNet, 0, len(best))
	for _, n := range best {
		nets = append(nets, n)
	}
	sort.Slice(nets, func(i, j int) bool {
		if nets[i].Active != nets[j].Active {
			return nets[i].Active
		}
		return nets[i].Signal > nets[j].Signal
	})
	return ok(nets)
}

// onlyWifiOnline: the notebook reaches the relay only over WLAN, so switching
// it off from the phone would lock the phone out until someone is at the notebook.
func (a *Agent) onlyWifiOnline(ctx context.Context) bool {
	out, err := a.sys.Run(ctx, "ip", "route", "show", "default")
	if err != nil {
		return false
	}
	wifi := map[string]bool{}
	for _, d := range a.netDevices(ctx) {
		if d.typ == "wifi" {
			wifi[d.dev] = true
		}
	}
	sawWifi := false
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if i := slices.Index(f, "dev"); i >= 0 && i+1 < len(f) {
			if !wifi[f[i+1]] {
				return false // another way out (cable, tethering …)
			}
			sawWifi = true
		}
	}
	return sawWifi
}

var errUnreachable = errors.New("the notebook would become unreachable: WLAN is its only connection")

func (a *Agent) wifiSet(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ On, Confirm bool }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	if !p.On && !p.Confirm && a.onlyWifiOnline(ctx) {
		return nil, errUnreachable
	}
	state := "off"
	if p.On {
		state = "on"
	}
	_, err := a.sys.Run(ctx, "nmcli", "radio", "wifi", state)
	return nil, err
}

func (a *Agent) wifiConnect(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ SSID, Password string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	// arguments go straight to nmcli (no shell), but a leading dash would read as an option
	if p.SSID == "" || strings.HasPrefix(p.SSID, "-") || len(p.SSID) > 32 {
		return nil, fmt.Errorf("invalid network name %q", p.SSID)
	}
	if saved, err := a.sys.Run(ctx, "nmcli", "-t", "-f", "NAME,TYPE", "connection", "show"); err == nil && p.Password == "" {
		for _, l := range strings.Split(saved, "\n") {
			if f := nmFields(l); len(f) == 2 && f[0] == p.SSID && f[1] == "802-11-wireless" {
				_, err := a.sys.Run(ctx, "nmcli", "connection", "up", "id", p.SSID)
				return nil, err
			}
		}
	}
	args := []string{"dev", "wifi", "connect", p.SSID}
	if p.Password != "" {
		args = append(args, "password", p.Password)
	}
	if out, err := a.sys.Run(ctx, "nmcli", args...); err != nil {
		return nil, fmt.Errorf("connect failed: %s", strings.TrimSpace(out))
	}
	return nil, nil
}

func (a *Agent) wifiDisconnect(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Confirm bool }
	if len(body) > 0 {
		decode(body, &p)
	}
	if !p.Confirm && a.onlyWifiOnline(ctx) {
		return nil, errUnreachable
	}
	for _, d := range a.netDevices(ctx) {
		if d.typ == "wifi" && d.state == "connected" {
			_, err := a.sys.Run(ctx, "nmcli", "dev", "disconnect", d.dev)
			return nil, err
		}
	}
	return nil, errors.New("not connected to a WLAN")
}

// ---- Bluetooth ----

var macRe = regexp.MustCompile(`^[0-9A-F]{2}(:[0-9A-F]{2}){5}$`)

func parseBTDevices(out string) map[string]string {
	devs := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(l), " ", 3)
		if len(f) == 3 && f[0] == "Device" && macRe.MatchString(f[1]) {
			devs[f[1]] = f[2]
		}
	}
	return devs
}

func (a *Agent) btDevices(ctx context.Context) []BTDevice {
	paired, err := a.sys.Run(ctx, "bluetoothctl", "devices", "Paired")
	if err != nil {
		return nil
	}
	connected := map[string]string{}
	if out, err := a.sys.Run(ctx, "bluetoothctl", "devices", "Connected"); err == nil {
		connected = parseBTDevices(out)
	}
	var ds []BTDevice
	for mac, name := range parseBTDevices(paired) {
		_, on := connected[mac]
		ds = append(ds, BTDevice{MAC: mac, Name: name, Connected: on})
	}
	sort.Slice(ds, func(i, j int) bool {
		if ds[i].Connected != ds[j].Connected {
			return ds[i].Connected
		}
		return strings.ToLower(ds[i].Name) < strings.ToLower(ds[j].Name)
	})
	return ds
}

func (a *Agent) btList(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return ok(a.btDevices(ctx))
}

func (a *Agent) btSet(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ On bool }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	state := "off"
	if p.On {
		state = "on"
	}
	_, err := a.sys.Run(ctx, "bluetoothctl", "power", state)
	return nil, err
}

func (a *Agent) btDevice(ctx context.Context, body json.RawMessage, verb string) (json.RawMessage, error) {
	var p struct{ MAC string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	mac := strings.ToUpper(p.MAC)
	if !macRe.MatchString(mac) {
		return nil, fmt.Errorf("invalid device %q", p.MAC)
	}
	out, err := a.sys.Run(ctx, "bluetoothctl", verb, mac)
	if err != nil || strings.Contains(out, "Failed") {
		return nil, fmt.Errorf("%s failed: %s", verb, strings.TrimSpace(out))
	}
	return nil, nil
}

func (a *Agent) btConnect(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	return a.btDevice(ctx, body, "connect")
}

func (a *Agent) btDisconnect(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	return a.btDevice(ctx, body, "disconnect")
}

// ---- do not disturb, night light ----

// defaultVelaShell: vela's control center (quickshell) owns DND and night light when it is installed.
func defaultVelaShell() string {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, _ := os.UserHomeDir()
		data = filepath.Join(home, ".local/share")
	}
	dir := filepath.Join(data, "vela", "shell")
	if fileExists(filepath.Join(dir, "shell.qml")) {
		return dir
	}
	return ""
}

// velaStateFile: quickshell keeps per-shell state files; the newest one with
// doNotDisturb in it belongs to the running vela shell.
func (a *Agent) velaStateFile() string {
	if a.velaState != "" {
		return a.velaState
	}
	home, _ := os.UserHomeDir()
	files, _ := filepath.Glob(filepath.Join(home, ".local/state/quickshell/by-shell/*/state.json"))
	newest, at := "", int64(0)
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil || st.ModTime().Unix() < at {
			continue
		}
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), "doNotDisturb") {
			newest, at = f, st.ModTime().Unix()
		}
	}
	return newest
}

func (a *Agent) velaToggles() (dnd, night bool, found bool) {
	if a.velaShell == "" {
		return false, false, false
	}
	b, err := os.ReadFile(a.velaStateFile())
	if err != nil {
		return false, false, false
	}
	var st struct {
		DoNotDisturb bool `json:"doNotDisturb"`
		NightLight   bool `json:"nightLight"`
	}
	if json.Unmarshal(b, &st) != nil {
		return false, false, false
	}
	return st.DoNotDisturb, st.NightLight, true
}

func (a *Agent) shellToggles(ctx context.Context) (dnd, night bool) {
	if d, n, ok := a.velaToggles(); ok {
		return d, n
	}
	if out, err := a.sys.Run(ctx, "makoctl", "mode"); err == nil {
		dnd = strings.Contains(out, "do-not-disturb")
	}
	_, err := a.sys.Run(ctx, "pidof", "hyprsunset")
	return dnd, err == nil
}

// setShellToggle switches vela's DND or night light; its IPC only knows "toggle",
// so it is called only when the state differs.
func (a *Agent) setVelaToggle(ctx context.Context, name string, want bool) (bool, error) {
	dnd, night, ok := a.velaToggles()
	if !ok {
		return false, nil
	}
	cur := dnd
	if name == "nightLight" {
		cur = night
	}
	if cur != want {
		_, err := a.sys.Run(ctx, "qs", "-p", a.velaShell, "ipc", "call", "toggle", name)
		return true, err
	}
	return true, nil
}

func (a *Agent) nightSet(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ On bool }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	if handled, err := a.setVelaToggle(ctx, "nightLight", p.On); handled {
		return nil, err
	}
	if p.On {
		return nil, a.sys.Start(ctx, "hyprsunset", "-t", "4500")
	}
	a.sys.Run(ctx, "pkill", "-x", "hyprsunset")
	return nil, nil
}

// ---- power profile, audio output ----

func (a *Agent) powerSet(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Profile string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	if !slices.Contains(powerProfiles, p.Profile) {
		return nil, fmt.Errorf("unknown power profile %q", p.Profile)
	}
	_, err := a.sys.Run(ctx, "powerprofilesctl", "set", p.Profile)
	return nil, err
}

func (a *Agent) audioOutputs(ctx context.Context) []AudioOutput {
	out, err := a.sys.Run(ctx, "pactl", "-f", "json", "list", "sinks")
	if err != nil {
		return nil
	}
	var sinks []struct{ Name, Description string }
	json.Unmarshal([]byte(out), &sinks)
	def, _ := a.sys.Run(ctx, "pactl", "get-default-sink")
	def = strings.TrimSpace(def)
	outs := make([]AudioOutput, 0, len(sinks))
	for _, s := range sinks {
		outs = append(outs, AudioOutput{Name: s.Name, Description: s.Description, Default: s.Name == def})
	}
	return outs
}

func (a *Agent) audioOutputList(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return ok(a.audioOutputs(ctx))
}

func (a *Agent) audioOutputSet(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Name string }
	if err := decode(body, &p); err != nil {
		return nil, err
	}
	for _, o := range a.audioOutputs(ctx) {
		if o.Name == p.Name {
			_, err := a.sys.Run(ctx, "pactl", "set-default-sink", o.Name)
			return nil, err
		}
	}
	return nil, fmt.Errorf("no audio output %q", p.Name)
}
