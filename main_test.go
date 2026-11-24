package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckHealth(t *testing.T) {
	status := http.StatusOK
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer ts.Close()
	_, port, _ := net.SplitHostPort(ts.Listener.Addr().String())

	// A wildcard listen address is checked on loopback.
	for _, addr := range []string{":" + port, "0.0.0.0:" + port, "127.0.0.1:" + port} {
		if err := checkHealth(addr, time.Second); err != nil {
			t.Errorf("checkHealth(%q) = %v, want healthy", addr, err)
		}
	}
	status = http.StatusServiceUnavailable
	if err := checkHealth(":"+port, time.Second); err == nil {
		t.Error("503 counted as healthy")
	}
	ts.Close()
	if err := checkHealth(":"+port, time.Second); err == nil {
		t.Error("closed server counted as healthy")
	}
	if err := checkHealth("nonsense", time.Second); err == nil {
		t.Error("bad address accepted")
	}
}

func TestCheckDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := checkDataDir(dir); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("write test left %d files behind", len(ents))
	}
	if os.Getuid() == 0 {
		t.Skip("root can write a read-only directory")
	}
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	if err := checkDataDir(dir); err == nil {
		t.Error("read-only data directory accepted")
	}
}
