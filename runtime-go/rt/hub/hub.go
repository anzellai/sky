// Package hub implements `sky console-serve` — the Sky Console Hub
// daemon. v0.16.4 Chunks 2+3 land the OTLP/HTTP receiver and the
// SQLite hot store. The UI ships in Chunks 4-6.
//
// Entry point: Run(cfg). The Haskell CLI (`runConsoleServe` in
// app/Main.hs) materialises the embedded runtime-go tree to a cache
// dir, `go build`s runtime-go/cmd/sky-hub, then execs the resulting
// binary with the resolved flag values forwarded as `--port`,
// `--data-dir`, `--auth`, `--tls-cert`, `--tls-key`.
//
// Design notes:
//
//   - HTTP server lives on a single net.Listener; close()'d on
//     shutdown.
//   - Receiver pushes parsed records onto a bounded channel
//     (capacity HubBufferCap, default 16384) drained by the Store
//     batcher.
//   - Auth + payload caps gate happen BEFORE the channel send so a
//     pathological caller can't fill the channel with junk.
//   - Every HTTP handler is wrapped in a defer/recover that emits a
//     500 + structured log line; no process panic at the request
//     boundary (CLAUDE.md §6 — synchronous-panic gate).
//   - SIGINT/SIGTERM trigger a 5 s graceful shutdown: stop accepting
//     new requests, drain the channel into the store, close the DB.
package hub

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// HubConfig carries the resolved CLI + env config. Constructed by Run
// from os.Args + os.Environ.
type HubConfig struct {
	// Port is the HTTP listen port. 0 = ephemeral (test fixtures).
	Port int
	// DataDir is the directory housing console-hot.db.
	DataDir string
	// AuthMode is "token" | "app" | "off". v0.16.4 implements
	// "token" + "off"; "app" mode lands in Chunk 7.
	AuthMode string
	// TLSCert + TLSKey are file paths. Both empty → HTTP-only.
	TLSCert string
	TLSKey  string
	// Token is the expected bearer (SKY_CONSOLE_HUB_TOKEN). Empty +
	// AuthMode == "token" is rejected at Run().
	Token string
	// MaxPayloadBytes caps each POST body. Default 4 MiB; env-
	// overridable via SKY_CONSOLE_HUB_MAX_PAYLOAD.
	MaxPayloadBytes int64
	// RetentionHours is the SQLite prune window. Default 24 h. Set
	// to 0 to prune everything older than `now` (testing).
	RetentionHours int
	// PruneInterval is how often the pruner runs. Default 1 h.
	PruneInterval time.Duration
}

// DefaultMaxPayloadBytes — 4 MiB. Matches HUB.md §"Receiver
// implementation" + the prompt's spec.
const DefaultMaxPayloadBytes int64 = 4 * 1024 * 1024

// DefaultRetentionHours — 24 h hot-store window. Matches HUB.md.
const DefaultRetentionHours = 24

// DefaultPruneInterval — 1 h pruner cadence.
var DefaultPruneInterval = 1 * time.Hour

// HubBufferCap bounds the in-memory channel between receiver and
// writer. Sized so a brief writer stall (e.g. SQLite checkpoint)
// doesn't drop telemetry on a busy hub. Sustained overflow drops
// at the receiver, never blocks the HTTP goroutine.
const HubBufferCap = 16384

// ConfigFromEnv reads env vars into a HubConfig stamped with CLI
// defaults. The CLI driver in cmd/sky-hub overlays flag values
// AFTER this call so flags win over env (matches the standard
// precedence in CLAUDE.md §"Environment variables").
func ConfigFromEnv() HubConfig {
	cfg := HubConfig{
		Port:            4000,
		DataDir:         "./skyhub-data",
		AuthMode:        "token",
		MaxPayloadBytes: DefaultMaxPayloadBytes,
		RetentionHours:  DefaultRetentionHours,
		PruneInterval:   DefaultPruneInterval,
		Token:           os.Getenv("SKY_CONSOLE_HUB_TOKEN"),
	}
	if v := os.Getenv("SKY_CONSOLE_HUB_MAX_PAYLOAD"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			cfg.MaxPayloadBytes = n
		}
	}
	if v := os.Getenv("SKY_CONSOLE_HUB_RETENTION_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.RetentionHours = n
		}
	}
	if v := os.Getenv("SKY_CONSOLE_HUB_PRUNE_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.PruneInterval = time.Duration(n) * time.Second
		}
	}
	return cfg
}

// Validate checks fundamental config invariants. Called by Run
// before binding the port. Token-mode without a token is rejected
// hard so an operator doesn't accidentally expose a public ingest.
func (c *HubConfig) Validate() error {
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("hub: invalid port %d", c.Port)
	}
	if c.DataDir == "" {
		return fmt.Errorf("hub: data-dir is required")
	}
	switch c.AuthMode {
	case "token":
		if c.Token == "" {
			return fmt.Errorf("hub: auth=token requires SKY_CONSOLE_HUB_TOKEN")
		}
	case "off":
		// permitted — operator deliberately disables auth (local
		// dev / behind a trusted reverse proxy).
	case "app":
		return fmt.Errorf("hub: auth=app not implemented until Chunk 7")
	default:
		return fmt.Errorf("hub: unknown auth mode %q (want token|off)", c.AuthMode)
	}
	if c.TLSCert != "" && c.TLSKey == "" {
		return fmt.Errorf("hub: --tls-cert set but --tls-key missing")
	}
	if c.TLSKey != "" && c.TLSCert == "" {
		return fmt.Errorf("hub: --tls-key set but --tls-cert missing")
	}
	if c.MaxPayloadBytes <= 0 {
		c.MaxPayloadBytes = DefaultMaxPayloadBytes
	}
	if c.PruneInterval <= 0 {
		c.PruneInterval = DefaultPruneInterval
	}
	return nil
}

// Run starts the hub. Blocks until SIGINT/SIGTERM or a fatal listen
// error. Returns nil on graceful shutdown.
//
// Lifecycle:
//
//  1. Validate cfg.
//  2. Open store (Chunk 3) — fails fast if SQLite open errors.
//  3. Construct receiver mux.
//  4. Bind listener.
//  5. Start http.Server in a goroutine.
//  6. Block on signal channel.
//  7. Graceful shutdown: close listener (Server.Shutdown), close
//     store (which drains its batcher).
func Run(cfg HubConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	store, err := newStore(cfg.DataDir, storeOptions{
		retentionHours: cfg.RetentionHours,
		pruneInterval:  cfg.PruneInterval,
	})
	if err != nil {
		return fmt.Errorf("hub: open store: %w", err)
	}

	recv := newReceiver(cfg, store)
	mux := http.NewServeMux()
	recv.attach(mux)

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("hub: listen :%d: %w", cfg.Port, err)
	}
	resolvedPort := listener.Addr().(*net.TCPAddr).Port

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Banner — visible only when run interactively, suppressed by
	// the test harness via SKY_CONSOLE_HUB_QUIET.
	if os.Getenv("SKY_CONSOLE_HUB_QUIET") == "" {
		fmt.Fprintf(os.Stderr,
			"[sky.hub] listening on :%d, data-dir=%s, auth=%s\n",
			resolvedPort, cfg.DataDir, cfg.AuthMode)
	}

	serveErr := make(chan error, 1)
	go func() {
		var e error
		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			e = srv.ServeTLS(listener, cfg.TLSCert, cfg.TLSKey)
		} else {
			e = srv.Serve(listener)
		}
		if e != nil && !errors.Is(e, http.ErrServerClosed) {
			serveErr <- e
		}
		close(serveErr)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
		// graceful shutdown
	case err := <-serveErr:
		if err != nil {
			_ = store.Close()
			return fmt.Errorf("hub: serve: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[sky.hub] shutdown: %v", err)
	}
	if err := store.Close(); err != nil {
		log.Printf("[sky.hub] store close: %v", err)
	}
	return nil
}
