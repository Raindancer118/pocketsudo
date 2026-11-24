// pocketsudo lets you approve sudo on this machine with a fingerprint on your phone.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Raindancer118/pocketsudo/internal/host"
)

var version = "dev"

const usage = `pocketsudo – approve sudo with your phone's fingerprint

  pocketsudo pair [--relay URL] [--name NAME] [--print-uri]  pair a phone (QR code)
  pocketsudo status                            show pairings and PAM state
  pocketsudo test                              send a test request to your phone
  pocketsudo unpair NAME|CHANNEL               remove a phone
  pocketsudo install-pam [--file F]            enable for sudo (/etc/pam.d/sudo)
  pocketsudo uninstall-pam [--file F]          restore the original PAM file
  pocketsudo set timeout|local-fingerprint|remote-only VALUE
  pocketsudo auth                              PAM helper (called by pam_exec)
  pocketsudo version

Everything except version needs root (sudo).`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	if cmd == "auth" {
		os.Exit(runAuth())
	}
	var err error
	switch cmd {
	case "version", "--version":
		fmt.Println("pocketsudo", version)
		return
	case "help", "-h", "--help":
		fmt.Println(usage)
		return
	}
	if os.Geteuid() != 0 && os.Getenv("POCKETSUDO_CONFIG") == "" {
		fmt.Fprintln(os.Stderr, "pocketsudo: needs root – run it with sudo")
		os.Exit(1)
	}
	switch cmd {
	case "pair":
		err = runPair(args)
	case "status":
		err = runStatus()
	case "test":
		err = runTest()
	case "unpair":
		err = runUnpair(args)
	case "install-pam":
		err = runInstallPAM(args, true)
	case "uninstall-pam":
		err = runInstallPAM(args, false)
	case "set":
		err = runSet(args)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocketsudo:", err)
		os.Exit(1)
	}
}

// runAuth is the PAM entry point. Exit 0 = authenticated; anything else lets
// PAM continue with the next module (password).
func runAuth() int {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("pocketsudo: internal error, falling back to password")
			os.Exit(1)
		}
	}()
	if os.Getenv("PAM_TYPE") != "" && os.Getenv("PAM_TYPE") != "auth" {
		return 1
	}
	cfg, err := host.LoadConfig()
	if err != nil {
		fmt.Println("pocketsudo: config:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	s := host.SessionFromPAM()
	var local host.LocalVerifier
	if cfg.LocalFingerprint != "off" {
		local = host.FprintdVerifier()
	}
	res := host.Authenticate(ctx, cfg, s, host.AuthUI{Out: os.Stdout, Skip: host.TTYSkip()}, local)
	if res.Approved {
		return 0
	}
	return 1
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func runPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	relay := fs.String("relay", host.DefaultRelay, "relay URL")
	name := fs.String("name", hostname(), "name shown on the phone")
	printURI := fs.Bool("print-uri", false, "also print the pairing link (open it on the phone instead of scanning)")
	fs.Parse(args)
	cfg, err := host.LoadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	in := bufio.NewReader(os.Stdin)
	showQR := func(uri string) {
		host.RenderQR(os.Stdout, uri)
		if *printURI {
			fmt.Println(uri)
		}
	}
	p, err := host.Pair(ctx, cfg, strings.TrimRight(*relay, "/"), *name, host.PairUI{Out: os.Stdout, ShowQR: showQR,
		Confirm: func(device, code string) bool {
			fmt.Printf("\n%q wants to pair.\nVerification code: %s %s\nDoes the phone show the same code? [y/N] ",
				device, code[:3], code[3:])
			line, _ := in.ReadString('\n')
			return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "j")
		}})
	if err != nil {
		return err
	}
	fmt.Printf("Paired with %s.\n", p.DeviceName)
	if b, _ := os.ReadFile("/etc/pam.d/sudo"); !host.PAMInstalled(string(b)) {
		fmt.Println("Next: sudo pocketsudo test, then sudo pocketsudo install-pam")
	}
	return nil
}

func runStatus() error {
	cfg, err := host.LoadConfig()
	if err != nil {
		return err
	}
	fmt.Println("config:", host.ConfigPath())
	fmt.Printf("timeout: %ds, local fingerprint: %s, remote only: %v\n", cfg.TimeoutSec, cfg.LocalFingerprint, cfg.RemoteOnly)
	if len(cfg.Pairings) == 0 {
		fmt.Println("no phones paired – run: sudo pocketsudo pair")
	}
	for _, p := range cfg.Pairings {
		fmt.Printf("  %-20s channel %s…  relay %s  paired %s\n", p.DeviceName, p.Channel[:8], p.Relay, p.Paired.Format("2006-01-02"))
	}
	b, err := os.ReadFile("/etc/pam.d/sudo")
	switch {
	case err != nil:
		fmt.Println("pam: cannot read /etc/pam.d/sudo:", err)
	case host.PAMInstalled(string(b)):
		fmt.Println("pam: active for sudo")
		for _, l := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "auth") && strings.Contains(t, "pam_fprintd.so") && cfg.LocalFingerprint != "off" {
				fmt.Println("warning: pam_fprintd is active again (chwd?) – run: sudo pocketsudo install-pam")
			}
		}
	default:
		fmt.Println("pam: not installed – run: sudo pocketsudo install-pam")
	}
	return nil
}

func runTest() error {
	cfg, err := host.LoadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Pairings) == 0 {
		return errors.New("no phone paired")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	s := host.Session{Hostname: hostname(), Service: "pocketsudo-test", User: os.Getenv("SUDO_USER"),
		Command: "pocketsudo test", TTY: "test"}
	if s.User == "" {
		s.User = "root"
	}
	cfg.LocalFingerprint = "off"
	start := time.Now()
	res := host.Authenticate(ctx, cfg, s, host.AuthUI{Out: os.Stdout, Skip: host.TTYSkip()}, nil)
	if !res.Approved {
		return fmt.Errorf("not approved (%s)", res.Reason)
	}
	fmt.Printf("OK – round trip %.1fs\n", time.Since(start).Seconds())
	return nil
}

func runUnpair(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: pocketsudo unpair NAME|CHANNEL")
	}
	cfg, err := host.LoadConfig()
	if err != nil {
		return err
	}
	for _, p := range cfg.Pairings {
		if p.DeviceName == args[0] || strings.HasPrefix(p.Channel, args[0]) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := host.Unpair(ctx, cfg, p); err != nil {
				return err
			}
			fmt.Println("removed", p.DeviceName)
			return nil
		}
	}
	return fmt.Errorf("no pairing %q", args[0])
}

func runInstallPAM(args []string, install bool) error {
	fs := flag.NewFlagSet("pam", flag.ExitOnError)
	file := fs.String("file", "/etc/pam.d/sudo", "PAM service file")
	fs.Parse(args)
	b, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	if !install {
		out, changed := host.UninstallPAM(string(b))
		if !changed {
			fmt.Println("nothing to do")
			return nil
		}
		if err := host.WritePAMFile(*file, out); err != nil {
			return err
		}
		fmt.Println("removed from", *file)
		return nil
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return err
	}
	if fi, err := os.Stat(bin); err != nil || fi.Mode().Perm()&0o022 != 0 || !strings.HasPrefix(bin, "/usr/") {
		return fmt.Errorf("%s must live under /usr and not be group/world-writable (it runs as root)", bin)
	}
	cfg, err := host.LoadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Pairings) == 0 {
		return errors.New("pair a phone first (sudo pocketsudo pair)")
	}
	out, err := host.InstallPAM(string(b), bin, cfg.LocalFingerprint != "off")
	if err != nil {
		return err
	}
	if err := host.WritePAMFile(*file, out); err != nil {
		return err
	}
	fmt.Println("installed in", *file, "– your password still works as fallback.")
	fmt.Println("Tip: keep a root shell open (sudo -s) while you try it in a new terminal.")
	return nil
}

func runSet(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: pocketsudo set timeout SECONDS | local-fingerprint auto|off | remote-only true|false")
	}
	cfg, err := host.LoadConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "timeout":
		var n int
		if _, err := fmt.Sscan(args[1], &n); err != nil || n < 5 || n > 300 {
			return errors.New("timeout must be 5–300 seconds")
		}
		cfg.TimeoutSec = n
	case "local-fingerprint":
		if args[1] != "auto" && args[1] != "off" {
			return errors.New("local-fingerprint: auto or off")
		}
		cfg.LocalFingerprint = args[1]
		fmt.Println("re-run sudo pocketsudo install-pam so pam_fprintd is switched accordingly")
	case "remote-only":
		cfg.RemoteOnly = args[1] == "true" || args[1] == "1"
	default:
		return fmt.Errorf("unknown setting %q", args[0])
	}
	return cfg.Save()
}
