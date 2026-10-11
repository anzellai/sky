// Command fakellama is a stand-in for llama-server in the Std.Ai.Local tests.
//
// It accepts the launch arguments the supervisor passes, serves /health,
// /v1/chat/completions and /v1/embeddings behind the API key from
// --api-key-file, and misbehaves on request through a `fake.json` file in
// its own directory, so a test can drive a crash, a slow start or a slow
// answer without a real model:
//
//	{"healthDelayMs": 500, "crashOnRequest": 1, "exitOnStart": 3,
//	 "slowMs": 2000, "firstLaunchOnly": true}
//
// `fakellama --write-test-model <path>` writes a small valid GGUF file (the
// shape of Qwen2.5 0.5B, no tensors) and prints its SHA-256, for tests that
// cannot compute a digest themselves.
//
// Each launch appends one line to `launches.log` (its arguments) and writes
// the NAMES of its environment variables to `env.txt`, so a test can check
// that the key is not on the command line and the environment was cleared.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

type knobs struct {
	HealthDelayMs   int  `json:"healthDelayMs"`
	CrashOnRequest  int  `json:"crashOnRequest"`
	ExitOnStart     int  `json:"exitOnStart"`
	SlowMs          int  `json:"slowMs"`
	FirstLaunchOnly bool `json:"firstLaunchOnly"`
}

func main() {
	self, _ := os.Executable()
	dir := filepath.Dir(self)
	args := os.Args[1:]
	if len(args) == 2 && args[0] == "--write-test-model" {
		b := testModel()
		if err := os.WriteFile(args[1], b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		sum := sha256.Sum256(b)
		fmt.Println(hex.EncodeToString(sum[:]))
		return
	}
	for _, a := range args {
		if a == "--version" {
			fmt.Println("version: 0.0.0 (build 11200, fake)")
			return
		}
	}
	var k knobs
	if b, err := os.ReadFile(filepath.Join(dir, "fake.json")); err == nil {
		_ = json.Unmarshal(b, &k)
	}
	launches := 0
	if b, err := os.ReadFile(filepath.Join(dir, "launches.log")); err == nil {
		launches = strings.Count(string(b), "\n")
	}
	appendLine(filepath.Join(dir, "launches.log"), strings.Join(args, " "))
	var names []string
	for _, kv := range os.Environ() {
		n, _, _ := strings.Cut(kv, "=")
		names = append(names, n)
	}
	_ = os.WriteFile(filepath.Join(dir, "env.txt"), []byte(strings.Join(names, "\n")+"\n"), 0o644)
	if k.FirstLaunchOnly && launches > 0 {
		k = knobs{}
	}
	if k.ExitOnStart != 0 {
		fmt.Fprintln(os.Stderr, "fake: refusing to load the model")
		os.Exit(k.ExitOnStart)
	}

	host, port, keyFile := "127.0.0.1", "8080", ""
	embeddings := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host":
			i++
			host = args[i]
		case "--port":
			i++
			port = args[i]
		case "--api-key-file":
			i++
			keyFile = args[i]
		case "--embeddings":
			embeddings = true
		}
	}
	key := ""
	if keyFile != "" {
		b, _ := os.ReadFile(keyFile)
		key = strings.TrimSpace(string(b))
	}
	start := time.Now()
	var requests atomic.Int64
	authed := func(r *http.Request) bool {
		return key == "" || r.Header.Get("Authorization") == "Bearer "+key
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if time.Since(start) < time.Duration(k.HealthDelayMs)*time.Millisecond {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"message":"Loading model"}}`)
			return
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			w.WriteHeader(401)
			return
		}
		n := requests.Add(1)
		if k.CrashOnRequest > 0 && int(n) == k.CrashOnRequest {
			fmt.Fprintln(os.Stderr, "fake: GGML_ASSERT failed, aborting")
			os.Exit(134)
		}
		if k.SlowMs > 0 {
			time.Sleep(time.Duration(k.SlowMs) * time.Millisecond)
		}
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Temperature *float64 `json:"temperature"`
			Seed        *int     `json:"seed"`
			MaxTokens   *int     `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := ""
		if len(req.Messages) > 0 {
			last = req.Messages[len(req.Messages)-1].Content
		}
		extra := ""
		if req.Seed != nil {
			extra += fmt.Sprintf(" seed=%d", *req.Seed)
		}
		if req.MaxTokens != nil {
			extra += fmt.Sprintf(" max=%d", *req.MaxTokens)
		}
		if req.Temperature != nil {
			extra += fmt.Sprintf(" temp=%g", *req.Temperature)
		}
		content := fmt.Sprintf("fake[%s]: %s%s", req.Model, last, extra)
		b, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8},
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			w.WriteHeader(401)
			return
		}
		if !embeddings {
			w.WriteHeader(501)
			fmt.Fprint(w, `{"error":{"message":"This server does not support embeddings. Start it with --embeddings"}}`)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var data []any
		for i, s := range req.Input {
			v := make([]float64, 4)
			for j, c := range s {
				v[j%4] += float64(c) / 1000
			}
			data = append(data, map[string]any{"index": i, "embedding": v})
		}
		b, _ := json.Marshal(map[string]any{"data": data, "usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1}})
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	fmt.Fprintf(os.Stderr, "fake: listening on %s:%s\n", host, port)
	if err := http.ListenAndServe(host+":"+port, mux); err != nil {
		fmt.Fprintln(os.Stderr, "fake:", err)
		os.Exit(1)
	}
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// testModel is a GGUF v3 header with the metadata the fit plan reads.
func testModel() []byte {
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	kv := []struct {
		k string
		v any
	}{
		{"general.architecture", "qwen2"},
		{"qwen2.block_count", uint32(24)},
		{"qwen2.context_length", uint32(32768)},
		{"qwen2.embedding_length", uint32(896)},
		{"qwen2.attention.head_count", uint32(14)},
		{"qwen2.attention.head_count_kv", uint32(2)},
	}
	b.WriteString("GGUF")
	w(uint32(3))
	w(uint64(0))
	w(uint64(len(kv)))
	for _, e := range kv {
		str(e.k)
		switch v := e.v.(type) {
		case string:
			w(uint32(8))
			str(v)
		case uint32:
			w(uint32(4))
			w(v)
		}
	}
	return b.Bytes()
}
