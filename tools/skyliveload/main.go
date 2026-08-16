// Command skyliveload is a load generator that speaks Sky.Live's real
// protocol: it establishes a session, holds an SSE connection open, and
// POSTs events, reading the patches back.
//
// WHY NOT wrk / ab
//
// Those measure static GETs. A Sky.Live interaction is a stateful
// exchange -- a session cookie, a CSRF token, a long-lived SSE stream,
// and a POST whose reply is a patch set computed against the session's
// previous render tree. Hitting `/` with a generic HTTP benchmarker
// measures first-paint HTML generation and would produce a confident,
// meaningless throughput number that has nothing to do with the
// per-interaction cost being sized.
//
// PROTOCOL, as implemented by the runtime at this commit
//
//	GET /                        -> mints sky_sid + __sky_csrf cookies,
//	                                returns HTML carrying data-sky-hid
//	                                handler ids.                (live.go:4082)
//	GET /_sky/sse?tab=&path=     -> text/event-stream: a 2 KiB comment pad,
//	                                `event: hello`, a resync `event: patch`,
//	                                then heartbeats every 15s.  (live.go:6191)
//	POST /_sky/event             -> {sessionId,seq,msg,args,handlerId,tab}
//	                                with X-Sky-Csrf; the reply carries THIS
//	                                tab's patches inline.       (live.go:4428)
//
// The acting tab's patches come back on the POST response, not over
// SSE -- SSE fan-out explicitly excludes the originating tab
// (live.go:4666). So request->response time is the complete, correct
// interaction latency, and no cross-stream correlation is needed.
//
// # PROVING THE GENERATOR IS ACTUALLY LOADING
//
// A client that silently fails to connect produces beautiful numbers.
// Every run therefore asserts, and reports:
//   - sessions that completed the handshake (cookie + SSE hello),
//   - interactions that returned a *successful, patch-bearing* reply,
//   - a per-status-class breakdown, so CSRF rejections or session-lost
//     404s can never be counted as throughput.
//
// A run whose successful-interaction count is zero, or whose error rate
// exceeds -max-error-rate, exits non-zero rather than printing a rate.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------

type config struct {
	baseURL       string
	sessions      int
	duration      time.Duration
	thinkTime     time.Duration
	thinkJitter   float64
	rampUp        time.Duration
	maxErrorRate  float64
	jsonOut       string
	label         string
	warmup        time.Duration
	selfCheckOnly bool

	hidSuffix    string
	hidContext   string
	hidCtxRe     *regexp.Regexp
	setupPath    string
	setup        []setupStep
	minPatchRate float64
}

func main() {
	var cfg config
	flag.StringVar(&cfg.baseURL, "url", "http://127.0.0.1:8000", "base URL of the Sky.Live app")
	flag.IntVar(&cfg.sessions, "sessions", 100, "number of concurrent sessions to hold")
	flag.DurationVar(&cfg.duration, "duration", 30*time.Second, "measurement window")
	flag.DurationVar(&cfg.thinkTime, "think", 1*time.Second, "mean delay between a session's interactions (0 = closed loop, as fast as possible)")
	flag.Float64Var(&cfg.thinkJitter, "think-jitter", 0.3, "fractional uniform jitter applied to think time")
	flag.DurationVar(&cfg.rampUp, "ramp", 5*time.Second, "spread session establishment over this window")
	flag.Float64Var(&cfg.maxErrorRate, "max-error-rate", 0.01, "fail the run if the error rate exceeds this")
	flag.StringVar(&cfg.jsonOut, "json", "", "write the full result as JSON to this path")
	flag.StringVar(&cfg.label, "label", "", "label recorded in the output (e.g. analytics=on)")
	flag.DurationVar(&cfg.warmup, "warmup", 3*time.Second, "discard interactions from this initial window")
	flag.BoolVar(&cfg.selfCheckOnly, "self-check", false, "establish one session, do one interaction, print the exchange, exit")

	// Handler selection + setup script. See script.go for the failure this
	// exists to prevent: the previous "first .click on the page" rule
	// chose skyforum's site-title link, whose Msg is a no-op on the page
	// it is rendered on, and three archived runs measured that.
	flag.StringVar(&cfg.hidSuffix, "hid-suffix", ".click",
		"only consider handler ids with this suffix (the DOM event)")
	flag.StringVar(&cfg.hidContext, "hid-context", "",
		"regex matched against the ~300 bytes following the hid attribute, to name a "+
			"specific element (handler ids are structural paths and carry no semantics)")
	flag.StringVar(&cfg.setupPath, "setup", "",
		"JSON file of setup steps run once per session before measurement (e.g. sign in)")
	flag.Float64Var(&cfg.minPatchRate, "min-patch-rate", 0.9,
		"fail the run unless at least this fraction of counted interactions returned "+
			"a patch; a run of empty exchanges is not a measurement of the diff path")

	// Target guards -- see guard.go. Load against anything but loopback is
	// opt-in, production is refused outright, and the resolved target is
	// confirmed before the first request.
	var remoteLoad, assumeYes, prodOverride bool
	flag.BoolVar(&remoteLoad, "remote-load", false,
		"permit generating load against a non-loopback target (default: refuse)")
	flag.BoolVar(&assumeYes, "assume-yes", false,
		"skip the interactive target confirmation (throwaway targets only)")
	flag.BoolVar(&prodOverride, "yes-i-will-take-down-production", false,
		"override the production-host refusal; do not use")
	flag.Parse()

	// Before ANY request, including -self-check, which is still a real
	// session against a real server.
	if err := checkTarget(cfg.baseURL, remoteLoad, prodOverride, assumeYes); err != nil {
		fmt.Fprintf(os.Stderr, "\n%v\n", err)
		os.Exit(3)
	}

	if cfg.hidContext != "" {
		re, err := regexp.Compile(cfg.hidContext)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad -hid-context: %v\n", err)
			os.Exit(3)
		}
		cfg.hidCtxRe = re
	}
	var err error
	if cfg.setup, err = loadSetup(cfg.setupPath); err != nil {
		fmt.Fprintf(os.Stderr, "bad -setup: %v\n", err)
		os.Exit(3)
	}

	if cfg.selfCheckOnly {
		if err := selfCheck(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "SELF-CHECK FAILED: %v\n", err)
			os.Exit(1)
		}
		return
	}

	res, err := run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RUN FAILED: %v\n", err)
		os.Exit(1)
	}
	res.print(os.Stdout)
	if cfg.jsonOut != "" {
		if err := res.writeJSON(cfg.jsonOut); err != nil {
			fmt.Fprintf(os.Stderr, "could not write JSON: %v\n", err)
			os.Exit(1)
		}
	}
	if !res.Valid {
		fmt.Fprintf(os.Stderr, "\nRUN INVALID: %s\n", res.InvalidReason)
		os.Exit(2)
	}
}

// ---------------------------------------------------------------------
// One session: the faithful protocol client
// ---------------------------------------------------------------------

var (
	// data-sky-hid="<sky-id>.<event>" -- the handler id the client posts
	// back. Emitted by renderVNode at live.go:418.
	hidRe = regexp.MustCompile(`data-sky-hid="([^"]+)"`)
	// The inline script's session id, as a cross-check on the cookie.
	sidRe = regexp.MustCompile(`var __skySid = "([^"]+)"`)
)

type session struct {
	id        int
	client    *http.Client
	base      string
	hidSuffix string
	hidCtxRe  *regexp.Regexp
	sid       string
	csrf    string
	tab     string
	handler string // a real data-sky-hid scraped from the served HTML

	sseCancel context.CancelFunc
	sseFrames atomic.Int64 // frames seen on the stream (liveness evidence)
	sseOpen   atomic.Bool

	// The most recent full-page body this session fetched. Two uses:
	// picking a handler after a scripted setup step navigates, and
	// checking that the ids a patch names actually exist in the DOM the
	// client is holding -- "patches were emitted" and "patches were
	// applicable" are different claims and this run has to make both.
	lastBody []byte

	patches      atomic.Int64 // patch objects returned across all replies
	patchesNoID  atomic.Int64 // patches whose id is absent from lastBody
	patchReplies atomic.Int64 // replies carrying at least one patch
}

// establish performs the browser's opening sequence: GET the page for
// cookies + handler ids, then open the SSE stream and wait for `hello`.
func (s *session) establish(ctx context.Context) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	s.client = &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 4,
			// SSE plus events per session; do not let the pool starve.
			MaxConnsPerHost: 0,
		},
		Timeout: 0, // per-request contexts instead; SSE must not time out
	}

	// --- GET / : mint session + CSRF, scrape a handler id ---
	req, _ := http.NewRequestWithContext(ctx, "GET", s.base+"/", nil)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET /: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("GET / body: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET / returned %d, want 200", resp.StatusCode)
	}

	for _, c := range resp.Cookies() {
		switch {
		case strings.HasSuffix(c.Name, "_sid") || c.Name == "sky_sid":
			s.sid = c.Value
		case c.Name == "__sky_csrf":
			s.csrf = c.Value
		}
	}
	if s.sid == "" {
		// Fall back to the inline script var, which carries the same id.
		if m := sidRe.FindSubmatch(body); m != nil {
			s.sid = string(m[1])
		}
	}
	if s.sid == "" {
		return fmt.Errorf("no session cookie and no __skySid in the page; " +
			"the app did not establish a Sky.Live session")
	}

	s.lastBody = body
	// The handler is CHOSEN, not stumbled upon. See script.go for why the
	// old "first hid ending .click" rule silently measured a no-op on
	// skyforum. A page with no matching handler is an error, never a
	// fallback to some other handler.
	hid, err := pickHandler(body, s.hidSuffix, s.hidCtxRe)
	if err != nil {
		return err
	}
	s.handler = hid

	s.tab = fmt.Sprintf("t%d-%d", s.id, rand.Int63())

	// --- GET /_sky/sse : hold the stream, confirm `hello` ---
	sseCtx, cancel := context.WithCancel(ctx)
	s.sseCancel = cancel
	helloCh := make(chan error, 1)
	go s.readSSE(sseCtx, helloCh)

	select {
	case err := <-helloCh:
		if err != nil {
			return fmt.Errorf("SSE: %w", err)
		}
	case <-time.After(15 * time.Second):
		cancel()
		return fmt.Errorf("SSE handshake timed out (no `hello` in 15s)")
	}
	return nil
}

// readSSE consumes the event stream for the session's lifetime. It
// signals the first `hello` on helloCh, then keeps draining -- a real
// browser holds the stream open, and the server's fan-out behaviour
// depends on a live connection existing.
func (s *session) readSSE(ctx context.Context, hello chan<- error) {
	url := fmt.Sprintf("%s/_sky/sse?tab=%s&path=%%2F", s.base, s.tab)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.client.Do(req)
	if err != nil {
		select {
		case hello <- fmt.Errorf("connect: %w", err):
		default:
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		select {
		case hello <- fmt.Errorf("stream returned %d (X-Sky-Status=%q)",
			resp.StatusCode, resp.Header.Get("X-Sky-Status")):
		default:
		}
		return
	}
	s.sseOpen.Store(true)
	defer s.sseOpen.Store(false)

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	sawHello := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event:") {
			s.sseFrames.Add(1)
			if !sawHello && strings.TrimSpace(line[6:]) == "hello" {
				sawHello = true
				select {
				case hello <- nil:
				default:
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
	if !sawHello {
		select {
		case hello <- fmt.Errorf("stream closed before `hello`"):
		default:
		}
	}
}

// outcome classifies one interaction. Only `ok` counts as throughput.
type outcome int

const (
	outOK outcome = iota
	outNoPatches
	outDesync
	outSessionLost
	outCSRF
	outHTTPError
	outTransport
)

func (o outcome) String() string {
	switch o {
	case outOK:
		return "ok"
	case outNoPatches:
		return "ok_no_patches"
	case outDesync:
		return "desync"
	case outSessionLost:
		return "session_lost"
	case outCSRF:
		return "csrf_rejected"
	case outHTTPError:
		return "http_error"
	default:
		return "transport_error"
	}
}

type eventReply struct {
	Seq     int64 `json:"seq"`
	Patches []struct {
		ID   string            `json:"id"`
		Text *string           `json:"text"`
		HTML *string           `json:"html"`
		Attr map[string]string `json:"attrs"`
	} `json:"patches"`
}

// interact performs one steady-state POST /_sky/event on the session's
// chosen handler.
func (s *session) interact(ctx context.Context, seq int64) (time.Duration, outcome, int, int) {
	return s.interactWith(ctx, seq, s.handler, nil)
}

// interactWith performs one POST /_sky/event and reads the reply fully.
// Returns the wall time of the complete exchange, its classification, the
// reply size, and the number of patch objects it carried.
func (s *session) interactWith(ctx context.Context, seq int64, handler string, args []any) (time.Duration, outcome, int, int) {
	if args == nil {
		args = []any{}
	}
	body, _ := json.Marshal(map[string]any{
		"sessionId": s.sid,
		"seq":       seq,
		"msg":       "",
		"args":      args,
		"handlerId": handler,
		"tab":       s.tab,
	})

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, "POST", s.base+"/_sky/event", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if s.csrf != "" {
		req.Header.Set("X-Sky-Csrf", s.csrf)
	}

	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		return time.Since(start), outTransport, 0, 0
	}
	payload, rerr := io.ReadAll(resp.Body)
	resp.Body.Close()
	elapsed := time.Since(start)
	if rerr != nil {
		return elapsed, outTransport, 0, 0
	}

	switch {
	case resp.StatusCode == 403:
		return elapsed, outCSRF, 0, 0
	case resp.Header.Get("X-Sky-Status") == "session-lost" || resp.StatusCode == 404:
		return elapsed, outSessionLost, 0, 0
	case resp.Header.Get("X-Sky-Status") == "desync":
		return elapsed, outDesync, 0, 0
	case resp.StatusCode != 200 && resp.StatusCode != 204:
		return elapsed, outHTTPError, 0, 0
	}

	// A text/html reply is the full-body fallback -- real work, real
	// patch payload, just not the JSON diff route.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return elapsed, outOK, len(payload), 1
	}

	var reply eventReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return elapsed, outHTTPError, 0, 0
	}
	if len(reply.Patches) == 0 {
		// Legitimate (the model advanced without changing the view) but
		// counted separately: a run consisting ENTIRELY of these is not
		// exercising the diff path and must not be quoted as throughput.
		return elapsed, outNoPatches, len(payload), 0
	}

	// "Patches were emitted" and "patches were APPLICABLE" are separate
	// claims. A browser applies a patch by finding `sky-id="<id>"` in its
	// DOM; a patch naming an id the client does not hold is a dropped
	// update, not work delivered (that class shipped as a real defect --
	// see memory `sky_live_patch_target_missing_2026_07_17`). Check the
	// ids against the last full page this session fetched.
	s.patches.Add(int64(len(reply.Patches)))
	s.patchReplies.Add(1)
	if len(s.lastBody) > 0 {
		for _, p := range reply.Patches {
			if p.ID == "" || !bytes.Contains(s.lastBody, []byte(`sky-id="`+p.ID+`"`)) {
				s.patchesNoID.Add(1)
			}
		}
	}
	return elapsed, outOK, len(payload), len(reply.Patches)
}

func (s *session) close() {
	if s.sseCancel != nil {
		s.sseCancel()
	}
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
}

// ---------------------------------------------------------------------
// Self-check: prove the client speaks the protocol before trusting it
// ---------------------------------------------------------------------

func selfCheck(cfg config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := &session{id: 0, base: strings.TrimRight(cfg.baseURL, "/"),
		hidSuffix: cfg.hidSuffix, hidCtxRe: cfg.hidCtxRe}
	if err := s.establish(ctx); err != nil {
		return err
	}
	defer s.close()
	if err := s.runSetup(ctx, cfg.setup, cfg.hidSuffix, cfg.hidCtxRe); err != nil {
		return err
	}

	fmt.Printf("session established\n")
	fmt.Printf("  sky_sid      %s\n", s.sid)
	fmt.Printf("  __sky_csrf   %s\n", abbrev(s.csrf))
	fmt.Printf("  tab          %s\n", s.tab)
	fmt.Printf("  handlerId    %s   (scraped from data-sky-hid)\n", s.handler)
	fmt.Printf("  SSE open     %v (%d frames so far)\n", s.sseOpen.Load(), s.sseFrames.Load())

	// FOUR interactions, not two. The steady-state interaction has to be
	// repeatable: skyforum's vote button TOGGLES, so it patches on every
	// press, but a handler that patches once and then settles into a fixed
	// point would pass a two-interaction check and then produce a run of
	// empty exchanges -- which is the failure this whole file exists to
	// stop being invisible.
	var empties, steady int
	for i := int64(1); i <= 4; i++ {
		d, out, n, np := s.interact(ctx, i)
		steady += np
		fmt.Printf("interaction %d: %s in %v, %d bytes of reply, %d patches\n", i, out, d, n, np)
		if out != outOK && out != outNoPatches {
			return fmt.Errorf("interaction %d classified %s -- the client is not "+
				"speaking the protocol correctly", i, out)
		}
		if np == 0 {
			empties++
		}
	}
	if empties > 0 {
		return fmt.Errorf("%d of 4 interactions returned zero patches: this "+
			"app+handler (%s) does not change the view on every press, so a load "+
			"run against it would measure empty exchanges and NOT the render/diff "+
			"path. Choose another handler with -hid-context, or script the state "+
			"it needs with -setup", empties, s.handler)
	}
	if u := s.patchesNoID.Load(); u > 0 {
		return fmt.Errorf("%d patches named a sky-id absent from the served page: "+
			"the server did the work but a browser would drop the update", u)
	}
	fmt.Printf("\nSELF-CHECK PASSED: %d patches over 4 steady-state interactions "+
		"(setup steps excluded), all naming ids present in the DOM. The server is "+
		"doing real per-interaction work.\n", steady)
	return nil
}

func abbrev(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:8] + "..."
}

// ---------------------------------------------------------------------
// The run
// ---------------------------------------------------------------------

type sample struct {
	at      time.Time
	latency time.Duration
	out     outcome
	patches int
}

type Result struct {
	Label              string         `json:"label"`
	URL                string         `json:"url"`
	Sessions           int            `json:"sessions_requested"`
	SessionsLive       int            `json:"sessions_established"`
	Duration           string         `json:"duration"`
	ThinkTime          string         `json:"think_time"`
	Interactions       int            `json:"interactions_counted"`
	Throughput         float64        `json:"interactions_per_sec"`
	P50ms              float64        `json:"p50_ms"`
	P95ms              float64        `json:"p95_ms"`
	P99ms              float64        `json:"p99_ms"`
	MaxMs              float64        `json:"max_ms"`
	Outcomes           map[string]int `json:"outcomes"`
	ErrorRate          float64        `json:"error_rate"`
	PatchesTotal       int            `json:"patches_total"`
	PatchBearing       int            `json:"interactions_with_patches"`
	PatchRate          float64        `json:"patch_rate"`
	PatchesPerInt      float64        `json:"patches_per_interaction"`
	PatchesUnresolved  int64          `json:"patches_naming_absent_ids"`
	Handler            string         `json:"handler_id"`
	SetupSteps         int            `json:"setup_steps"`
	SSEFramesTotal     int64          `json:"sse_frames_total"`
	SSEStillOpen       int            `json:"sse_still_open_at_end"`
	GeneratorCPUPct    float64        `json:"generator_cpu_percent_of_machine"`
	GeneratorSaturated bool           `json:"generator_possibly_saturated"`
	Valid              bool           `json:"valid"`
	InvalidReason      string         `json:"invalid_reason,omitempty"`
	Host               string         `json:"host"`
	GoMaxProcs         int            `json:"gomaxprocs"`
	StartedAt          string         `json:"started_at"`
}

func run(cfg config) (*Result, error) {
	base := strings.TrimRight(cfg.baseURL, "/")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Establish sessions, ramped ---
	sessions := make([]*session, 0, cfg.sessions)
	var mu sync.Mutex
	var wg sync.WaitGroup
	establishErrs := map[string]int{}

	gap := time.Duration(0)
	if cfg.rampUp > 0 && cfg.sessions > 1 {
		gap = cfg.rampUp / time.Duration(cfg.sessions)
	}

	fmt.Fprintf(os.Stderr, "establishing %d sessions over %v...\n", cfg.sessions, cfg.rampUp)
	for i := 0; i < cfg.sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Duration(i) * gap)
			s := &session{id: i, base: base, hidSuffix: cfg.hidSuffix, hidCtxRe: cfg.hidCtxRe}
			if err := s.establish(ctx); err != nil {
				mu.Lock()
				establishErrs[err.Error()]++
				mu.Unlock()
				return
			}
			if err := s.runSetup(ctx, cfg.setup, cfg.hidSuffix, cfg.hidCtxRe); err != nil {
				mu.Lock()
				establishErrs[err.Error()]++
				mu.Unlock()
				s.close()
				return
			}
			mu.Lock()
			sessions = append(sessions, s)
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if len(sessions) == 0 {
		var detail []string
		for e, n := range establishErrs {
			detail = append(detail, fmt.Sprintf("%dx %s", n, e))
		}
		return nil, fmt.Errorf("ZERO sessions established -- the generator is not "+
			"loading anything. Causes: %s", strings.Join(detail, "; "))
	}
	if len(establishErrs) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: %d/%d sessions failed to establish:\n",
			cfg.sessions-len(sessions), cfg.sessions)
		for e, n := range establishErrs {
			fmt.Fprintf(os.Stderr, "  %dx %s\n", n, e)
		}
	}
	fmt.Fprintf(os.Stderr, "%d sessions live; measuring for %v (warmup %v)\n",
		len(sessions), cfg.duration, cfg.warmup)

	// --- Drive interactions ---
	started := time.Now()
	warmupEnds := started.Add(cfg.warmup)
	deadline := warmupEnds.Add(cfg.duration)

	samples := make([][]sample, len(sessions))
	cpuStart := processCPU()

	for i, s := range sessions {
		wg.Add(1)
		go func(i int, s *session) {
			defer wg.Done()
			var local []sample
			var seq int64
			for time.Now().Before(deadline) {
				seq++
				d, out, _, np := s.interact(ctx, seq)
				local = append(local, sample{at: time.Now(), latency: d, out: out, patches: np})
				if cfg.thinkTime > 0 {
					j := 1 + (rand.Float64()*2-1)*cfg.thinkJitter
					time.Sleep(time.Duration(float64(cfg.thinkTime) * j))
				}
			}
			samples[i] = local
		}(i, s)
	}
	wg.Wait()
	cpuUsed := processCPU() - cpuStart
	wall := time.Since(started)

	// --- Aggregate, discarding warmup ---
	outcomes := map[string]int{}
	var lat []time.Duration
	counted := 0
	patchesInWindow := 0
	patchBearing := 0
	for _, ss := range samples {
		for _, sm := range ss {
			if sm.at.Before(warmupEnds) {
				continue
			}
			outcomes[sm.out.String()]++
			counted++
			patchesInWindow += sm.patches
			if sm.patches > 0 {
				patchBearing++
			}
			if sm.out == outOK || sm.out == outNoPatches {
				lat = append(lat, sm.latency)
			}
		}
	}

	var sseFrames, patchesNoID int64
	sseOpen := 0
	for _, s := range sessions {
		sseFrames += s.sseFrames.Load()
		patchesNoID += s.patchesNoID.Load()
		if s.sseOpen.Load() {
			sseOpen++
		}
		s.close()
	}

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	res := &Result{
		Label:          cfg.label,
		URL:            base,
		Sessions:       cfg.sessions,
		SessionsLive:   len(sessions),
		Duration:       cfg.duration.String(),
		ThinkTime:      cfg.thinkTime.String(),
		Interactions:   counted,
		Outcomes:       outcomes,
		SSEFramesTotal: sseFrames,
		SSEStillOpen:   sseOpen,
		Host:           hostname(),
		GoMaxProcs:     runtime.GOMAXPROCS(0),
		StartedAt:      started.UTC().Format(time.RFC3339),
		P50ms:          pct(lat, 0.50),
		P95ms:          pct(lat, 0.95),
		P99ms:          pct(lat, 0.99),
		MaxMs:          pct(lat, 1.0),

		PatchesTotal:      patchesInWindow,
		PatchBearing:      patchBearing,
		PatchesUnresolved: patchesNoID,
		SetupSteps:        len(cfg.setup),
	}
	if len(sessions) > 0 {
		res.Handler = sessions[0].handler
	}
	if counted > 0 {
		res.PatchRate = float64(patchBearing) / float64(counted)
		res.PatchesPerInt = float64(patchesInWindow) / float64(counted)
	}
	measured := wall - cfg.warmup
	if measured > 0 {
		res.Throughput = float64(counted) / measured.Seconds()
	}

	good := outcomes[outOK.String()] + outcomes[outNoPatches.String()]
	if counted > 0 {
		res.ErrorRate = float64(counted-good) / float64(counted)
	}

	// Generator saturation check: CPU seconds burned by THIS process
	// versus the wall-clock core budget. If the generator is using most
	// of the machine, the numbers describe the generator.
	if measured > 0 {
		res.GeneratorCPUPct = cpuUsed / measured.Seconds() / float64(runtime.NumCPU()) * 100
		res.GeneratorSaturated = res.GeneratorCPUPct > 70
	}

	// --- Validity: refuse to present a number we cannot stand behind ---
	res.Valid = true
	switch {
	case counted == 0:
		res.Valid, res.InvalidReason = false, "no interactions were recorded in the measurement window"
	case good == 0:
		res.Valid, res.InvalidReason = false, "every interaction failed; the throughput figure describes errors, not work"
	case outcomes[outOK.String()] == 0:
		res.Valid, res.InvalidReason = false,
			"no interaction produced a single patch: the server never ran the diff path, so this measures an empty exchange"
	case res.PatchRate < cfg.minPatchRate:
		// A MAJORITY test, not a "was there ever one" test. The archived
		// forum runs had ZERO, and the zero check above would have caught
		// them -- but a single stray patch among 5,000 empty exchanges
		// would have passed it while measuring exactly the same nothing.
		// Whatever fraction of interactions is supposed to hit the diff
		// path has to be declared and met.
		res.Valid, res.InvalidReason = false,
			fmt.Sprintf("only %.1f%% of interactions returned a patch (%d of %d); "+
				"-min-patch-rate is %.1f%%. The rest were empty exchanges that never "+
				"ran render+diff, so this does not measure the interaction path",
				res.PatchRate*100, patchBearing, counted, cfg.minPatchRate*100)
	case patchesNoID > 0:
		// Patches were emitted but name sky-ids the client is not holding.
		// The server did the work; the browser would drop the update.
		res.Valid, res.InvalidReason = false,
			fmt.Sprintf("%d patches named a sky-id absent from the page the session "+
				"was holding: emitted is not applied", patchesNoID)
	case res.ErrorRate > cfg.maxErrorRate:
		res.Valid, res.InvalidReason = false,
			fmt.Sprintf("error rate %.2f%% exceeds the %.2f%% limit", res.ErrorRate*100, cfg.maxErrorRate*100)
	case sseFrames == 0:
		res.Valid, res.InvalidReason = false,
			"no SSE frames were ever received: the sessions were not actually connected the way a browser connects"
	case res.GeneratorSaturated:
		res.Valid, res.InvalidReason = false,
			fmt.Sprintf("the generator itself used %.0f%% of this machine's CPU; the measurement describes the generator, not the server", res.GeneratorCPUPct)
	}
	return res, nil
}

func pct(sorted []time.Duration, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return float64(sorted[i].Microseconds()) / 1000.0
}

func (r *Result) print(w io.Writer) {
	fmt.Fprintf(w, "\n=== Sky.Live load run ===\n")
	if r.Label != "" {
		fmt.Fprintf(w, "label                %s\n", r.Label)
	}
	fmt.Fprintf(w, "url                  %s\n", r.URL)
	fmt.Fprintf(w, "sessions             %d requested, %d established\n", r.Sessions, r.SessionsLive)
	fmt.Fprintf(w, "think time           %s\n", r.ThinkTime)
	fmt.Fprintf(w, "interactions         %d\n", r.Interactions)
	fmt.Fprintf(w, "throughput           %.1f interactions/sec\n", r.Throughput)
	fmt.Fprintf(w, "latency p50/p95/p99  %.2f / %.2f / %.2f ms  (max %.2f)\n", r.P50ms, r.P95ms, r.P99ms, r.MaxMs)
	fmt.Fprintf(w, "error rate           %.3f%%\n", r.ErrorRate*100)
	fmt.Fprintf(w, "outcomes             %v\n", r.Outcomes)
	fmt.Fprintf(w, "handler              %s (%d setup steps/session)\n", r.Handler, r.SetupSteps)
	fmt.Fprintf(w, "patches              %d total, %.2f per interaction, %.1f%% of interactions bore one\n",
		r.PatchesTotal, r.PatchesPerInt, r.PatchRate*100)
	if r.PatchesUnresolved > 0 {
		fmt.Fprintf(w, "  UNAPPLIABLE        %d patches named an absent sky-id\n", r.PatchesUnresolved)
	}
	fmt.Fprintf(w, "SSE frames received  %d (across %d sessions; %d streams still open at end)\n",
		r.SSEFramesTotal, r.SessionsLive, r.SSEStillOpen)
	fmt.Fprintf(w, "generator CPU        %.1f%% of machine%s\n", r.GeneratorCPUPct,
		map[bool]string{true: "  <-- SATURATED, numbers describe the generator", false: ""}[r.GeneratorSaturated])
	fmt.Fprintf(w, "valid                %v\n", r.Valid)
	if !r.Valid {
		fmt.Fprintf(w, "invalid because      %s\n", r.InvalidReason)
	}
}

func (r *Result) writeJSON(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
