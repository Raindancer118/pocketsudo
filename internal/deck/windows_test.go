package deck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const testClients = `[
 {"address":"0x55aa01","class":"brave-browser","title":"GitHub – Brave","pid":4242,"monitor":2,"workspace":{"id":3,"name":"3"}},
 {"address":"0x55aa02","class":"brave-browser","title":"Docs – Brave","pid":4242,"monitor":0,"workspace":{"id":1,"name":"1"}},
 {"address":"0x55aa03","class":"md.obsidian.Obsidian","title":"Notes","pid":777,"monitor":1,"workspace":{"name":"special:minimized"}},
 {"address":"0x55aa04","class":"Alacritty","title":"zsh","pid":900,"monitor":0,"workspace":{"id":1,"name":"1"}}
]`

const testMonitors = `[{"id":0,"name":"eDP-1","activeWorkspace":{"id":1}},{"id":1,"name":"DP-7","activeWorkspace":{"id":2}},{"id":2,"name":"DP-8","activeWorkspace":{"id":3}}]`

func windowRig(t *testing.T) *rig {
	r := newRig(t)
	data := t.TempDir()
	r.agent.dataDirs = []string{data}
	os.MkdirAll(filepath.Join(data, "applications"), 0o755)
	write := func(id, body string) {
		os.WriteFile(filepath.Join(data, "applications", id+".desktop"), []byte("[Desktop Entry]\nType=Application\n"+body), 0o644)
	}
	write("brave-browser", "Name=Brave\nExec=/usr/bin/brave %U\n")                           // id = class
	write("obsidian", "Name=Obsidian\nExec=obsidian\nStartupWMClass=md.obsidian.Obsidian\n") // via StartupWMClass
	write("Alacritty", "Name=Alacritty\nExec=alacritty\n")                                   // case-insensitive
	write("gimp", "Name=GIMP\nExec=gimp-3.0 %U\n")                                           // not running
	r.sys.out["hyprctl clients -j"] = testClients
	r.sys.out["hyprctl monitors -j"] = testMonitors
	return r
}

func TestAppsListShowsRunningWindows(t *testing.T) {
	r := windowRig(t)
	res := result(t, r.send("apps.list", nil, false))
	var apps []struct {
		ID      string
		Running []struct{ Address, Title, Workspace, Monitor string }
	}
	json.Unmarshal(res.Data, &apps)
	got := map[string]int{}
	for _, a := range apps {
		got[a.ID] = len(a.Running)
		if a.ID == "brave-browser" && (a.Running[0].Monitor != "DP-8" || a.Running[0].Workspace != "3") {
			t.Fatalf("brave window: %+v", a.Running[0])
		}
	}
	if got["brave-browser"] != 2 || got["obsidian"] != 1 || got["Alacritty"] != 1 || got["gimp"] != 0 {
		t.Fatalf("running: %v", got)
	}
}

func TestWindowActions(t *testing.T) {
	r := windowRig(t)
	move := map[string]string{"address": "0x55aa01", "monitor": "eDP-1"}
	if res := result(t, r.send("window.move", move, false)); res.OK {
		t.Fatal("moved a window without a remote session")
	}
	result(t, r.send("session.open", nil, true))

	// to the workspace that screen shows right now, without pulling the view along
	if res := result(t, r.send("window.move", move, false)); !res.OK ||
		!r.sys.ran(`hyprctl dispatch hl.dsp.window.move({ workspace = "1", window = "address:0x55aa01", follow = false })`) {
		t.Fatalf("move: %+v %v", res, r.sys.calls)
	}
	if res := result(t, r.send("window.close", map[string]string{"address": "0x55aa02"}, false)); !res.OK ||
		!r.sys.ran(`hyprctl dispatch hl.dsp.window.close({ window = "address:0x55aa02" })`) {
		t.Fatalf("close: %+v %v", res, r.sys.calls)
	}
	if res := result(t, r.send("window.kill", map[string]string{"address": "0x55aa03"}, false)); !res.OK || !r.sys.ran("kill -KILL 777") {
		t.Fatalf("kill: %+v %v", res, r.sys.calls)
	}

	// the address ends up in a Lua string: only real windows, nothing else
	for _, bad := range []string{`0x55aa01" }) hl.exec_cmd("rm -rf ~") --`, "0xdead", ""} {
		if res := result(t, r.send("window.close", map[string]string{"address": bad}, false)); res.OK {
			t.Fatalf("accepted address %q", bad)
		}
	}
	if res := result(t, r.send("window.move", map[string]string{"address": "0x55aa01", "monitor": "HDMI-9"}, false)); res.OK {
		t.Fatal("moved to an unknown screen")
	}
}
