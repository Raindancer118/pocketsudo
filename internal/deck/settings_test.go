package deck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func settingsRig(t *testing.T) *rig {
	r := newRig(t)
	o := r.sys.out
	o["nmcli -t -f DEVICE,TYPE,STATE,CONNECTION dev"] = "enp47s0u1u3u3:ethernet:connected:Wired connection 2\nwlan0:wifi:connected:MT_Home\nlo:loopback:connected (externally):lo\n"
	o["nmcli radio wifi"] = "enabled\n"
	o["nmcli -t -f IN-USE,SSID,SIGNAL,SECURITY dev wifi list"] = "*:MT_Home:82:WPA2\n :Cafe\\:Gast:40:\n :MT_Home:30:WPA2\n :Nachbar:55:WPA3\n"
	o["nmcli -t -f NAME,TYPE connection show"] = "MT_Home:802-11-wireless\nCafe\\:Gast:802-11-wireless\nWired connection 2:802-3-ethernet\n"
	o["ip route show default"] = "default via 192.168.0.1 dev enp47s0u1u3u3 proto dhcp metric 100\ndefault via 192.168.0.1 dev wlan0 proto dhcp metric 600\n"
	o["bluetoothctl show"] = "Controller 00:11:22:33:44:55 (public)\n\tName: Eclipse\n\tPowered: yes\n"
	o["bluetoothctl devices Paired"] = "Device AC:80:0A:12:68:93 WH-1000XM4\nDevice 58:18:62:42:85:0D WF-1000XM5\n"
	o["bluetoothctl devices Connected"] = "Device 58:18:62:42:85:0D WF-1000XM5\n"
	o["powerprofilesctl get"] = "balanced\n"
	o["pactl -f json list sinks"] = `[{"index":58,"name":"alsa_output.pci-0000_00_1f.3.analog-stereo","description":"Built-in Audio Analog Stereo"},{"index":71,"name":"bluez_output.58_18_62_42_85_0D.1","description":"WF-1000XM5"}]`
	o["pactl get-default-sink"] = "bluez_output.58_18_62_42_85_0D.1\n"
	// vela's control center keeps do-not-disturb and night light here
	shell := t.TempDir()
	state := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(state, []byte(`{"doNotDisturb": true, "nightLight": false}`), 0o644)
	r.agent.velaShell, r.agent.velaState = shell, state
	return r
}

func TestSettingsOverview(t *testing.T) {
	r := settingsRig(t)
	res := result(t, r.send("settings", nil, false))
	var s Settings
	json.Unmarshal(res.Data, &s)
	if !res.OK || !s.WiFi.On || s.WiFi.SSID != "MT_Home" || !s.Bluetooth.On || s.Bluetooth.Device != "WF-1000XM5" ||
		!s.DND || s.NightLight || s.Power != "balanced" || s.Output != "WF-1000XM5" {
		t.Fatalf("settings %+v %+v", res, s)
	}
}

func TestWifiList(t *testing.T) {
	r := settingsRig(t)
	res := result(t, r.send("wifi.list", nil, false))
	var nets []WifiNet
	json.Unmarshal(res.Data, &nets)
	// strongest entry per name, connected first, then by signal; escaped colons kept in names
	if !res.OK || len(nets) != 3 || nets[0].SSID != "MT_Home" || !nets[0].Active || nets[0].Signal != 82 ||
		nets[1].SSID != "Nachbar" || !nets[1].Secure || nets[1].Known ||
		nets[2].SSID != "Cafe:Gast" || nets[2].Secure || !nets[2].Known {
		t.Fatalf("networks %+v", nets)
	}
}

func TestWifiOffKeepsTheNotebookReachable(t *testing.T) {
	r := settingsRig(t)
	// cable is there too: switching WLAN off is safe
	if res := result(t, r.send("wifi.set", map[string]bool{"on": false}, false)); !res.OK || !r.sys.ran("nmcli radio wifi off") {
		t.Fatalf("wifi off with cable: %+v", res)
	}
	// WLAN is the only way out: refuse unless the phone confirms it knows
	r = settingsRig(t)
	r.sys.out["ip route show default"] = "default via 192.168.0.1 dev wlan0 proto dhcp metric 600\n"
	res := result(t, r.send("wifi.set", map[string]bool{"on": false}, false))
	if res.OK || r.sys.ran("nmcli radio wifi off") || !strings.Contains(res.Error, "unreachable") {
		t.Fatalf("cut its own connection: %+v", res)
	}
	if res := result(t, r.send("wifi.set", map[string]bool{"on": false, "confirm": true}, false)); !res.OK || !r.sys.ran("nmcli radio wifi off") {
		t.Fatalf("confirmed wifi off: %+v", res)
	}
}

func TestWifiConnect(t *testing.T) {
	r := settingsRig(t)
	if res := result(t, r.send("wifi.connect", map[string]string{"ssid": "Nachbar", "password": "geheim 123"}, false)); !res.OK ||
		!r.sys.ran("nmcli dev wifi connect Nachbar password geheim 123") {
		t.Fatalf("connect new: %+v %v", res, r.sys.calls)
	}
	// known network: its saved profile, no password needed
	if res := result(t, r.send("wifi.connect", map[string]string{"ssid": "Cafe:Gast"}, false)); !res.OK || !r.sys.ran("nmcli connection up id Cafe:Gast") {
		t.Fatalf("connect known: %+v %v", res, r.sys.calls)
	}
	if res := result(t, r.send("wifi.connect", map[string]string{"ssid": "-h"}, false)); res.OK {
		t.Fatal("ssid that looks like an option accepted")
	}
}

func TestBluetooth(t *testing.T) {
	r := settingsRig(t)
	res := result(t, r.send("bt.list", nil, false))
	var devs []BTDevice
	json.Unmarshal(res.Data, &devs)
	if !res.OK || len(devs) != 2 || devs[0].Name != "WF-1000XM5" || !devs[0].Connected || devs[1].Connected {
		t.Fatalf("devices %+v", devs)
	}
	if res := result(t, r.send("bt.connect", map[string]string{"mac": "AC:80:0A:12:68:93"}, false)); !res.OK || !r.sys.ran("bluetoothctl connect AC:80:0A:12:68:93") {
		t.Fatalf("connect: %+v", res)
	}
	if res := result(t, r.send("bt.disconnect", map[string]string{"mac": "58:18:62:42:85:0D"}, false)); !res.OK || !r.sys.ran("bluetoothctl disconnect 58:18:62:42:85:0D") {
		t.Fatalf("disconnect: %+v", res)
	}
	if res := result(t, r.send("bt.connect", map[string]string{"mac": "AC:80; reboot"}, false)); res.OK {
		t.Fatal("bad mac accepted")
	}
	if res := result(t, r.send("bt.set", map[string]bool{"on": false}, false)); !res.OK || !r.sys.ran("bluetoothctl power off") {
		t.Fatalf("power off: %+v", res)
	}
}

func TestToggles(t *testing.T) {
	r := settingsRig(t)
	// vela: only toggle when the state differs (its IPC only knows "toggle")
	if res := result(t, r.send("dnd", map[string]bool{"on": true}, false)); !res.OK || r.sys.ran("qs") {
		t.Fatalf("dnd already on toggled again: %+v %v", res, r.sys.calls)
	}
	if res := result(t, r.send("night.set", map[string]bool{"on": true}, false)); !res.OK || !r.sys.ran("qs -p "+r.agent.velaShell+" ipc call toggle nightLight") {
		t.Fatalf("night light: %+v %v", res, r.sys.calls)
	}
	if res := result(t, r.send("power.set", map[string]string{"profile": "performance"}, false)); !res.OK || !r.sys.ran("powerprofilesctl set performance") {
		t.Fatalf("power: %+v", res)
	}
	if res := result(t, r.send("power.set", map[string]string{"profile": "turbo; rm"}, false)); res.OK {
		t.Fatal("unknown power profile accepted")
	}
	if res := result(t, r.send("audio.output", map[string]string{"name": "alsa_output.pci-0000_00_1f.3.analog-stereo"}, false)); !res.OK ||
		!r.sys.ran("pactl set-default-sink alsa_output.pci-0000_00_1f.3.analog-stereo") {
		t.Fatalf("output: %+v", res)
	}
	if res := result(t, r.send("audio.output", map[string]string{"name": "nope"}, false)); res.OK {
		t.Fatal("unknown output accepted")
	}
}
