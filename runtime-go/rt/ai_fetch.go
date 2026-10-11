//go:build !js

package rt

// ai_fetch.go — where Std.Ai.Local keeps its engines and models, and how it
// fetches them.
//
// The procedure is the embedded-PostgreSQL bundle procedure, reused: the
// expected digest is pinned in code (ai_pins.go, or a model reference the
// user wrote), the digest is taken from the bytes on disk, a mismatch
// installs nothing and names both digests, an archive is extracted outside
// the cache and renamed into place, and the engine binary runs once
// (`--version`) before the install counts.
//
// Layout under the AI home (`<PREFIX>_AI_HOME`, else `$SKY_HOME/ai`, else
// `<user cache dir>/sky/ai`: `~/Library/Caches/sky/ai` on macOS,
// `$XDG_CACHE_HOME/sky/ai` or `~/.cache/sky/ai` on Linux). There is one AI
// home per user, never one per project, so every app, test and worktree on
// the machine shares one copy of each engine and model:
//
//	engines/llama/<build>-<variant>/llama-server   the pinned engine
//	models/sha256/<hex>.gguf                        content-addressed models
//	downloads/                                      partial downloads (resumable)
//	run/<id>.key                                    per-run engine API keys (0600)
//	engines.json                                    the running-engine registry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// aiHome is the root of the engine and model cache.
func aiHome() string {
	if h := strings.TrimSpace(skyGetenv("AI_HOME")); h != "" {
		return h
	}
	if h := strings.TrimSpace(os.Getenv("SKY_HOME")); h != "" {
		return filepath.Join(h, "ai")
	}
	if c, err := os.UserCacheDir(); err == nil && c != "" {
		return filepath.Join(c, "sky", "ai")
	}
	return filepath.Join(skyHomeDir(), "ai")
}

// aiFetchAllowed reports whether a missing artefact may be downloaded at run
// time: `<PREFIX>_AI_FETCH` = on / off, else on in development and off in
// production (a production host provisions ahead with `sky ai provision`).
func aiFetchAllowed() (bool, error) {
	switch v := strings.ToLower(strings.TrimSpace(skyGetenv("AI_FETCH"))); v {
	case "":
		return !productionFromEnv(), nil
	case "on", "1", "true", "yes":
		return true, nil
	case "off", "0", "false", "no":
		return false, nil
	default:
		return false, aiFailf(aiInvalidSetting, "%s=%q: use on or off", skyEnvName("AI_FETCH"), v)
	}
}

// aiDevelopment reports whether the app runs in development (ENV unset or
// dev / development / local), the same predicate the production gate uses.
func aiDevelopment() bool { return !productionFromEnv() }

// aiHTTPClient fetches artefacts. No overall timeout: a model is hundreds of
// megabytes. The transport's dial and header timeouts bound a dead server.
var aiHTTPClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ResponseHeaderTimeout: 60 * time.Second,
	IdleConnTimeout:       30 * time.Second,
}}

// aiValidSha256 reports whether s is 64 lower-case hex digits.
func aiValidSha256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// aiFileSha256 hashes a file on disk.
func aiFileSha256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// aiDownload fetches url into dest and returns the digest of the bytes
// written. When want is set, a mismatch removes the download and fails with
// ChecksumMismatch, so nothing unverified is ever left at dest.
//
// A download is resumable: the bytes land in `<downloads>/<name>.partial` and
// a later attempt continues with a Range request. The partial file is hashed
// again before it counts, because the digest is taken from the bytes on
// disk, never from a running total that a restart would lose.
func aiDownload(ctx context.Context, url, dest, want string) (string, error) {
	dl := filepath.Join(aiHome(), "downloads")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		return "", aiFailf(aiEngineFailed, "cannot create %s: %v", dl, err)
	}
	unlock, err := aiLockFile(filepath.Join(dl, filepath.Base(dest)+".lock"), 10*time.Minute)
	if err != nil {
		return "", err
	}
	defer unlock()
	partial := filepath.Join(dl, filepath.Base(dest)+".partial")

	for attempt := 0; attempt < 2; attempt++ {
		var have int64
		if st, err := os.Stat(partial); err == nil {
			have = st.Size()
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return "", aiFailf(aiInvalidSetting, "bad download URL %q: %v", url, err)
		}
		if have > 0 {
			req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
		}
		resp, err := aiHTTPClient.Do(req)
		if err != nil {
			return "", aiFailf(aiUnreachable, "download %s: %v", url, err)
		}
		flags := os.O_CREATE | os.O_WRONLY
		switch {
		case resp.StatusCode == http.StatusPartialContent && have > 0:
			flags |= os.O_APPEND
		case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && have > 0:
			// The partial file is already complete (or longer than the
			// object). Hash it as it is; a wrong file fails the digest.
			resp.Body.Close()
			flags = 0
		case resp.StatusCode == http.StatusOK:
			flags |= os.O_TRUNC
		default:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return "", aiFailf(aiUnreachable, "download %s: HTTP %d %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if flags != 0 {
			f, err := os.OpenFile(partial, flags, 0o644)
			if err != nil {
				resp.Body.Close()
				return "", aiFailf(aiEngineFailed, "cannot write %s: %v", partial, err)
			}
			_, cerr := io.Copy(f, resp.Body)
			resp.Body.Close()
			if err := f.Close(); cerr == nil {
				cerr = err
			}
			if cerr != nil {
				return "", aiFailf(aiUnreachable, "download %s interrupted: %v (it resumes on the next attempt)", url, cerr)
			}
		}
		got, err := aiFileSha256(partial)
		if err != nil {
			return "", aiFailf(aiEngineFailed, "cannot hash %s: %v", partial, err)
		}
		if want != "" && got != want {
			os.Remove(partial)
			if have > 0 && attempt == 0 {
				// A stale partial from another file or a corrupted resume:
				// start again once from zero before calling it a mismatch.
				continue
			}
			return "", aiFailf(aiChecksumMismatch, "%s: expected sha256 %s, the downloaded bytes are sha256 %s; nothing was installed", url, want, got)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", aiFailf(aiEngineFailed, "cannot create %s: %v", filepath.Dir(dest), err)
		}
		if err := os.Rename(partial, dest); err != nil {
			return "", aiFailf(aiEngineFailed, "cannot install %s: %v", dest, err)
		}
		return got, nil
	}
	return "", aiFailf(aiEngineFailed, "download %s did not complete", url)
}

// aiLockFile takes an exclusive lock by creating path with O_EXCL. A lock
// whose owner process is gone, or that is older than stale, is broken: a
// killed fetch must not block every later one. Portable (no flock), because
// the runtime also builds for Windows.
func aiLockFile(path string, stale time.Duration) (func(), error) {
	deadline := time.Now().Add(stale + 5*time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, aiFailf(aiEngineFailed, "cannot create lock %s: %v", path, err)
		}
		if b, rerr := os.ReadFile(path); rerr == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			st, serr := os.Stat(path)
			old := serr == nil && time.Since(st.ModTime()) > stale
			if (pid > 0 && !processAlive(pid)) || old {
				os.Remove(path)
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, aiFailf(aiEngineFailed, "lock %s is held by another process", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ─── the engine ─────────────────────────────────────────────────────────

// aiEngineBinary is a discovered llama-server.
type aiEngineBinary struct {
	path    string // the llama-server executable
	variant string // "metal", "cpu", or "custom" for an operator's own build
	build   string // the pinned build tag, or "custom"
}

const aiLlamaServerName = "llama-server"

func aiExe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// aiHostAsset is the pinned engine archive for this host, if there is one.
func aiHostAsset() (aiLlamaAsset, bool) {
	for _, a := range aiLlamaAssets {
		if a.goos == runtime.GOOS && a.goarch == runtime.GOARCH {
			return a, true
		}
	}
	return aiLlamaAsset{}, false
}

func aiEngineDir(a aiLlamaAsset) string {
	return filepath.Join(aiHome(), "engines", "llama", aiLlamaBuild+"-"+a.variant)
}

// aiDiscoverEngine finds llama-server: the operator's `<PREFIX>_AI_LLAMA_BIN`
// directory (set but incomplete is an error, as with SKY_POSTGRES_BIN), else
// the pinned cache. PATH is not consulted: an unpinned build breaks the
// launch-argument and log contracts the supervisor relies on. With fetch
// allowed a missing pinned build is fetched; otherwise NotProvisioned.
func aiDiscoverEngine(ctx context.Context) (aiEngineBinary, error) {
	if dir := strings.TrimSpace(skyGetenv("AI_LLAMA_BIN")); dir != "" {
		p := filepath.Join(dir, aiExe(aiLlamaServerName))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return aiEngineBinary{}, aiFailf(aiNotProvisioned,
				"%s=%s has no %s in it; point it at the directory that holds llama-server, or unset it to use the pinned build",
				skyEnvName("AI_LLAMA_BIN"), dir, aiExe(aiLlamaServerName))
		}
		return aiEngineBinary{path: p, variant: "custom", build: "custom"}, nil
	}
	a, ok := aiHostAsset()
	if !ok {
		return aiEngineBinary{}, aiFailf(aiNotProvisioned,
			"there is no pinned llama-server build for %s/%s; install llama.cpp and set %s to the directory that holds llama-server",
			runtime.GOOS, runtime.GOARCH, skyEnvName("AI_LLAMA_BIN"))
	}
	bin := filepath.Join(aiEngineDir(a), aiExe(aiLlamaServerName))
	if fileExists(bin) {
		return aiEngineBinary{path: bin, variant: a.variant, build: aiLlamaBuild}, nil
	}
	allowed, err := aiFetchAllowed()
	if err != nil {
		return aiEngineBinary{}, err
	}
	if !allowed {
		return aiEngineBinary{}, aiFailf(aiNotProvisioned,
			"llama-server %s is not installed and fetching is off (%s); run `sky ai provision` on this host first",
			aiLlamaBuild, skyEnvName("AI_FETCH"))
	}
	return aiProvisionEngine(ctx)
}

// aiProvisionEngine downloads, verifies and installs the pinned engine for
// this host. Idempotent: an installed build returns at once.
func aiProvisionEngine(ctx context.Context) (aiEngineBinary, error) {
	if strings.TrimSpace(skyGetenv("AI_LLAMA_BIN")) != "" {
		// The operator brings the engine: provisioning checks it, fetches
		// nothing.
		b, err := aiDiscoverEngine(ctx)
		if err != nil {
			return b, err
		}
		return b, aiCheckEngineRuns(b.path)
	}
	a, ok := aiHostAsset()
	if !ok {
		return aiEngineBinary{}, aiFailf(aiNotProvisioned,
			"there is no pinned llama-server build for %s/%s; install llama.cpp and set %s",
			runtime.GOOS, runtime.GOARCH, skyEnvName("AI_LLAMA_BIN"))
	}
	final := aiEngineDir(a)
	bin := filepath.Join(final, aiExe(aiLlamaServerName))
	if fileExists(bin) {
		return aiEngineBinary{path: bin, variant: a.variant, build: aiLlamaBuild}, nil
	}
	archive := filepath.Join(aiHome(), "downloads", a.file)
	if _, err := aiDownload(ctx, aiLlamaReleaseURL(a.file), archive, a.sha256); err != nil {
		return aiEngineBinary{}, err
	}
	defer os.Remove(archive)
	if err := aiInstallArchive(archive, final); err != nil {
		return aiEngineBinary{}, err
	}
	return aiEngineBinary{path: bin, variant: a.variant, build: aiLlamaBuild}, nil
}

// aiInstallArchive extracts a verified engine archive outside the cache,
// checks that its llama-server runs, and renames it into place. The archive's
// single top-level directory (`llama-<build>/`) is the engine directory.
func aiInstallArchive(archive, final string) error {
	parent := filepath.Dir(final)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return aiFailf(aiEngineFailed, "cannot create %s: %v", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return aiFailf(aiEngineFailed, "cannot create a staging directory: %v", err)
	}
	defer os.RemoveAll(tmp)
	f, err := os.Open(archive)
	if err != nil {
		return aiFailf(aiEngineFailed, "cannot open %s: %v", archive, err)
	}
	err = extractTarGz(f, tmp)
	f.Close()
	if err != nil {
		return aiFailf(aiEngineFailed, "cannot extract %s: %v", filepath.Base(archive), err)
	}
	root := tmp
	if entries, err := os.ReadDir(tmp); err == nil && len(entries) == 1 && entries[0].IsDir() {
		root = filepath.Join(tmp, entries[0].Name())
	}
	bin := filepath.Join(root, aiExe(aiLlamaServerName))
	if !fileExists(bin) {
		return aiFailf(aiEngineFailed, "%s has no %s", filepath.Base(archive), aiLlamaServerName)
	}
	if err := aiCheckEngineRuns(bin); err != nil {
		return err
	}
	if err := os.Rename(root, final); err != nil {
		if fileExists(filepath.Join(final, aiExe(aiLlamaServerName))) {
			return nil // another process installed it first
		}
		return aiFailf(aiEngineFailed, "cannot install the engine at %s: %v", final, err)
	}
	return nil
}

// aiCheckEngineRuns runs `llama-server --version` once: a build that cannot
// run on this host (wrong architecture, a missing library) is refused before
// it counts as installed.
func aiCheckEngineRuns(bin string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = aiEngineEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return aiFailf(aiEngineFailed, "%s --version failed on this host: %v: %s", bin, err, aiTail(string(out), 600))
	}
	if !strings.Contains(string(out), "version") {
		return aiFailf(aiEngineFailed, "%s --version printed no version: %s", bin, aiTail(string(out), 600))
	}
	return nil
}

// ─── models ─────────────────────────────────────────────────────────────

// aiModelRef is a pinned model reference: `hf:<org>/<repo>/<file>#sha256=<hex>`
// or `file:<path>#sha256=<hex>`. The same syntax is the Sky value's string
// form and the `<PREFIX>_AI_<SLOT>_MODEL` value for a local slot.
type aiModelRef struct {
	kind   string // "hf" or "file"
	repo   string // hf only
	file   string // hf: the file in the repo; file: the path
	sha256 string
}

func (m aiModelRef) String() string {
	if m.kind == "hf" {
		return "hf:" + m.repo + "/" + m.file + "#sha256=" + m.sha256
	}
	return "file:" + m.file + "#sha256=" + m.sha256
}

// aiParseModelRef parses a model reference. A reference without a valid
// digest is refused: an unpinned model cannot be loaded.
func aiParseModelRef(s string) (aiModelRef, error) {
	s = strings.TrimSpace(s)
	body, frag, ok := strings.Cut(s, "#")
	if !ok || !strings.HasPrefix(frag, "sha256=") {
		return aiModelRef{}, aiFailf(aiInvalidSetting,
			"model reference %q has no #sha256=<hex> digest; an unpinned model cannot be loaded (run `sky ai pull <repo> <file>` to get the digest)", s)
	}
	sum := strings.ToLower(strings.TrimPrefix(frag, "sha256="))
	if !aiValidSha256(sum) {
		return aiModelRef{}, aiFailf(aiInvalidSetting, "model reference %q: the digest must be 64 hex digits", s)
	}
	switch {
	case strings.HasPrefix(body, "hf:"):
		rest := strings.TrimPrefix(body, "hf:")
		parts := strings.Split(rest, "/")
		if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[len(parts)-1] == "" {
			return aiModelRef{}, aiFailf(aiInvalidSetting, "model reference %q: use hf:<org>/<repo>/<file>#sha256=<hex>", s)
		}
		return aiModelRef{kind: "hf", repo: parts[0] + "/" + parts[1], file: strings.Join(parts[2:], "/"), sha256: sum}, nil
	case strings.HasPrefix(body, "file:"):
		p := strings.TrimPrefix(body, "file:")
		if p == "" {
			return aiModelRef{}, aiFailf(aiInvalidSetting, "model reference %q: the path is empty", s)
		}
		return aiModelRef{kind: "file", file: p, sha256: sum}, nil
	}
	return aiModelRef{}, aiFailf(aiInvalidSetting, "model reference %q: use hf:<org>/<repo>/<file>#sha256=<hex> or file:<path>#sha256=<hex>", s)
}

func aiModelStorePath(sum string) string {
	return filepath.Join(aiHome(), "models", "sha256", sum+".gguf")
}

// aiEnsureModel returns a verified local path for a model. A Hugging Face
// model lives in the content-addressed store (its name is its digest, and it
// was verified when it was stored). A `file:` model is hashed where it lies,
// because the user may have replaced the file since it was written down.
func aiEnsureModel(ctx context.Context, m aiModelRef) (string, error) {
	if m.kind == "file" {
		got, err := aiFileSha256(m.file)
		if err != nil {
			return "", aiFailf(aiNotProvisioned, "model file %s: %v", m.file, err)
		}
		if got != m.sha256 {
			return "", aiFailf(aiChecksumMismatch, "model file %s: expected sha256 %s, the file is sha256 %s; it was not loaded", m.file, m.sha256, got)
		}
		return m.file, nil
	}
	dest := aiModelStorePath(m.sha256)
	if fileExists(dest) {
		return dest, nil
	}
	allowed, err := aiFetchAllowed()
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", aiFailf(aiNotProvisioned,
			"model %s is not in %s and fetching is off (%s); run `sky ai pull %s %s` on this host first",
			m.String(), filepath.Dir(dest), skyEnvName("AI_FETCH"), m.repo, m.file)
	}
	if _, err := aiDownload(ctx, aiHuggingFaceURL(m.repo, m.file), dest, m.sha256); err != nil {
		return "", err
	}
	return dest, nil
}

// aiPullUnpinned downloads a Hugging Face file whose digest is not known yet
// (`sky ai pull`), stores it by the digest of the bytes received, and returns
// that digest so the caller can pin it in code.
func aiPullUnpinned(ctx context.Context, repo, file string) (path, sum string, size int64, err error) {
	tmpName := "pull-" + aiRandomHex(8) + ".gguf"
	tmp := filepath.Join(aiHome(), "downloads", tmpName+".done")
	sum, err = aiDownload(ctx, aiHuggingFaceURL(repo, file), tmp, "")
	if err != nil {
		return "", "", 0, err
	}
	dest := aiModelStorePath(sum)
	if fileExists(dest) {
		os.Remove(tmp)
	} else {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", "", 0, aiFailf(aiEngineFailed, "cannot create %s: %v", filepath.Dir(dest), err)
		}
		if err := os.Rename(tmp, dest); err != nil {
			return "", "", 0, aiFailf(aiEngineFailed, "cannot store %s: %v", dest, err)
		}
	}
	st, _ := os.Stat(dest)
	if st != nil {
		size = st.Size()
	}
	return dest, sum, size, nil
}

func aiRandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// aiTail keeps the last n bytes of s, for a log tail in an error message.
func aiTail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
