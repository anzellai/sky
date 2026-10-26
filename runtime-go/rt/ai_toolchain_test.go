//go:build !js

package rt

// Tests of ai_toolchain.go: the lock policy over the shipped locks, the
// toolchain id, and a full install from a local server with a fake uv and a
// fake CPython (shell scripts packed as the real archives are), so the pinned
// fetch, the digest check, the staging rename, the version check against the
// lock, the self-check, the lock file and the cleared environment are all
// exercised without the network.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestToolchainLocksFollowThePolicy(t *testing.T) {
	var locks []string
	fs.WalkDir(aiTrainAssets, "ai_train_assets/locks", func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".txt") {
			locks = append(locks, p)
		}
		return nil
	})
	// Unsloth is the one Python toolchain; Mlx and Fake train without Python.
	if len(locks) != 1 || !strings.Contains(locks[0], "unsloth") {
		t.Fatalf("expected the unsloth lock only, found %v", locks)
	}
	deny := []string{"unsloth-studio", "unsloth_studio", "studio"}
	for _, p := range locks {
		b, _ := aiTrainAssets.ReadFile(p)
		if _, err := aiTrainAssets.ReadFile(strings.TrimSuffix(p, ".txt") + ".in"); err != nil {
			t.Errorf("%s has no .in file it was compiled from", p)
		}
		for i, line := range strings.Split(string(b), "\n") {
			l := strings.TrimSpace(line)
			for _, bad := range []string{"-e ", "--editable", "git+", "http://", "https://", "file:", " @ ", "--index-url", "--extra-index-url", "--find-links", "--trusted-host"} {
				if strings.Contains(l, bad) && !strings.HasPrefix(l, "#") {
					t.Errorf("%s line %d: %q is not a hashed pinned wheel (%s)", p, i+1, l, bad)
				}
			}
		}
		pins, err := aiParseLock(b)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if len(pins) == 0 {
			t.Fatalf("%s pins nothing", p)
		}
		for _, pin := range pins {
			if len(pin.hashes) == 0 {
				t.Errorf("%s: %s %s has no hash", p, pin.name, pin.version)
			}
			for _, h := range pin.hashes {
				if !strings.HasPrefix(h, "sha256:") || !aiValidSha256(strings.TrimPrefix(h, "sha256:")) {
					t.Errorf("%s: %s hash %q is not sha256", p, pin.name, h)
				}
			}
			for _, d := range deny {
				if strings.Contains(pin.name, d) {
					t.Errorf("%s pins %s: the AGPL Unsloth Studio is never installed", p, pin.name)
				}
			}
		}
	}
}

func TestParseLockReadsPinsAndHashes(t *testing.T) {
	pins, err := aiParseLock([]byte("# comment\nMLX_LM==0.31.3 \\\n    --hash=sha256:" + strings.Repeat("a", 64) + " \\\n    --hash=sha256:" + strings.Repeat("b", 64) + "\nnumpy==2.5.3 \\\n    --hash=sha256:" + strings.Repeat("c", 64) + "\n"))
	if err != nil || len(pins) != 2 || pins[0].name != "mlx-lm" || pins[0].version != "0.31.3" || len(pins[0].hashes) != 2 {
		t.Fatalf("%+v %v", pins, err)
	}
	if _, err := aiParseLock([]byte("git+https://example.com/x.git\n")); err == nil {
		t.Fatal("a VCS requirement parsed")
	}
}

func TestToolchainIDChangesWithEveryInput(t *testing.T) {
	a := aiToolchainDef{name: "unsloth", lockData: []byte("x==1\n")}
	if a.id() != a.id() {
		t.Fatal("the id is not stable")
	}
	b := aiToolchainDef{name: "unsloth", lockData: []byte("x==2\n")}
	if a.id() == b.id() {
		t.Fatal("a lock change kept the id")
	}
	c := aiToolchainDef{name: "other", lockData: []byte("x==1\n")}
	if a.id() == c.id() {
		t.Fatal("two toolchains share an id")
	}
	if len(a.id()) != 16 {
		t.Fatalf("id %q", a.id())
	}
}

// ─── a full install against a local server ──────────────────────────────

const fakeUvScript = `#!/bin/sh
# A stand-in for uv: records its arguments and environment, and does what the
# toolchain installer asks of it.
echo "$@" >> "$FAKE_LOG_DIR/uv-calls.log"
env >> "$FAKE_LOG_DIR/uv-env.log"
case "$1" in
--version) echo "uv 0.12.16 (fake)"; exit 0 ;;
venv)
  for a in "$@"; do last="$a"; done
  for a in "$@"; do if [ "$prev" = "--python" ]; then py="$a"; fi; prev="$a"; done
  mkdir -p "$last/bin" "$last/lib/python3.12/site-packages"
  cp "$py" "$last/bin/python"; chmod +x "$last/bin/python"
  exit 0 ;;
pip)
  for a in "$@"; do last="$a"; done
  for a in "$@"; do if [ "$prev" = "--python" ]; then py="$a"; fi; prev="$a"; done
  sp="$(dirname "$(dirname "$py")")/lib/python3.12/site-packages"
  grep -E '^[A-Za-z0-9._-]+==' "$last" | sed -E 's/ .*//' | while IFS= read -r req; do
    name="${req%%==*}"; ver="${req##*==}"
    mkdir -p "$sp/$name-$ver.dist-info"
  done
  exit 0 ;;
esac
exit 3
`

const fakePythonScript = `#!/bin/sh
# A stand-in for the pinned CPython.
case "$*" in
*"--selfcheck"*) echo '{"type":"hello","v":1,"versions":{"python":"3.12.14"}}' ;;
*"sys.version"*) echo "python 3.12.14" ;;
*) exit 4 ;;
esac
`

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

type toolServer struct {
	uv, py   []byte
	requests int
	mu       sync.Mutex
}

// toolchainWorld points the toolchain pins at a local server serving a fake
// uv and a fake CPython, in a fresh AI home.
type worldOpts struct{ corruptPython, wrongVersion bool }

func toolchainWorld(t *testing.T, o worldOpts) (*toolServer, string) {
	corruptPython := o.corruptPython
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tools are shell scripts")
	}
	home := t.TempDir()
	t.Setenv("SKY_AI_HOME", home)
	t.Setenv("SKY_AI_FETCH", "on")
	logs := t.TempDir()
	// The installer clears the environment, so the log directory is written
	// into the script itself.
	uvScript := strings.ReplaceAll(fakeUvScript, "$FAKE_LOG_DIR", logs)
	if o.wrongVersion {
		uvScript = strings.Replace(uvScript, `ver="${req##*==}"`, `ver="9.9"`, 1)
	}
	ts := &toolServer{
		uv: tarGz(t, map[string]string{"uv-test/uv": uvScript}),
		py: tarGz(t, map[string]string{"python/bin/python3": fakePythonScript}),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.mu.Lock()
		ts.requests++
		ts.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/uv.tar.gz"):
			w.Write(ts.uv)
		case strings.HasSuffix(r.URL.Path, "/python.tar.gz"):
			if corruptPython {
				w.Write(append([]byte{}, ts.py[:len(ts.py)-1]...))
				return
			}
			w.Write(ts.py)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	savedUv, savedPy, savedUvURL, savedPyURL := aiUvAssets, aiPythonAssets, aiUvReleaseURL, aiPythonReleaseURL
	aiUvAssets = []aiToolAsset{{goos: runtime.GOOS, goarch: runtime.GOARCH, target: "test", file: "uv.tar.gz", sha256: sum(ts.uv)}}
	aiPythonAssets = []aiToolAsset{{goos: runtime.GOOS, goarch: runtime.GOARCH, target: "test", file: "python.tar.gz", sha256: sum(ts.py)}}
	aiUvReleaseURL = func(f string) string { return srv.URL + "/uv/" + f }
	aiPythonReleaseURL = func(f string) string { return srv.URL + "/py/" + f }
	t.Cleanup(func() {
		aiUvAssets, aiPythonAssets, aiUvReleaseURL, aiPythonReleaseURL = savedUv, savedPy, savedUvURL, savedPyURL
	})
	return ts, logs
}

const testLock = "alpha==1.0 \\\n    --hash=sha256:" + "1111111111111111111111111111111111111111111111111111111111111111" + "\nbeta-pkg==2.5 \\\n    --hash=sha256:" + "2222222222222222222222222222222222222222222222222222222222222222" + "\n"

func TestToolchainInstallsFromPinsAndChecksTheLock(t *testing.T) {
	_, logs := toolchainWorld(t, worldOpts{})
	t.Setenv("OPENAI_API_KEY", "sk-must-not-leak")
	t.Setenv("PIP_INDEX_URL", "https://evil.example/simple")
	t.Setenv("UV_INDEX_URL", "https://evil.example/simple")
	d := aiToolchainDef{name: "unsloth", lockData: []byte(testLock)}
	info, err := aiInstallToolchain(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != d.id() || !fileExists(info.Python) || !fileExists(filepath.Join(info.DriverDir, "sky_train.py")) || info.Packages != 2 {
		t.Fatalf("%+v", info)
	}
	for _, f := range []string{"ok.json", "sbom.json"} {
		if !fileExists(filepath.Join(info.Dir, f)) {
			t.Fatalf("no %s", f)
		}
	}
	sbom, _ := os.ReadFile(filepath.Join(info.Dir, "sbom.json"))
	if !strings.Contains(string(sbom), "beta-pkg") || !strings.Contains(string(sbom), "sha256:2222") {
		t.Fatalf("sbom %s", sbom)
	}
	calls, _ := os.ReadFile(filepath.Join(logs, "uv-calls.log"))
	for _, want := range []string{"venv --no-config --relocatable", "pip sync --no-config --require-hashes --only-binary :all: --index-url https://pypi.org/simple"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("uv was not called with %q:\n%s", want, calls)
		}
	}
	env, _ := os.ReadFile(filepath.Join(logs, "uv-env.log"))
	for _, bad := range []string{"OPENAI_API_KEY", "sk-must-not-leak", "PIP_INDEX_URL", "UV_INDEX_URL", "evil.example"} {
		if strings.Contains(string(env), bad) {
			t.Errorf("uv saw %s", bad)
		}
	}
	// Installed: a second call returns it without running uv again.
	before, _ := os.ReadFile(filepath.Join(logs, "uv-calls.log"))
	again, err := aiInstallToolchain(context.Background(), d)
	after, _ := os.ReadFile(filepath.Join(logs, "uv-calls.log"))
	if err != nil || again.ID != info.ID || len(after) != len(before) {
		t.Fatalf("reinstall: %v, uv calls %d -> %d", err, len(before), len(after))
	}
	// No staging directory is left behind.
	entries, _ := os.ReadDir(filepath.Dir(info.Dir))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "staging-") {
			t.Fatalf("staging left behind: %s", e.Name())
		}
	}
}

func TestToolchainRefusesAnEnvironmentThatDiffersFromTheLock(t *testing.T) {
	toolchainWorld(t, worldOpts{wrongVersion: true})
	d := aiToolchainDef{name: "unsloth", lockData: []byte(testLock)}
	_, err := aiInstallToolchain(context.Background(), d)
	f, ok := err.(*aiFailure)
	if !ok || f.code != aiToolchainFailed || !strings.Contains(f.msg, "alpha 1.0") {
		t.Fatalf("%v", err)
	}
	if fileExists(filepath.Join(d.dir(), "ok.json")) {
		t.Fatal("a toolchain that differs from its lock was installed")
	}
}

func TestToolchainChecksumMismatchInstallsNothing(t *testing.T) {
	toolchainWorld(t, worldOpts{corruptPython: true})
	_, err := aiInstallToolchain(context.Background(), aiToolchainDef{name: "unsloth", lockData: []byte(testLock)})
	f, ok := err.(*aiFailure)
	if !ok || f.code != aiChecksumMismatch {
		t.Fatalf("%v", err)
	}
	exp, _ := f.attrValue("expected")
	act, _ := f.attrValue("actual")
	if !aiValidSha256(exp) || !aiValidSha256(act) || exp == act {
		t.Fatalf("both digests: %q %q", exp, act)
	}
	if fileExists(filepath.Join(aiHome(), "python")) {
		entries, _ := os.ReadDir(filepath.Join(aiHome(), "python"))
		if len(entries) > 0 {
			t.Fatalf("something was installed: %v", entries)
		}
	}
}

func TestToolchainNotProvisionedWhenFetchIsOff(t *testing.T) {
	toolchainWorld(t, worldOpts{})
	t.Setenv("SKY_AI_FETCH", "off")
	_, err := aiEnsureToolchainDef(context.Background(), aiToolchainDef{name: "unsloth", lockData: []byte(testLock)})
	f, ok := err.(*aiFailure)
	if !ok || f.code != aiNotProvisioned {
		t.Fatalf("%v", err)
	}
	if cmd, _ := f.attrValue("command"); cmd != "sky ai provision --train unsloth" {
		t.Fatalf("command %q", cmd)
	}
}

func TestToolchainConcurrentProvisionInstallsOnce(t *testing.T) {
	_, logs := toolchainWorld(t, worldOpts{})
	d := aiToolchainDef{name: "unsloth", lockData: []byte(testLock)}
	var wg sync.WaitGroup
	ids := make([]string, 4)
	errs := make([]error, 4)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			info, err := aiInstallToolchain(context.Background(), d)
			ids[i], errs[i] = info.ID, err
		}(i)
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil || ids[i] != d.id() {
			t.Fatalf("install %d: %v %s", i, errs[i], ids[i])
		}
	}
	calls, _ := os.ReadFile(filepath.Join(logs, "uv-calls.log"))
	if n := strings.Count(string(calls), "venv "); n != 1 {
		t.Fatalf("%d venvs were created:\n%s", n, calls)
	}
}

func TestToolchainForHost(t *testing.T) {
	_, err := aiToolchainFor("unsloth")
	if runtime.GOOS == "darwin" {
		if f, ok := err.(*aiFailure); !ok || f.code != aiNoAccelerator {
			t.Fatalf("unsloth on macOS: %v", err)
		}
	}
	if _, err := aiToolchainFor("nope"); err == nil {
		t.Fatal("an unknown toolchain")
	}
	files, err := aiDriverFiles()
	if err != nil || files["sky_train.py"] == nil || files["backends/unsloth.py"] == nil {
		t.Fatalf("driver files %v", err)
	}
	// Mlx and Fake train without Python: no driver backend and no toolchain.
	if files["backends/fake.py"] != nil || files["backends/mlx.py"] != nil {
		t.Fatal("the driver still carries a Python Mlx or Fake backend")
	}
	for _, name := range []string{"mlx", "fake"} {
		f, ok := func() (*aiFailure, bool) { _, err := aiToolchainFor(name); f, ok := err.(*aiFailure); return f, ok }()
		if !ok || f.code != aiInvalidSetting {
			t.Fatalf("%s has a Python toolchain", name)
		}
	}
	for name := range files {
		if strings.Contains(name, "__pycache__") || strings.HasPrefix(filepath.Base(name), ".") {
			t.Fatalf("the driver embeds %s", name)
		}
	}
	if fmt.Sprint(len(aiDriverDigest())) != "64" {
		t.Fatal("driver digest")
	}
}

func TestTrainBaseEnsureDownloadsAPinnedSnapshot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SKY_AI_HOME", home)
	t.Setenv("SKY_AI_FETCH", "on")
	files := map[string]string{"config.json": `{"model_type":"qwen2"}`, "model.safetensors": "weights"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/resolve/0123456789012345678901234567890123456789/") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(files[filepath.Base(r.URL.Path)]))
	}))
	defer srv.Close()
	saved := aiHuggingFaceRevisionURL
	aiHuggingFaceRevisionURL = func(repo, rev, f string) string { return srv.URL + "/" + repo + "/resolve/" + rev + "/" + f }
	defer func() { aiHuggingFaceRevisionURL = saved }()
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	snap := fmt.Sprintf(`{"repo":"org/model","revision":"0123456789012345678901234567890123456789","files":[{"path":"config.json","sha256":%q},{"path":"model.safetensors","sha256":%q}]}`,
		sum(files["config.json"]), sum(files["model.safetensors"]))
	dir, err := aiTrainBaseEnsure(snap)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "model.safetensors")); string(b) != "weights" {
		t.Fatalf("%s", b)
	}
	bad := strings.Replace(snap, sum("weights"), sum("other"), 1)
	os.RemoveAll(filepath.Join(home, "bases"))
	if _, err := aiTrainBaseEnsure(bad); err == nil || err.(*aiFailure).code != aiChecksumMismatch {
		t.Fatalf("%v", err)
	}
	t.Setenv("SKY_AI_FETCH", "off")
	if _, err := aiTrainBaseEnsure(snap); err == nil || err.(*aiFailure).code != aiNotProvisioned {
		t.Fatalf("%v", err)
	}
	if _, err := aiTrainBaseEnsure(`{"repo":"org/model","revision":"main","files":[{"path":"x","sha256":"` + sum("x") + `"}]}`); err == nil || err.(*aiFailure).code != aiInvalidSetting {
		t.Fatalf("an unpinned revision: %v", err)
	}
}
