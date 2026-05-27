// mock/main.go — tiny streaming HTTP server for examples/28-streaming-chat.
//
// Simulates an LLM-style streaming completion: on every POST to /stream
// the server writes one chunk every 100 ms for 20 chunks, then closes.
// The chunks together form a deterministic reply so the Sky.Live app
// can assert convergence after Done lands.
//
// Run alongside the Sky.Live app:
//
//	# Terminal 1
//	go run examples/28-streaming-chat/mock/main.go
//	# Terminal 2
//	cd examples/28-streaming-chat && sky run src/Main.sky
//
// The Playwright probe (scripts/verify-streaming-chat.sh) boots both
// automatically.

package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", ":8765", "listen address")
	chunkMs := flag.Int("chunkMs", 100, "delay between chunks (ms)")
	chunks := flag.Int("chunks", 20, "total number of chunks per stream")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		// Read the prompt — we echo it back framed inside the reply so
		// Playwright can assert a non-empty round-trip.
		bodyBytes, _ := io.ReadAll(r.Body)
		prompt := strings.TrimSpace(string(bodyBytes))
		if prompt == "" {
			prompt = "<empty>"
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no") // proxy bypass
		w.WriteHeader(200)

		fmt.Fprintf(w, "echo: %s\n", prompt)
		flusher.Flush()

		for i := 0; i < *chunks; i++ {
			fmt.Fprintf(w, "[token %02d] ", i+1)
			flusher.Flush()
			time.Sleep(time.Duration(*chunkMs) * time.Millisecond)
		}
		fmt.Fprintln(w, "<end>")
		flusher.Flush()
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // no write timeout — streams may run for minutes
	}
	log.Printf("mock streaming server listening on %s (chunks=%d, delay=%dms)\n", *addr, *chunks, *chunkMs)
	if err := srv.ListenAndServe(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
