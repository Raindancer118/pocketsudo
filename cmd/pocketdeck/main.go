// pocketdeck is the notebook agent of PocketDeck: control this machine from
// the paired phone (unlock, media, volume, shortcuts …). Runs as the user.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Raindancer118/pocketsudo/internal/deck"
	"github.com/Raindancer118/pocketsudo/internal/host"
)

var version = "dev"

const usage = `PocketHub (pocketdeck) – control this machine from your phone

  pocketdeck pair [--relay URL] [--name NAME] [--print-uri]
  pocketdeck run                 agent loop (what the user service runs)
  pocketdeck install-service     install + start the systemd user service
  pocketdeck send [--zip] FILE…  send files or folders to the phone (Downloads/PocketHub)
  pocketdeck install-menu        add "Send to phone" to the file manager's right-click menu
  pocketdeck status
  pocketdeck selftest            read volume, brightness, media, lock state locally
  pocketdeck version

Runs as your user, not as root.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "pocketdeck: run it as your user, not root")
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "pair":
		err = pair(os.Args[2:])
	case "run":
		err = run()
	case "install-service":
		err = installService()
	case "send":
		err = send(os.Args[2:])
	case "install-menu":
		err = installMenu()
	case "status":
		err = status()
	case "selftest":
		err = selftest()
	case "version", "--version":
		fmt.Println("pocketdeck", version)
	default:
		fmt.Println(usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocketdeck:", err)
		os.Exit(1)
	}
}

func send(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	zipAll := fs.Bool("zip", false, "send everything as one zip")
	notify := fs.Bool("notify", false, "report on the desktop (for the file manager menu)")
	fs.Parse(args)
	err := sendFiles(fs.Args(), *zipAll)
	if *notify {
		if err != nil {
			exec.Command("notify-send", "-a", "PocketHub", "-i", "dialog-error", "Not sent to phone", err.Error()).Run()
		} else {
			exec.Command("notify-send", "-a", "PocketHub", "-i", "smartphone", "Sent to phone", strings.Join(fs.Args(), "\n")).Run()
		}
	}
	return err
}

func sendFiles(files []string, zipAll bool) error {
	if len(files) == 0 {
		return errors.New("usage: pocketdeck send [--zip] FILE…")
	}
	cfg, err := deck.Load(deck.DefaultPath())
	if err != nil {
		return err
	}
	a, err := deck.NewAgent(cfg, deck.NewDesktop(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sent, err := a.SendFiles(ctx, files, zipAll)
	for _, n := range sent {
		fmt.Println("sent:", n)
	}
	if err == nil {
		fmt.Println("The phone downloads it now; it waits on the relay for 15 minutes.")
	}
	return err
}

// installMenu adds "Send to phone" to Dolphin's right-click menu (KDE service menu).
func installMenu() error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Join(xdgData(), "kio", "servicemenus")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	menu := "[Desktop Entry]\nType=Service\nMimeType=all/all;\nX-KDE-Protocols=file\nX-KDE-ServiceTypes=KonqPopupMenu/Plugin\nActions=pocketdeckSend\n\n" +
		"[Desktop Action pocketdeckSend]\nName=Send to phone\nName[de]=An Handy senden\nIcon=smartphone\nExec=\"" + bin + "\" send --notify %F\n"
	path := filepath.Join(dir, "pocketdeck-send.desktop")
	// Dolphin ignores service menus that are not executable
	if err := os.WriteFile(path, []byte(menu), 0o755); err != nil {
		return err
	}
	fmt.Println("Right-click menu installed:", path)
	return nil
}

func xdgData() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

func pair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	relay := fs.String("relay", host.DefaultRelay, "relay URL")
	hn, _ := os.Hostname()
	name := fs.String("name", hn, "name shown on the phone")
	printURI := fs.Bool("print-uri", false, "also print the pairing link")
	fs.Parse(args)
	cfg, err := deck.Load(deck.DefaultPath())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	in := bufio.NewReader(os.Stdin)
	err = deck.Pair(ctx, cfg, strings.TrimRight(*relay, "/"), *name, host.PairUI{Out: os.Stdout,
		ShowQR: func(uri string) {
			host.RenderQR(os.Stdout, uri)
			if *printURI {
				fmt.Println(uri)
			}
		},
		Confirm: func(device, code string) bool {
			fmt.Printf("\n%q wants to pair.\nVerification code: %s %s\nDoes the phone show the same code? [y/N] ", device, code[:3], code[3:])
			line, _ := in.ReadString('\n')
			l := strings.TrimSpace(line)
			return strings.EqualFold(l, "y") || strings.EqualFold(l, "j")
		}})
	if err != nil {
		return err
	}
	fmt.Println("Paired with", cfg.DeviceName+". Next: pocketdeck install-service")
	return nil
}

func run() error {
	cfg, err := deck.Load(deck.DefaultPath())
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	a, err := deck.NewAgent(cfg, deck.NewDesktop(), log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func status() error {
	cfg, err := deck.Load(deck.DefaultPath())
	if err != nil {
		return err
	}
	fmt.Println("config:", deck.DefaultPath())
	if !cfg.IsPaired() {
		fmt.Println("not paired – run: pocketdeck pair")
		return nil
	}
	fmt.Printf("paired with %s via %s (channel %s…, since %s)\n", cfg.DeviceName, cfg.Relay, cfg.Channel[:8], cfg.Paired.Format("2006-01-02"))
	out, _ := exec.Command("systemctl", "--user", "is-active", "pocketdeck").Output()
	fmt.Println("service:", strings.TrimSpace(string(out)))
	return nil
}

func selftest() error {
	return deck.SelfTest(context.Background(), deck.NewDesktop(), func(v any) {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(b))
	})
}

const unit = `[Unit]
Description=PocketHub agent (control this machine from the paired phone)
After=graphical-session.target network-online.target
PartOf=graphical-session.target

[Service]
ExecStart=%s run
Restart=on-failure
RestartSec=5

[Install]
WantedBy=graphical-session.target
`

func installService() error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return err
	}
	dir, _ := os.UserConfigDir()
	path := filepath.Join(dir, "systemd", "user", "pocketdeck.service")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(unit, bin)), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", "pocketdeck"}, {"restart", "pocketdeck"}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
		}
	}
	fmt.Println("installed", path, "– running")
	if _, err := exec.LookPath("dolphin"); err == nil {
		return installMenu()
	}
	return nil
}
