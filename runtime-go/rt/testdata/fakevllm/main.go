// Command fakevllm stands in for vLLM's OpenAI-compatible server in the tests
// of the Std.Ai.Serve GPU tier (ai_serve.go): the routes the tier uses, with
// vLLM's own shapes for runtime LoRA loading.
//
//	fakevllm -addr 127.0.0.1:0 -port-file <path> -base <served base name>
//	         [-delay-ms N] [-health-after-ms N]
//
//	GET  /health                    200 once -health-after-ms has passed
//	GET  /v1/models                 the base, then each loaded adapter (parent = base)
//	POST /v1/load_lora_adapter      {"lora_name","lora_path"}: the directory must hold
//	                                adapter_config.json and adapter_model.safetensors;
//	                                400 when the name is already loaded
//	POST /v1/unload_lora_adapter    {"lora_name"}: 404 when it is not loaded
//	POST /v1/chat/completions       the model must be the base or a loaded adapter
//	                                (404 otherwise); answers "echo:<model>", or SSE
//	                                chunks when "stream" is true
//	GET  /metrics                   vllm:num_requests_running, _waiting, kv cache use
//	GET  /_fake/stats               loaded adapters, load and unload counts, requests
//	                                per model, and requests in flight per model
//
// A request for an adapter that is unloaded while it runs fails the way vLLM
// fails it (500), so a tier that unloads before its requests drain is caught.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type state struct {
	mu        sync.Mutex
	base      string
	loaded    map[string]string // name -> path
	loads     int
	unloads   int
	requests  map[string]int
	inflight  map[string]int
	broken    int // requests whose adapter vanished while they ran
	delay     time.Duration
	healthyAt time.Time
	hideLoras bool // list only the base in /v1/models
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	portFile := flag.String("port-file", "", "write the listen address here")
	base := flag.String("base", "base", "the served name of the base model")
	delayMs := flag.Int("delay-ms", 0, "the time each completion takes")
	healthAfter := flag.Int("health-after-ms", 0, "answer /health 503 until this has passed")
	hideLoras := flag.Bool("hide-loras", false, "list only the base in /v1/models, as vLLM 0.31 does for adapters loaded at run time")
	flag.Parse()
	s := &state{base: *base, hideLoras: *hideLoras, loaded: map[string]string{}, requests: map[string]int{}, inflight: map[string]int{},
		delay: time.Duration(*delayMs) * time.Millisecond, healthyAt: time.Now().Add(time.Duration(*healthAfter) * time.Millisecond)}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakevllm:", err)
		os.Exit(1)
	}
	if *portFile != "" {
		os.WriteFile(*portFile+".tmp", []byte(ln.Addr().String()), 0o644)
		os.Rename(*portFile+".tmp", *portFile)
	}
	if err := http.Serve(ln, s); err != nil {
		fmt.Fprintln(os.Stderr, "fakevllm:", err)
		os.Exit(1)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *state) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		if time.Now().Before(s.healthyAt) {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	case "/v1/models":
		s.mu.Lock()
		data := []any{map[string]any{"id": s.base, "object": "model", "root": "/models/base", "parent": nil}}
		names := make([]string, 0, len(s.loaded))
		for n := range s.loaded {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if s.hideLoras {
				break
			}
			data = append(data, map[string]any{"id": n, "object": "model", "root": s.loaded[n], "parent": s.base})
		}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"object": "list", "data": data})
	case "/v1/load_lora_adapter":
		var req struct {
			Name string `json:"lora_name"`
			Path string `json:"lora_path"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		for _, f := range []string{"adapter_config.json", "adapter_model.safetensors"} {
			if _, err := os.Stat(filepath.Join(req.Path, f)); err != nil {
				writeJSON(w, 400, map[string]any{"object": "error", "message": "No adapter found for " + req.Path, "code": 400})
				return
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loaded[req.Name]; ok {
			writeJSON(w, 400, map[string]any{"object": "error", "message": "The lora adapter '" + req.Name + "' has already been loaded.", "code": 400})
			return
		}
		s.loaded[req.Name] = req.Path
		s.loads++
		io.WriteString(w, "Success: LoRA adapter '"+req.Name+"' added successfully.")
	case "/v1/unload_lora_adapter":
		var req struct {
			Name string `json:"lora_name"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.loaded[req.Name]; !ok {
			writeJSON(w, 404, map[string]any{"object": "error", "message": "The lora adapter '" + req.Name + "' cannot be found.", "code": 404})
			return
		}
		delete(s.loaded, req.Name)
		s.unloads++
		io.WriteString(w, "Success: LoRA adapter '"+req.Name+"' removed successfully.")
	case "/v1/chat/completions", "/v1/completions":
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		_, ok := s.loaded[req.Model]
		if !ok && req.Model != s.base {
			s.mu.Unlock()
			writeJSON(w, 404, map[string]any{"object": "error", "message": "The model `" + req.Model + "` does not exist.", "code": 404})
			return
		}
		s.requests[req.Model]++
		s.inflight[req.Model]++
		s.mu.Unlock()
		time.Sleep(s.delay)
		s.mu.Lock()
		s.inflight[req.Model]--
		_, still := s.loaded[req.Model]
		vanished := !still && req.Model != s.base
		if vanished {
			s.broken++
		}
		s.mu.Unlock()
		if vanished {
			writeJSON(w, 500, map[string]any{"object": "error", "message": "LoRA " + req.Model + " was unloaded during the request", "code": 500})
			return
		}
		answer := "echo:" + req.Model
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fl, _ := w.(http.Flusher)
			for _, part := range []string{"echo:", req.Model} {
				b, _ := json.Marshal(map[string]any{"model": req.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": part}}}})
				fmt.Fprintf(w, "data: %s\n\n", b)
				if fl != nil {
					fl.Flush()
				}
			}
			io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		writeJSON(w, 200, map[string]any{"model": req.Model, "choices": []any{map[string]any{"index": 0,
			"message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
	case "/metrics":
		s.mu.Lock()
		running := 0
		for _, n := range s.inflight {
			running += n
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE vllm:num_requests_running gauge\nvllm:num_requests_running{model_name=%q} %d\n", s.base, running)
		fmt.Fprintf(w, "# TYPE vllm:num_requests_waiting gauge\nvllm:num_requests_waiting{model_name=%q} 0\n", s.base)
		fmt.Fprintf(w, "# TYPE vllm:kv_cache_usage_perc gauge\nvllm:kv_cache_usage_perc{model_name=%q} 0.25\n", s.base)
	case "/_fake/stats":
		s.mu.Lock()
		names := []string{}
		for n := range s.loaded {
			names = append(names, n)
		}
		sort.Strings(names)
		out := map[string]any{"loaded": names, "loads": s.loads, "unloads": s.unloads, "requests": s.requests,
			"inflight": s.inflight, "broken": s.broken}
		s.mu.Unlock()
		writeJSON(w, 200, out)
	default:
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			writeJSON(w, 404, map[string]any{"object": "error", "message": "no route " + r.URL.Path})
			return
		}
		w.WriteHeader(404)
	}
}
