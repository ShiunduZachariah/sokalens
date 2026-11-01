package main

import (
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
	_ "modernc.org/sqlite"
)

func TestConfigureDatabasePoolCapsOpenConnections(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	cfg := config.Config{
		DBMaxOpenConns:    3,
		DBMaxIdleConns:    1,
		DBConnMaxLifetime: 20 * time.Minute,
		DBConnMaxIdleTime: 2 * time.Minute,
	}
	configureDatabasePool(db, cfg)

	if got := db.Stats().MaxOpenConnections; got != 3 {
		t.Fatalf("MaxOpenConnections = %d, want 3", got)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
	if got := db.Stats().Idle; got > 1 {
		t.Fatalf("idle connections = %d, want at most 1", got)
	}
}

func TestNewHTTPServerAppliesConfiguredTimeouts(t *testing.T) {
	cfg := config.Config{
		Port:                  9090,
		HTTPReadHeaderTimeout: 2 * time.Second,
		HTTPReadTimeout:       7 * time.Second,
		HTTPWriteTimeout:      8 * time.Second,
		HTTPIdleTimeout:       45 * time.Second,
		HTTPMaxHeaderBytes:    32 * 1024,
	}
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	server := newHTTPServer(cfg, handler)

	if server.Addr != ":9090" || server.Handler == nil {
		t.Fatalf("server address/handler = %q/%v", server.Addr, server.Handler)
	}
	if server.ReadHeaderTimeout != 2*time.Second || server.ReadTimeout != 7*time.Second || server.WriteTimeout != 8*time.Second || server.IdleTimeout != 45*time.Second {
		t.Fatalf("server timeouts = header %s read %s write %s idle %s", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 32*1024 {
		t.Fatalf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, 32*1024)
	}
}
