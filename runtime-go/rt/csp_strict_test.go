package rt

// Strict Content-Security-Policy — every page Sky serves must run under
//
//	script-src 'self' 'wasm-unsafe-eval'
//
// with no hashes, no 'unsafe-inline' and no 'unsafe-eval'. THE DEFECT: the
// Sky.Live client (and so the Sky Console, a Live sub-app) was one inline
// <script> carrying the session id + CSRF token as JS literals, the Sky.Spa SSR
// page booted its wasm from an inline <script>, the legacy console shell was an
// inline <script>, and the client ran `data-sky-eval` through `new Function`. A
// proxy sending a strict policy (a real Caddy deployment) left all of them dead.
//
// The fix: executable script lives in same-origin, content-hashed files
// (/_sky/live.<hash>.js, /spa-boot.<hash>.js, /_sky/console-shell.<hash>.js);
// per-page data rides in non-executable <script type="application/json">
// blocks. SKY_CSP=strict makes the runtime send the policy itself.
//
// The browser half is scripts/csp-e2e.sh.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var scriptTagRe = regexp.MustCompile(`(?is)<script\b([^>]*)>`)
var attrTypeRe = regexp.MustCompile(`(?i)\btype\s*=\s*"([^"]*)"`)
var attrSrcRe = regexp.MustCompile(`(?i)\bsrc\s*=`)
var inlineHandlerRe = regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`)

// inlineExecutableScripts lists every <script> opening tag in page that a
// browser would EXECUTE from inline text: no src= and a type that is not a
// data block. A strict script-src blocks each one.
func inlineExecutableScripts(page string) []string {
	var out []string
	for _, m := range scriptTagRe.FindAllStringSubmatch(page, -1) {
		attrs := m[1]
		if attrSrcRe.MatchString(attrs) {
			continue
		}
		if t := attrTypeRe.FindStringSubmatch(attrs); t != nil {
			switch strings.ToLower(strings.TrimSpace(t[1])) {
			case "application/json", "application/ld+json", "importmap+json":
				continue
			}
		}
		out = append(out, m[0])
	}
	return out
}

func assertNoInlineExecutable(t *testing.T, what, page string) {
	t.Helper()
	if bad := inlineExecutableScripts(page); len(bad) > 0 {
		t.Fatalf("%s carries %d inline executable <script> element(s) a strict script-src blocks: %q\n%.800s",
			what, len(bad), bad, page)
	}
	if strings.Contains(strings.ToLower(page), "javascript:") {
		t.Fatalf("%s carries a javascript: URL", what)
	}
	if m := inlineHandlerRe.FindString(page); m != "" {
		t.Fatalf("%s carries an inline event-handler attribute: %q", what, m)
	}
}

func liveCfgFromPage(t *testing.T, page string) map[string]any {
	t.Helper()
	const open = `<script type="application/json" id="sky-live-cfg">`
	i := strings.Index(page, open)
	if i < 0 {
		t.Fatalf("page has no %s block:\n%.600s", open, page)
	}
	rest := page[i+len(open):]
	j := strings.Index(rest, "</script>")
	if j < 0 {
		t.Fatal("unterminated sky-live-cfg block")
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(rest[:j]), &cfg); err != nil {
		t.Fatalf("sky-live-cfg is not JSON: %v\n%s", err, rest[:j])
	}
	return cfg
}

func TestLivePageHasNoInlineExecutableScript(t *testing.T) {
	for _, env := range []string{"development", "production"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("ENV", env)
			t.Setenv("SKY_DEV_BANNER", "")
			app := newBindingTestApp("sky_sid")
			defer app.store.Close()
			page := servedPage(t, app)
			assertNoInlineExecutable(t, "the Sky.Live page", page)
			want := `<script src="` + liveClientPath + `"></script>`
			if !strings.Contains(page, want) {
				t.Fatalf("Sky.Live page does not load the client from %q:\n%.800s", want, page)
			}
			cfg := liveCfgFromPage(t, page)
			if sid, _ := cfg["sid"].(string); sid == "" {
				t.Fatalf("sky-live-cfg carries no session id: %v", cfg)
			}
			if _, ok := cfg["csrf"]; !ok {
				t.Fatalf("sky-live-cfg carries no csrf field: %v", cfg)
			}
		})
	}
}

// A sub-app (the Sky Console at /_sky/console) loads the client under its base.
func TestLiveSubAppPageLoadsClientUnderBase(t *testing.T) {
	t.Setenv("ENV", "development")
	app := newBindingTestApp("sky_sid")
	defer app.store.Close()
	app.basePath = "/_sky/console"
	rr := httptest.NewRecorder()
	app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/_sky/console/", nil))
	page := rr.Body.String()
	assertNoInlineExecutable(t, "the sub-app page", page)
	if want := `<script src="/_sky/console` + liveClientPath + `"></script>`; !strings.Contains(page, want) {
		t.Fatalf("sub-app page does not load the client from %q:\n%.800s", want, page)
	}
	if base, _ := liveCfgFromPage(t, page)["base"].(string); base != "/_sky/console" {
		t.Fatalf("sky-live-cfg base = %q, want /_sky/console", base)
	}
	mux := http.NewServeMux()
	registerSubAppRoutes(mux, app, "/_sky/console", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_sky/console"+liveClientPath, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != liveClientJS {
		t.Fatalf("sub-app mux does not serve the client at its base: status %d", rec.Code)
	}
}

// A per-page value cannot break out of the JSON block.
func TestLiveCfgEscapesScriptBreakout(t *testing.T) {
	blk := liveCfgBlock(liveBootCfg{Sid: "</script><script>alert(1)</script>", Csrf: "x"})
	if strings.Count(blk, "</script>") != 1 {
		t.Fatalf("a value broke out of the JSON block: %s", blk)
	}
}

func TestLiveClientAssetIsContentHashedAndImmutable(t *testing.T) {
	sum := sha256.Sum256([]byte(liveClientJS))
	if want := "/_sky/live." + hex.EncodeToString(sum[:])[:12] + ".js"; liveClientPath != want {
		t.Fatalf("liveClientPath = %q, want the content hash %q", liveClientPath, want)
	}
	rec := httptest.NewRecorder()
	serveStaticJS(liveClientJS)(rec, httptest.NewRequest(http.MethodGet, liveClientPath, nil))
	if rec.Code != 200 || rec.Body.String() != liveClientJS {
		t.Fatalf("status %d, body mismatch", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("Cache-Control = %q, want immutable for a hashed name", cc)
	}
}

func TestSpaSSRPageHasNoInlineExecutableScript(t *testing.T) {
	page := SpaSSRPageSeeded(`<title>t</title>`, `<p>hi</p>`, "main.abc123.wasm", `{"a":"</script>"}`, "init", []string{"a"})
	assertNoInlineExecutable(t, "the Sky.Spa SSR page", page)
	want := `<script src="` + SpaBootPath + `" data-wasm="/main.abc123.wasm"></script>`
	if !strings.Contains(page, want) {
		t.Fatalf("SSR page does not boot through %q:\n%s", want, page)
	}
	if !strings.Contains(page, `<script id="sky-model" type="application/json">`) {
		t.Fatal("SSR page lost the #sky-model seed")
	}
	sum := sha256.Sum256([]byte(SpaBootJS))
	if want := "/spa-boot." + hex.EncodeToString(sum[:])[:12] + ".js"; SpaBootPath != want {
		t.Fatalf("SpaBootPath = %q, want %q", SpaBootPath, want)
	}
}

func TestLegacyConsoleShellHasNoInlineScript(t *testing.T) {
	assertNoInlineExecutable(t, "the legacy console shell", consoleHTML)
	if !strings.Contains(consoleHTML, `<script src="`+consoleShellPath+`"></script>`) {
		t.Fatal("legacy console shell does not load its script from consoleShellPath")
	}
	rec := httptest.NewRecorder()
	HandleConsoleShellJS(rec, httptest.NewRequest(http.MethodGet, consoleShellPath, nil))
	if rec.Code != 200 || rec.Body.String() != consoleShellJS {
		t.Fatalf("console shell script not served: %d", rec.Code)
	}
}

// Grep gate: no runtime JS may evaluate a string. Every embedded client script,
// and every non-test Go source in the runtime, is scanned.
func TestRuntimeJSHasNoEval(t *testing.T) {
	forbidden := []string{"new Function", "eval(", "data-sky-eval", `setTimeout("`, `setInterval("`, "document.write("}
	scripts := map[string]string{
		"liveClientJS":    liveClientJS,
		"SpaBootJS":       SpaBootJS,
		"consoleShellJS":  consoleShellJS,
		"webviewSharedJS": webviewSharedJS,
	}
	for name, js := range scripts {
		for _, f := range forbidden {
			if strings.Contains(js, f) {
				t.Errorf("%s contains %q", name, f)
			}
		}
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		src := string(b)
		for _, bad := range []string{"new Function(", "__skyRunEvals"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s contains %q — no runtime path may evaluate a string", f, bad)
			}
		}
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d runtime sources; the gate is vacuous", scanned)
	}
}

func TestStrictCSPHeader(t *testing.T) {
	t.Run("off by default", func(t *testing.T) {
		t.Setenv("SKY_CSP", "")
		t.Setenv("SKY_LIVE_FRAME_ANCESTORS", "")
		h := http.Header{}
		setSecurityHeaders(h)
		if h.Get("Content-Security-Policy") != "" || h.Get("X-Frame-Options") != "SAMEORIGIN" {
			t.Fatalf("default headers changed: %v", h)
		}
	})
	t.Run("strict sets the policy", func(t *testing.T) {
		t.Setenv("SKY_CSP", "strict")
		t.Setenv("SKY_LIVE_FRAME_ANCESTORS", "")
		h := http.Header{}
		setSecurityHeaders(h)
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "script-src 'self' 'wasm-unsafe-eval'", "connect-src 'self'", "frame-ancestors 'self'", "object-src 'none'", "base-uri 'self'"} {
			if !strings.Contains(csp, want) {
				t.Fatalf("strict CSP %q lacks %q", csp, want)
			}
		}
		scriptSrc := ""
		for _, d := range strings.Split(csp, ";") {
			if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src ") {
				scriptSrc = d
			}
		}
		if strings.Contains(scriptSrc, "'unsafe-inline'") || strings.Contains(scriptSrc, "'unsafe-eval'") {
			t.Fatalf("strict script-src is not strict: %q", scriptSrc)
		}
		if h.Get("X-Frame-Options") != "SAMEORIGIN" {
			t.Fatalf("X-Frame-Options dropped under strict: %v", h)
		}
	})
	t.Run("strict honours frame ancestors", func(t *testing.T) {
		t.Setenv("SKY_CSP", "strict")
		t.Setenv("SKY_LIVE_FRAME_ANCESTORS", "https://cp.example")
		h := http.Header{}
		setSecurityHeaders(h)
		if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors https://cp.example") {
			t.Fatalf("frame ancestors lost: %q", csp)
		}
		if h.Get("X-Frame-Options") != "" {
			t.Fatal("X-Frame-Options would forbid the allowed cross-origin framer")
		}
	})
	t.Run("never overwrites an app policy", func(t *testing.T) {
		t.Setenv("SKY_CSP", "strict")
		h := http.Header{}
		h.Set("Content-Security-Policy", "default-src 'none'")
		setSecurityHeaders(h)
		if got := h.Values("Content-Security-Policy"); len(got) != 1 || got[0] != "default-src 'none'" {
			t.Fatalf("app CSP overwritten: %v", got)
		}
	})
	t.Run("static files get it too, without overwriting", func(t *testing.T) {
		t.Setenv("SKY_CSP", "strict")
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<!doctype html>")) })
		rec := httptest.NewRecorder()
		withStrictCSP(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self' 'wasm-unsafe-eval'") {
			t.Fatalf("static response lacks the strict policy: %v", rec.Header())
		}
		t.Setenv("SKY_CSP", "")
		rec = httptest.NewRecorder()
		withStrictCSP(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))
		if rec.Header().Get("Content-Security-Policy") != "" {
			t.Fatal("static response got a policy with SKY_CSP unset")
		}
	})
}

func TestStrictCSPOnLivePage(t *testing.T) {
	t.Setenv("SKY_CSP", "strict")
	app := newBindingTestApp("sky_sid")
	defer app.store.Close()
	rr := httptest.NewRecorder()
	app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rr.Header().Get("Content-Security-Policy"), "script-src 'self' 'wasm-unsafe-eval'") {
		t.Fatalf("Live page under SKY_CSP=strict lacks the policy: %v", rr.Header())
	}
}
