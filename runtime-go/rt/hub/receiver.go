package hub

// OTLP/HTTP receiver. Exposes:
//
//	POST /v1/traces
//	POST /v1/metrics
//	POST /v1/logs
//	GET  /_hub/healthz   (200 OK + "ok")
//
// All three OTLP endpoints share the same pipeline:
//
//	1. authMiddleware  (token / off / app stub)
//	2. payload cap     (413 on overflow)
//	3. content-type dispatch (proto or json)
//	4. decode → []pendingItem
//	5. store.Insert (non-blocking — drops at the channel boundary
//	   when the writer is saturated; returns 200 because OTLP has
//	   no per-record ack)
//
// Each handler is wrapped in a defer/recover that turns a panic
// into a 500 + log line. Closes the synchronous-panic gate at the
// request boundary (CLAUDE.md §6).

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime/debug"
)

type receiver struct {
	cfg   HubConfig
	store *Store
}

func newReceiver(cfg HubConfig, store *Store) *receiver {
	return &receiver{cfg: cfg, store: store}
}

// attach wires every receiver endpoint onto mux. Wraps each handler
// in authMiddleware + recoverMiddleware so neither auth nor a bad
// payload can crash the daemon.
func (r *receiver) attach(mux *http.ServeMux) {
	tracesH := r.recoverMiddleware(http.HandlerFunc(r.handleTraces))
	metricsH := r.recoverMiddleware(http.HandlerFunc(r.handleMetrics))
	logsH := r.recoverMiddleware(http.HandlerFunc(r.handleLogs))

	mux.Handle("/v1/traces", authMiddleware(r.cfg, tracesH))
	mux.Handle("/v1/metrics", authMiddleware(r.cfg, metricsH))
	mux.Handle("/v1/logs", authMiddleware(r.cfg, logsH))

	// Health + readiness probes are intentionally NOT auth-gated:
	// load balancers / k8s probes / SkyDeploy uptime checks need
	// to reach them without a token. They reveal nothing beyond
	// "server is up".
	mux.HandleFunc("/_hub/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/_hub/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !r.store.Ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ready"))
	})
}

// recoverMiddleware turns a panic into a 500 + structured log entry.
// The hub MUST never exit on a malformed payload (CLAUDE.md §6 —
// synchronous-panic gate).
func (r *receiver) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[sky.hub] panic in %s %s: %v\n%s",
					req.Method, req.URL.Path, rec, debug.Stack())
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, req)
	})
}

// handleTraces / handleMetrics / handleLogs share the read-body +
// content-type dispatch loop. We split per-signal to keep the
// decoder selection trivially typed.

func (r *receiver) handleTraces(w http.ResponseWriter, req *http.Request) {
	r.handlePost(w, req, "traces")
}

func (r *receiver) handleMetrics(w http.ResponseWriter, req *http.Request) {
	r.handlePost(w, req, "metrics")
}

func (r *receiver) handleLogs(w http.ResponseWriter, req *http.Request) {
	r.handlePost(w, req, "logs")
}

func (r *receiver) handlePost(w http.ResponseWriter, req *http.Request, kind string) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Payload cap. http.MaxBytesReader writes a sentinel that we
	// detect on the read error path to surface a 413 instead of a
	// generic 400.
	req.Body = http.MaxBytesReader(w, req.Body, r.cfg.MaxPayloadBytes)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
		return
	}

	ct := req.Header.Get("Content-Type")
	var items []pendingItem
	switch {
	case isProtobuf(ct):
		items, err = decodeByKind(kind, body, true)
	case isJSON(ct):
		items, err = decodeByKind(kind, body, false)
	case ct == "":
		// Be lenient: a missing content-type falls through to a
		// best-effort decode (protobuf first, json fallback). The
		// OTel spec REQUIRES Content-Type but real-world clients
		// sometimes omit it on intermediate proxies.
		items, err = decodeByKind(kind, body, true)
		if err != nil {
			items, err = decodeByKind(kind, body, false)
		}
	default:
		http.Error(w,
			fmt.Sprintf("unsupported Content-Type %q (want application/x-protobuf or application/json)", ct),
			http.StatusUnsupportedMediaType)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("decode %s: %v", kind, err), http.StatusBadRequest)
		return
	}

	r.store.Insert(items)

	// 200 + empty body. OTLP collectors return ExportXxxServiceResponse
	// (typically empty) — we don't surface partial-success in v0.16.4.
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

func decodeByKind(kind string, body []byte, proto bool) ([]pendingItem, error) {
	switch kind {
	case "traces":
		if proto {
			return decodeTracesProto(body)
		}
		return decodeTracesJSON(body)
	case "metrics":
		if proto {
			return decodeMetricsProto(body)
		}
		return decodeMetricsJSON(body)
	case "logs":
		if proto {
			return decodeLogsProto(body)
		}
		return decodeLogsJSON(body)
	}
	return nil, fmt.Errorf("unknown kind %q", kind)
}
