//go:build !js

package rt

// ai_toolchain.go — the managed Python toolchains Std.Ai.Train runs its
// driver in. The user never installs Python, and the system Python is never
// used.
//
// A toolchain is built from pinned parts only (ai_pins.go): the uv release,
// the CPython build from python-build-standalone, and a hash lock of wheels
// (ai_train_assets/locks/<name>-<platform>.txt, made by
// scripts/ai/lock-toolchains.sh). Its id is the SHA-256 of all of them plus
// the driver's own files, so a changed pin or driver installs a new toolchain
// beside the old one instead of mutating it. Layout under the AI home:
//
//	tools/uv/<version>/uv
//	python/<tag>-<target>/bin/python3
//	toolchains/<name>/<id>/venv/            the locked packages
//	toolchains/<name>/<id>/driver/          the Sky training driver
//	toolchains/<name>/<id>/sbom.json        every package with its version and hashes
//	toolchains/<name>/<id>/ok.json          written last: the toolchain is complete
//	cache/uv/                               uv's wheel cache
//
// The install runs under a lock file, in a staging directory renamed into
// place, with a cleared environment (no PIP_* or UV_* setting of the user's,
// no token): `uv venv --relocatable`, then
// `uv pip sync --require-hashes --only-binary :all: --no-config`, so no wheel
// installs unless its hash is in the lock and no source build runs. The
// installed versions are read back from the environment's metadata and must
// equal the lock, and the driver's self-check must print its `hello` line,
// before ok.json is written.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

//go:embed ai_train_assets
var aiTrainAssets embed.FS

// aiToolchainDef is one toolchain for this host.
type aiToolchainDef struct {
	name     string
	lock     string // the embedded lock path, "" for the standard-library-only Fake toolchain
	lockData []byte // the tests' own lock, instead of the embedded one
}

// aiToolchainNames are the toolchains `sky ai provision --train` knows.
var aiToolchainNames = []string{"mlx", "unsloth", "fake"}

// aiToolchainFor is the toolchain called name on this host. Mlx has a lock for
// Apple silicon, Unsloth for Linux x86_64 with CUDA 13; Fake (the standard
// library only) runs wherever a pinned CPython does.
func aiToolchainFor(name string) (aiToolchainDef, error) {
	host := runtime.GOOS + "/" + runtime.GOARCH
	switch name {
	case "mlx":
		if host != "darwin/arm64" {
			return aiToolchainDef{}, aiFailf(aiNoAccelerator, "the mlx toolchain runs on Apple silicon, and this host is %s", host).with("wanted", "apple-silicon")
		}
		return aiToolchainDef{name: "mlx", lock: "ai_train_assets/locks/mlx-darwin-arm64.txt"}, nil
	case "unsloth":
		if host != "linux/amd64" {
			return aiToolchainDef{}, aiFailf(aiNoAccelerator, "the unsloth toolchain runs on Linux x86_64 with an NVIDIA GPU, and this host is %s", host).with("wanted", "cuda")
		}
		return aiToolchainDef{name: "unsloth", lock: "ai_train_assets/locks/unsloth-linux-x86_64-cuda13.txt"}, nil
	case "fake":
		if _, ok := aiHostTool(aiPythonAssets); !ok {
			return aiToolchainDef{}, aiFailf(aiNotProvisioned, "there is no pinned CPython for %s", host).with("what", "python").with("command", "train on darwin/arm64, linux/amd64 or linux/arm64")
		}
		return aiToolchainDef{name: "fake"}, nil
	}
	return aiToolchainDef{}, aiFailf(aiInvalidSetting, "toolchain %q: use mlx, unsloth or fake", name).with("setting", "toolchain")
}

func aiHostTool(assets []aiToolAsset) (aiToolAsset, bool) {
	for _, a := range assets {
		if a.goos == runtime.GOOS && a.goarch == runtime.GOARCH {
			return a, true
		}
	}
	return aiToolAsset{}, false
}

// aiDriverFiles are the driver's files, path → bytes (relative to driver/).
func aiDriverFiles() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(aiTrainAssets, "ai_train_assets/driver", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := aiTrainAssets.ReadFile(p)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(p, "ai_train_assets/driver/")] = b
		return nil
	})
	return out, err
}

// aiDriverDigest is the SHA-256 of the driver (every file's path and bytes).
func aiDriverDigest() string {
	files, _ := aiDriverFiles()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(files[n]))
		h.Write(files[n])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (d aiToolchainDef) lockBytes() []byte {
	if d.lockData != nil {
		return d.lockData
	}
	if d.lock == "" {
		return nil
	}
	b, _ := aiTrainAssets.ReadFile(d.lock)
	return b
}

// id is the toolchain's identity: every pinned input, hashed.
func (d aiToolchainDef) id() string {
	h := sha256.New()
	py, _ := aiHostTool(aiPythonAssets)
	uv, _ := aiHostTool(aiUvAssets)
	fmt.Fprintf(h, "toolchain %s\nlock %x\npython %s %s\nuv %s %s\ndriver %s\n",
		d.name, sha256.Sum256(d.lockBytes()), py.file, py.sha256, uv.file, uv.sha256, aiDriverDigest())
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (d aiToolchainDef) dir() string {
	return filepath.Join(aiHome(), "toolchains", d.name, d.id())
}

// aiLockPin is one `name==version` requirement of a lock and its hashes.
type aiLockPin struct {
	name, version string
	hashes        []string
}

var aiLockReqRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)==([^\s;\\]+)`)

// aiParseLock reads a `uv pip compile --generate-hashes` lock.
func aiParseLock(b []byte) ([]aiLockPin, error) {
	var pins []aiLockPin
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "--hash=") {
			if len(pins) == 0 {
				return nil, fmt.Errorf("a hash line before any requirement")
			}
			h := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, "--hash=")), "\\")
			pins[len(pins)-1].hashes = append(pins[len(pins)-1].hashes, strings.TrimSpace(h))
			continue
		}
		m := aiLockReqRe.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("lock line %q is not a pinned requirement", line)
		}
		pins = append(pins, aiLockPin{name: aiNormName(m[1]), version: m[2]})
	}
	return pins, sc.Err()
}

// aiNormName is a package name in its normalised form (PEP 503).
func aiNormName(s string) string {
	s = strings.ToLower(s)
	return regexp.MustCompile(`[-_.]+`).ReplaceAllString(s, "-")
}

// aiToolchainInfo is a provisioned toolchain.
type aiToolchainInfo struct {
	Name      string            `json:"name"`
	ID        string            `json:"id"`
	Python    string            `json:"python"`
	DriverDir string            `json:"driverDir"`
	Dir       string            `json:"dir"`
	Versions  map[string]string `json:"versions"`
	Packages  int               `json:"packages"`
	CreatedMs int64             `json:"createdMs"`
}

func (t aiToolchainInfo) value() map[string]any {
	keys := make([]string, 0, len(t.Versions))
	for k := range t.Versions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var versions []any
	for _, k := range keys {
		versions = append(versions, map[string]any{"name": k, "version": t.Versions[k]})
	}
	if versions == nil {
		versions = []any{}
	}
	return map[string]any{
		"name": t.Name, "id": t.ID, "python": t.Python, "driverDir": t.DriverDir,
		"dir": t.Dir, "versions": versions, "packages": t.Packages, "installed": true,
	}
}

func aiToolchainPython(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "venv", "Scripts", "python.exe")
	}
	return filepath.Join(dir, "venv", "bin", "python")
}

// aiInstalledToolchain reads a complete toolchain (its ok.json).
func aiInstalledToolchain(d aiToolchainDef) (aiToolchainInfo, bool) {
	dir := d.dir()
	b, err := os.ReadFile(filepath.Join(dir, "ok.json"))
	if err != nil {
		return aiToolchainInfo{}, false
	}
	var t aiToolchainInfo
	if json.Unmarshal(b, &t) != nil || t.ID != d.id() {
		return aiToolchainInfo{}, false
	}
	t.Dir, t.Python, t.DriverDir = dir, aiToolchainPython(dir), filepath.Join(dir, "driver")
	if !fileExists(t.Python) {
		return aiToolchainInfo{}, false
	}
	return t, true
}

// aiToolchainEnv is the environment of uv and of a self-check: the engine's
// allow-list plus the proxy and certificate settings a download needs. A
// PIP_*, UV_* or credential variable of the user's never reaches it.
func aiToolchainEnv(home string) []string {
	env := aiEngineEnv()
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env,
		"UV_NO_CONFIG=1",
		"UV_PYTHON_DOWNLOADS=never",
		"UV_CACHE_DIR="+filepath.Join(home, "cache", "uv"),
		"PYTHONNOUSERSITE=1",
		"PYTHONDONTWRITEBYTECODE=1",
	)
}

// aiEnsureToolchain returns the toolchain called name, installing it when
// fetching is allowed.
func aiEnsureToolchain(ctx context.Context, name string) (aiToolchainInfo, error) {
	d, err := aiToolchainFor(name)
	if err != nil {
		return aiToolchainInfo{}, err
	}
	if t, ok := aiInstalledToolchain(d); ok {
		return t, nil
	}
	allowed, err := aiFetchAllowed()
	if err != nil {
		return aiToolchainInfo{}, err
	}
	if !allowed {
		return aiToolchainInfo{}, aiFailf(aiNotProvisioned,
			"the %s training toolchain (%s) is not installed and fetching is off (%s); run `sky ai provision --train %s` on this host first",
			name, d.id(), skyEnvName("AI_FETCH"), name).with("what", name+" toolchain").with("command", "sky ai provision --train "+name)
	}
	return aiInstallToolchain(ctx, d)
}

func aiToolFail(tool string, format string, args ...any) *aiFailure {
	msg := fmt.Sprintf(format, args...)
	return aiFailf(aiToolchainFailed, "%s: %s", tool, msg).with("tool", tool).with("logTail", aiTail(msg, 1500))
}

// aiEnsureTool downloads, verifies and unpacks one pinned tool archive into
// final (staging, then rename). check proves the unpacked tool runs.
func aiEnsureTool(ctx context.Context, tool string, a aiToolAsset, url, final, exe string, check func(string) error) (string, error) {
	bin := filepath.Join(final, exe)
	if fileExists(bin) {
		return bin, nil
	}
	archive := filepath.Join(aiHome(), "downloads", a.file)
	if _, err := aiDownload(ctx, url, archive, a.sha256); err != nil {
		return "", err
	}
	defer os.Remove(archive)
	parent := filepath.Dir(final)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", aiToolFail(tool, "cannot create %s: %v", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, "staging-")
	if err != nil {
		return "", aiToolFail(tool, "cannot create a staging directory: %v", err)
	}
	defer os.RemoveAll(tmp)
	f, err := os.Open(archive)
	if err != nil {
		return "", aiToolFail(tool, "cannot open %s: %v", archive, err)
	}
	err = extractTarGz(f, tmp)
	f.Close()
	if err != nil {
		return "", aiToolFail(tool, "cannot extract %s: %v", a.file, err)
	}
	root := tmp
	if entries, err := os.ReadDir(tmp); err == nil && len(entries) == 1 && entries[0].IsDir() {
		root = filepath.Join(tmp, entries[0].Name())
	}
	if !fileExists(filepath.Join(root, exe)) {
		return "", aiToolFail(tool, "%s has no %s", a.file, exe)
	}
	if err := check(filepath.Join(root, exe)); err != nil {
		return "", err
	}
	if err := os.Rename(root, final); err != nil && !fileExists(bin) {
		return "", aiToolFail(tool, "cannot install %s: %v", final, err)
	}
	return bin, nil
}

func aiRunTool(ctx context.Context, tool string, env []string, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), aiToolFail(tool, "%s %s failed: %v: %s", filepath.Base(name), strings.Join(args, " "), err, aiTail(string(out), 1200))
	}
	return string(out), nil
}

func aiEnsureUv(ctx context.Context, env []string) (string, error) {
	a, ok := aiHostTool(aiUvAssets)
	if !ok {
		return "", aiFailf(aiNotProvisioned, "there is no pinned uv for %s/%s", runtime.GOOS, runtime.GOARCH).with("what", "uv").with("command", "train on darwin/arm64, linux/amd64 or linux/arm64")
	}
	final := filepath.Join(aiHome(), "tools", "uv", aiUvVersion)
	return aiEnsureTool(ctx, "uv", a, aiUvReleaseURL(a.file), final, aiExe("uv"), func(bin string) error {
		out, err := aiRunTool(ctx, "uv", env, "", bin, "--version")
		if err != nil {
			return err
		}
		if !strings.Contains(out, aiUvVersion) {
			return aiToolFail("uv", "uv --version printed %q, expected %s", strings.TrimSpace(out), aiUvVersion)
		}
		return nil
	})
}

func aiEnsurePython(ctx context.Context, env []string) (string, error) {
	a, ok := aiHostTool(aiPythonAssets)
	if !ok {
		return "", aiFailf(aiNotProvisioned, "there is no pinned CPython for %s/%s", runtime.GOOS, runtime.GOARCH).with("what", "python").with("command", "train on darwin/arm64, linux/amd64 or linux/arm64")
	}
	final := filepath.Join(aiHome(), "python", aiPythonTag+"-"+a.target)
	exe := filepath.Join("bin", "python3")
	if runtime.GOOS == "windows" {
		exe = "python.exe"
	}
	return aiEnsureTool(ctx, "python", a, aiPythonReleaseURL(a.file), final, exe, func(bin string) error {
		out, err := aiRunTool(ctx, "python", env, "", bin, "-I", "-B", "-c", "import sys; print('python', sys.version.split()[0])")
		if err != nil {
			return err
		}
		if !strings.Contains(out, aiPythonVersion) {
			return aiToolFail("python", "the pinned CPython printed %q, expected %s", strings.TrimSpace(out), aiPythonVersion)
		}
		return nil
	})
}

// aiInstallToolchain builds a toolchain in a staging directory and renames it
// into place. Two programs provisioning at once take turns on a lock file;
// the second finds the first's toolchain and returns it.
func aiInstallToolchain(ctx context.Context, d aiToolchainDef) (aiToolchainInfo, error) {
	home := aiHome()
	parent := filepath.Join(home, "toolchains", d.name)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return aiToolchainInfo{}, aiToolFail(d.name, "cannot create %s: %v", parent, err)
	}
	unlock, err := aiLockFile(filepath.Join(parent, "install.lock"), 60*time.Minute)
	if err != nil {
		return aiToolchainInfo{}, err
	}
	defer unlock()
	if t, ok := aiInstalledToolchain(d); ok {
		return t, nil
	}
	env := aiToolchainEnv(home)
	uv, err := aiEnsureUv(ctx, env)
	if err != nil {
		return aiToolchainInfo{}, err
	}
	python, err := aiEnsurePython(ctx, env)
	if err != nil {
		return aiToolchainInfo{}, err
	}
	staging, err := os.MkdirTemp(parent, "staging-")
	if err != nil {
		return aiToolchainInfo{}, aiToolFail(d.name, "cannot create a staging directory: %v", err)
	}
	defer os.RemoveAll(staging)
	venv := filepath.Join(staging, "venv")
	if _, err := aiRunTool(ctx, "uv", env, staging, uv, "venv", "--no-config", "--relocatable", "--python", python, venv); err != nil {
		return aiToolchainInfo{}, err
	}
	py := aiToolchainPython(staging)
	pins, err := aiParseLock(d.lockBytes())
	if err != nil {
		return aiToolchainInfo{}, aiToolFail(d.name, "the embedded lock %s does not parse: %v", d.lock, err)
	}
	if len(pins) > 0 {
		lock := filepath.Join(staging, "lock.txt")
		if err := os.WriteFile(lock, d.lockBytes(), 0o644); err != nil {
			return aiToolchainInfo{}, aiToolFail(d.name, "cannot write the lock: %v", err)
		}
		if _, err := aiRunTool(ctx, "uv", env, staging, uv, "pip", "sync", "--no-config", "--require-hashes",
			"--only-binary", ":all:", "--index-url", aiPyPIIndex, "--python", py, lock); err != nil {
			return aiToolchainInfo{}, err
		}
		installed := aiInstalledDists(venv)
		for _, p := range pins {
			if got := installed[p.name]; got != p.version {
				return aiToolchainInfo{}, aiToolFail(d.name, "the lock pins %s %s and the environment has %q", p.name, p.version, got)
			}
		}
	}
	files, err := aiDriverFiles()
	if err != nil {
		return aiToolchainInfo{}, aiToolFail(d.name, "cannot read the embedded driver: %v", err)
	}
	for rel, b := range files {
		p := filepath.Join(staging, "driver", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return aiToolchainInfo{}, aiToolFail(d.name, "cannot write the driver: %v", err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return aiToolchainInfo{}, aiToolFail(d.name, "cannot write the driver: %v", err)
		}
	}
	out, err := aiRunTool(ctx, "self-check", env, staging, py, "-I", "-B", filepath.Join(staging, "driver", "sky_train.py"), "--selfcheck", d.name)
	if err != nil {
		return aiToolchainInfo{}, err
	}
	var hello struct {
		Type     string            `json:"type"`
		Versions map[string]string `json:"versions"`
	}
	line := strings.TrimSpace(out)
	if i := strings.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:]
	}
	if json.Unmarshal([]byte(line), &hello) != nil || hello.Type != "hello" {
		return aiToolchainInfo{}, aiToolFail("self-check", "the driver's self-check printed no hello line: %s", aiTail(out, 800))
	}
	for k, v := range hello.Versions {
		for _, p := range pins {
			if p.name == aiNormName(k) && p.version != v {
				return aiToolchainInfo{}, aiToolFail("self-check", "the driver imports %s %s and the lock pins %s", k, v, p.version)
			}
		}
	}
	sbom := map[string]any{"toolchain": d.name, "id": d.id(), "python": aiPythonVersion + "+" + aiPythonTag, "uv": aiUvVersion, "driver": aiDriverDigest()}
	var pkgs []any
	for _, p := range pins {
		pkgs = append(pkgs, map[string]any{"name": p.name, "version": p.version, "hashes": p.hashes})
	}
	sbom["packages"] = pkgs
	sb, _ := json.MarshalIndent(sbom, "", "  ")
	if err := os.WriteFile(filepath.Join(staging, "sbom.json"), sb, 0o644); err != nil {
		return aiToolchainInfo{}, aiToolFail(d.name, "cannot write sbom.json: %v", err)
	}
	info := aiToolchainInfo{Name: d.name, ID: d.id(), Versions: hello.Versions, Packages: len(pins), CreatedMs: time.Now().UnixMilli()}
	ok, _ := json.MarshalIndent(info, "", "  ")
	if err := os.WriteFile(filepath.Join(staging, "ok.json"), ok, 0o644); err != nil {
		return aiToolchainInfo{}, aiToolFail(d.name, "cannot write ok.json: %v", err)
	}
	final := d.dir()
	if err := os.Rename(staging, final); err != nil && !fileExists(filepath.Join(final, "ok.json")) {
		return aiToolchainInfo{}, aiToolFail(d.name, "cannot install the toolchain at %s: %v", final, err)
	}
	t, okInst := aiInstalledToolchain(d)
	if !okInst {
		return aiToolchainInfo{}, aiToolFail(d.name, "the toolchain at %s does not read back", final)
	}
	return t, nil
}

// aiInstalledDists reads name → version from a virtual environment's
// *.dist-info directories.
func aiInstalledDists(venv string) map[string]string {
	out := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(venv, "lib", "python*", "site-packages", "*.dist-info"))
	win, _ := filepath.Glob(filepath.Join(venv, "Lib", "site-packages", "*.dist-info"))
	for _, m := range append(matches, win...) {
		base := strings.TrimSuffix(filepath.Base(m), ".dist-info")
		i := strings.LastIndexByte(base, '-')
		if i <= 0 {
			continue
		}
		out[aiNormName(base[:i])] = base[i+1:]
	}
	return out
}

// ─── kernels ────────────────────────────────────────────────────────────

// aiTestToolchain is the toolchain test mode reports: no install, no network.
func aiTestToolchain(name string) map[string]any {
	return map[string]any{"name": name, "id": "test-mode", "python": "", "driverDir": "", "dir": "",
		"versions": []any{}, "packages": 0, "installed": true}
}

// Ai_toolchainEnsure : String -> Task Error Toolchain
func Ai_toolchainEnsure(nameArg any) any {
	name := strings.ToLower(strings.TrimSpace(AsString(nameArg)))
	return func() any {
		if testModeActive() {
			if _, err := aiToolchainFor(name); err != nil && name != "fake" {
				return aiTaskErr(err)
			}
			return Ok[any, any](aiTestToolchain(name))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
		defer cancel()
		t, err := aiEnsureToolchain(ctx, name)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](t.value())
	}
}

// Ai_toolchainStatus : () -> Task Error (List Toolchain) — every toolchain this
// host can run, installed or not.
func Ai_toolchainStatus(_ any) any {
	return func() any {
		out := []any{}
		for _, name := range aiToolchainNames {
			d, err := aiToolchainFor(name)
			if err != nil {
				continue
			}
			if t, ok := aiInstalledToolchain(d); ok {
				out = append(out, t.value())
				continue
			}
			out = append(out, map[string]any{"name": name, "id": d.id(), "python": "", "driverDir": "", "dir": d.dir(),
				"versions": []any{}, "packages": 0, "installed": false})
		}
		return Ok[any, any](out)
	}
}

// ─── base snapshots ─────────────────────────────────────────────────────

// Ai_trainBaseEnsure : String -> Task Error String
//
// A pinned base snapshot `{repo, revision, files: [{path, sha256}]}` as a
// local directory: bases/<repo>@<revision>/, each file downloaded at the
// pinned commit and verified by its digest (ChecksumMismatch otherwise), the
// directory renamed into place when every file is there. The trainer reads
// the weights from it offline.
func Ai_trainBaseEnsure(snapArg any) any {
	snap := AsString(snapArg)
	return func() any {
		dir, err := aiTrainBaseEnsure(snap)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](dir)
	}
}

func aiTrainBaseEnsure(snap string) (string, error) {
	var s struct {
		Repo     string `json:"repo"`
		Revision string `json:"revision"`
		Files    []struct {
			Path   string `json:"path"`
			Sha256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(snap), &s); err != nil || s.Repo == "" || s.Revision == "" || len(s.Files) == 0 {
		return "", aiFailf(aiInvalidSetting, "the base snapshot is not {repo, revision, files}").with("setting", "base")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(s.Repo) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(s.Revision) {
		return "", aiFailf(aiInvalidSetting, "base snapshot %s@%s: write <org>/<repo> and the 40-digit commit", s.Repo, s.Revision).with("setting", "base")
	}
	for _, f := range s.Files {
		if !aiValidSha256(f.Sha256) || f.Path == "" || strings.Contains(f.Path, "..") || strings.HasPrefix(f.Path, "/") {
			return "", aiFailf(aiInvalidSetting, "base snapshot %s: file %q needs a relative path and a sha256", s.Repo, f.Path).with("setting", "base")
		}
	}
	if testModeActive() {
		return filepath.Join(aiHome(), "bases", "test-mode"), nil
	}
	final := filepath.Join(aiHome(), "bases", strings.ReplaceAll(s.Repo, "/", "--")+"@"+s.Revision)
	complete := func(dir string) bool {
		for _, f := range s.Files {
			if !fileExists(filepath.Join(dir, filepath.FromSlash(f.Path))) {
				return false
			}
		}
		return true
	}
	if complete(final) {
		return final, nil
	}
	allowed, err := aiFetchAllowed()
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", aiFailf(aiNotProvisioned, "the base %s@%s is not in %s and fetching is off (%s); run `sky ai provision --train <backend> --base dev` on this host first",
			s.Repo, s.Revision[:12], filepath.Dir(final), skyEnvName("AI_FETCH")).with("what", s.Repo+"@"+s.Revision[:12]).with("command", "sky ai provision --train mlx --base dev")
	}
	parent := filepath.Dir(final)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", aiTrainFail("cannot create %s: %v", parent, err)
	}
	unlock, err := aiLockFile(final+".lock", 60*time.Minute)
	if err != nil {
		return "", err
	}
	defer unlock()
	if complete(final) {
		return final, nil
	}
	staging := final + ".staging"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	for _, f := range s.Files {
		dest := filepath.Join(staging, filepath.FromSlash(f.Path))
		if fileExists(dest) {
			if got, _ := aiFileSha256(dest); got == f.Sha256 {
				continue
			}
		}
		if _, err := aiDownload(ctx, aiHuggingFaceRevisionURL(s.Repo, s.Revision, f.Path), dest, f.Sha256); err != nil {
			return "", err
		}
	}
	os.RemoveAll(final)
	if err := os.Rename(staging, final); err != nil {
		return "", aiTrainFail("cannot install the base at %s: %v", final, err)
	}
	return final, nil
}
