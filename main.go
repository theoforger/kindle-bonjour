// Command kindle-bonjour serves a Home Assistant and ECCC weather dashboard
// for a jailbroken Kindle. See PLAN.md.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	app := NewApp(store, NewWeatherService(), NewIconCache(filepath.Join(*dataDir, "icons")))
	if store.Get().SetupMode() {
		log.Printf("no Home Assistant config yet: setup mode")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.Run(ctx)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           (&Server{App: app, PollTimeout: 25 * time.Second}).Handler(),
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
