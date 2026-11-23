// pocketsudo-relay is the public rendezvous server. Configuration via env:
//
//	LISTEN            address, default :8080
//	DATA_DIR          where channels.json lives, default ./data
//	TRUST_PROXY       "1" behind a reverse proxy (X-Real-IP)
//	FCM_SERVICE_ACCOUNT_FILE  Firebase service account JSON (enables push)
//	FCM_API_KEY, FCM_APP_ID, FCM_SENDER_ID  public Firebase client options for the app
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Raindancer118/pocketsudo/internal/relay"
)

var version = "dev"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := relay.Options{DataDir: env("DATA_DIR", "data"), TrustProxy: os.Getenv("TRUST_PROXY") == "1", Logger: log}
	if f := os.Getenv("FCM_SERVICE_ACCOUNT_FILE"); f != "" {
		sa, err := os.ReadFile(f)
		if err != nil {
			log.Error("read service account", "err", err)
			os.Exit(1)
		}
		fcm, err := relay.NewFCM(ctx, sa)
		if err != nil {
			log.Error("fcm", "err", err)
			os.Exit(1)
		}
		opts.Pusher = fcm
		opts.FCMConfig = map[string]string{
			"project_id": fcm.ProjectID(),
			"api_key":    os.Getenv("FCM_API_KEY"),
			"app_id":     os.Getenv("FCM_APP_ID"),
			"sender_id":  os.Getenv("FCM_SENDER_ID"),
		}
		log.Info("push enabled", "project", fcm.ProjectID())
	} else {
		log.Warn("FCM_SERVICE_ACCOUNT_FILE not set: no push, phones only see requests when the app is open")
	}

	srv, err := relay.New(opts)
	if err != nil {
		log.Error("start", "err", err)
		os.Exit(1)
	}
	go srv.Janitor(ctx)

	hs := &http.Server{Addr: env("LISTEN", ":8080"), Handler: srv, ReadHeaderTimeout: 10 * time.Second,
		// long polls hold the response for up to 30 s
		WriteTimeout: 45 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(shut)
	}()
	log.Info("pocketsudo relay listening", "addr", hs.Addr, "version", version)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
