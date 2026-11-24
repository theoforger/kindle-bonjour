// Command kindle-bonjour serves a Home Assistant and ECCC weather dashboard
// for a jailbroken Kindle. See PLAN.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "time/tzdata" // TZ works in a scratch image
)

func main() {
	dataDir := flag.String("data", envOr("DATA_DIR", "/data"), "directory for config.json and the icon cache")
	addr := flag.String("addr", ":"+envOr("PORT", "8080"), "listen address")
	flag.Parse()

	store, err := LoadConfig(filepath.Join(*dataDir, "config.json"))
	if err != nil {
		log.Fatal(err)
	}
	if err := seedConfig(store, os.Getenv); err != nil {
		log.Fatal(err)
	}
	password, err := adminPassword(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	app := NewApp(store, NewWeatherService(), NewIconCache(filepath.Join(*dataDir, "icons")))
	if store.Get().SetupMode() {
		log.Printf("no Home Assistant config yet: setup mode, open /settings")
	}
	if password == "" {
		log.Printf("settings page has no password (set ADMIN_PASSWORD_FILE or ADMIN_PASSWORD)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.Run(ctx)

	handler := (&Server{App: app, PollTimeout: 25 * time.Second,
		Sites: NewSiteList(*dataDir), AdminPassword: password}).Handler()
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      40 * time.Second, // longer than the long-poll
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Printf("listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// seedConfig writes a first config from HA_URL and HA_TOKEN when there is
// no config file yet. Once a config exists the environment is ignored, so
// the settings page stays in charge.
func seedConfig(store *ConfigStore, env func(string) string) error {
	u, tok := strings.TrimSpace(env("HA_URL")), strings.TrimSpace(env("HA_TOKEN"))
	if store.Exists() || (u == "" && tok == "") {
		return nil
	}
	if err := store.Update(func(c *Config) error { c.HA = HAConfig{URL: u, Token: tok}; return nil }); err != nil {
		return fmt.Errorf("seed config from HA_URL/HA_TOKEN: %w", err)
	}
	log.Printf("wrote a first config from HA_URL/HA_TOKEN")
	return nil
}

// adminPassword reads ADMIN_PASSWORD_FILE (a Docker secret), else
// ADMIN_PASSWORD. Empty means no password.
func adminPassword(env func(string) string) (string, error) {
	if f := env("ADMIN_PASSWORD_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("ADMIN_PASSWORD_FILE: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return env("ADMIN_PASSWORD"), nil
}
