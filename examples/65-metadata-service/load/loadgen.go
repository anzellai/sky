// loadgen — a small closed-loop HTTP load harness for the metadata-service.
//
// It sweeps a set of concurrency levels; at each level, N goroutines each loop
// "send request → measure latency → repeat" until the per-level duration
// elapses (closed-loop / constant-concurrency, the model wrk and hey use).
// For each level it reports throughput (req/s), latency percentiles
// (p50/p90/p99) and the error rate, so the failure knee — the concurrency at
// which errors start or throughput stops climbing — is visible in the table.
//
// stdlib only; run with:  go run loadgen.go -url http://127.0.0.1:8137 ...
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8137", "base URL")
	path := flag.String("path", "/metadata/{key}", "request path; {key} is replaced by a random svc-NNNN")
	levels := flag.String("c", "1,8,16,32,64,128,256,512", "comma-separated concurrency levels")
	dur := flag.Duration("d", 8*time.Second, "duration per concurrency level")
	warm := flag.Duration("warm", 1*time.Second, "warm-up per level (not measured)")
	keyMax := flag.Int("keymax", 500, "highest svc-NNNN key to request")
	flag.Parse()

	// One shared transport with a generous connection pool so the client is
	// not the bottleneck: cap idle conns well above the top concurrency level.
	tr := &http.Transport{
		MaxIdleConns:        2048,
		MaxIdleConnsPerHost: 2048,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	fmt.Printf("# target   %s%s\n", *url, *path)
	fmt.Printf("# per-level duration=%s warmup=%s\n", *dur, *warm)
	fmt.Printf("%-6s %10s %9s %9s %9s %9s %9s %8s %8s\n",
		"conc", "req/s", "p50ms", "p90ms", "p99ms", "maxms", "meanms", "reqs", "err%")

	for _, tok := range strings.Split(*levels, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		var c int
		fmt.Sscanf(tok, "%d", &c)
		if c <= 0 {
			continue
		}
		runLevel(client, *url, *path, c, *dur, *warm, *keyMax)
	}
}

func runLevel(client *http.Client, base, path string, c int, dur, warm time.Duration, keyMax int) {
	// Warm-up (opens connections, primes caches) — not measured.
	if warm > 0 {
		warmCtx, cancel := context.WithTimeout(context.Background(), warm)
		var wg sync.WaitGroup
		for i := 0; i < c; i++ {
			wg.Add(1)
			go func(seed int64) {
				defer wg.Done()
				rng := rand.New(rand.NewSource(seed))
				for warmCtx.Err() == nil {
					doOne(client, base, path, rng, keyMax)
				}
			}(int64(i) + 1)
		}
		wg.Wait()
		cancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()

	var (
		wg     sync.WaitGroup
		errs   int64
		perLat = make([][]float64, c)
	)
	start := time.Now()
	for i := 0; i < c; i++ {
		wg.Add(1)
		go func(idx int, seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			lats := make([]float64, 0, 4096)
			for ctx.Err() == nil {
				t0 := time.Now()
				ok := doOne(client, base, path, rng, keyMax)
				ms := float64(time.Since(t0).Microseconds()) / 1000.0
				lats = append(lats, ms)
				if !ok {
					atomic.AddInt64(&errs, 1)
				}
			}
			perLat[idx] = lats
		}(i, int64(i)*7919+13)
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()

	all := make([]float64, 0, 1<<16)
	for _, s := range perLat {
		all = append(all, s...)
	}
	total := len(all)
	sort.Float64s(all)

	reqps := float64(total) / elapsed
	errPct := 0.0
	if total > 0 {
		errPct = float64(errs) / float64(total) * 100.0
	}
	fmt.Printf("%-6d %10.1f %9.2f %9.2f %9.2f %9.2f %9.2f %8d %8.2f\n",
		c, reqps, pct(all, 50), pct(all, 90), pct(all, 99), pct(all, 100), mean(all), total, errPct)
}

func doOne(client *http.Client, base, path string, rng *rand.Rand, keyMax int) bool {
	p := path
	if strings.Contains(p, "{key}") {
		key := fmt.Sprintf("svc-%04d", rng.Intn(keyMax)+1)
		p = strings.ReplaceAll(p, "{key}", key)
	}
	resp, err := client.Get(base + p)
	if err != nil {
		return false
	}
	// Must drain the body for connection reuse.
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := int(p / 100.0 * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
