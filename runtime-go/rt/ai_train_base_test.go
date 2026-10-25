//go:build !js

package rt

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const aiTestCommit = "0123456789012345678901234567890123456789"

func sha256Hex(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func gitBlobID(s string) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(s))
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// A base reference is `hf:<org>/<repo>@<40-digit commit>` or
// `file:<directory>`. Anything else, and a Hub base without its commit, is
// InvalidSetting naming the base setting.
func TestAiBaseReferenceValidation(t *testing.T) {
	good := map[string]string{
		"hf:Qwen/Qwen2.5-1.5B-Instruct@" + aiTestCommit: "Qwen/Qwen2.5-1.5B-Instruct@" + aiTestCommit,
	}
	for ref, id := range good {
		snap, got, err := aiParseBaseReference(ref)
		if err != nil || got != id || snap.Revision != aiTestCommit || len(snap.Files) != 0 {
			t.Fatalf("%s: %v %q %+v", ref, err, got, snap)
		}
	}
	dir := t.TempDir()
	snap, id, err := aiParseBaseReference("file:" + dir)
	if err != nil || id != "file:"+dir || snap.Repo != "file:"+dir {
		t.Fatalf("file base: %v %q %+v", err, id, snap)
	}
	for _, bad := range []string{
		"Qwen/Qwen2.5-1.5B-Instruct",
		"hf:Qwen/Qwen2.5-1.5B-Instruct",
		"hf:Qwen/Qwen2.5-1.5B-Instruct@main",
		"hf:Qwen/Qwen2.5-1.5B-Instruct@" + strings.ToUpper(aiTestCommit[:39]) + "A",
		"hf:Qwen@" + aiTestCommit,
		"hf:org/repo/file.gguf@" + aiTestCommit,
		"file:",
		"file:model.gguf#sha256=" + sha256Hex("x"),
		"dev",
	} {
		_, _, err := aiParseBaseReference(bad)
		f, ok := err.(*aiFailure)
		if !ok || f.code != aiInvalidSetting {
			t.Fatalf("%q: want InvalidSetting, got %v", bad, err)
		}
		if s, _ := f.attrValue("setting"); s != "base" {
			t.Fatalf("%q: setting %q", bad, s)
		}
	}
}

// A Hub base is pinned at its commit: an LFS file by the digest its pointer
// carries, any other file fetched and checked against its git blob id. A
// pickle is never listed. The list is kept, so a host with fetching off pins
// the same base again, and the base then downloads through the usual
// SHA-256 checks.
func TestAiHubBaseIsPinnedAtItsCommit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SKY_AI_HOME", home)
	t.Setenv("SKY_AI_FETCH", "on")
	files := map[string]string{
		"config.json":       `{"model_type":"qwen2"}`,
		"tokenizer.json":    `{"version":"1.0"}`,
		"model.safetensors": "weights",
		"pytorch_model.bin": "pickle",
		"README.md":         "# model",
	}
	tamper := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/models/org/model/tree/"+aiTestCommit):
			var out []map[string]any
			for _, p := range []string{"config.json", "tokenizer.json", "model.safetensors", "pytorch_model.bin", "README.md"} {
				e := map[string]any{"type": "file", "path": p, "size": len(files[p]), "oid": gitBlobID(files[p])}
				if strings.HasSuffix(p, ".safetensors") || strings.HasSuffix(p, ".bin") {
					e["lfs"] = map[string]any{"oid": sha256Hex(files[p]), "size": len(files[p])}
				}
				out = append(out, e)
			}
			json.NewEncoder(w).Encode(out)
		case strings.Contains(r.URL.Path, "/resolve/"+aiTestCommit+"/"):
			body := files[filepath.Base(r.URL.Path)]
			if tamper && filepath.Base(r.URL.Path) == "tokenizer.json" {
				body = `{"version":"2.0"}`
			}
			w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	savedTree, savedRes := aiHuggingFaceTreeURL, aiHuggingFaceRevisionURL
	aiHuggingFaceTreeURL = func(repo, rev string) string { return srv.URL + "/api/models/" + repo + "/tree/" + rev }
	aiHuggingFaceRevisionURL = func(repo, rev, f string) string { return srv.URL + "/" + repo + "/resolve/" + rev + "/" + f }
	defer func() { aiHuggingFaceTreeURL, aiHuggingFaceRevisionURL = savedTree, savedRes }()

	snap, _, err := aiParseBaseReference("hf:org/model@" + aiTestCommit)
	if err != nil {
		t.Fatal(err)
	}

	// A non-LFS file whose bytes are not the commit's blob is refused.
	tamper = true
	if _, err := aiPinBaseSnapshot(snap); err == nil || err.(*aiFailure).code != aiChecksumMismatch {
		t.Fatalf("a tampered small file: %v", err)
	}
	tamper = false

	pinned, err := aiPinBaseSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	want := []aiBaseSnapshotFile{
		{"config.json", sha256Hex(files["config.json"])},
		{"model.safetensors", sha256Hex("weights")},
		{"tokenizer.json", sha256Hex(files["tokenizer.json"])},
	}
	if fmt.Sprint(pinned.Files) != fmt.Sprint(want) {
		t.Fatalf("pinned %v, want %v", pinned.Files, want)
	}

	// Fetching off: the kept list pins it again, without the Hub.
	t.Setenv("SKY_AI_FETCH", "off")
	again, err := aiPinBaseSnapshot(snap)
	if err != nil || fmt.Sprint(again.Files) != fmt.Sprint(want) {
		t.Fatalf("pinned again with fetching off: %v %v", err, again.Files)
	}
	os.Remove(aiHubPinCache(snap))
	if _, err := aiPinBaseSnapshot(snap); err == nil || err.(*aiFailure).code != aiNotProvisioned {
		t.Fatalf("no kept list and fetching off: %v", err)
	}

	// The base ensure pins a commit-only snapshot, then downloads it.
	t.Setenv("SKY_AI_FETCH", "on")
	raw, _ := json.Marshal(snap)
	dir, err := aiTrainBaseEnsure(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "model.safetensors")); string(b) != "weights" {
		t.Fatalf("weights %q", b)
	}
	if fileExists(filepath.Join(dir, "pytorch_model.bin")) {
		t.Fatal("a pickle was downloaded")
	}

	// A repository the Hub does not have is NotRegistered.
	other, _, _ := aiParseBaseReference("hf:org/missing@" + aiTestCommit)
	if _, err := aiPinBaseSnapshot(other); err == nil || err.(*aiFailure).code != aiNotRegistered {
		t.Fatalf("a missing repository: %v", err)
	}
}

// A local base is pinned by hashing its directory and verified in place;
// a changed file is ChecksumMismatch, and an unpinned local snapshot is
// refused rather than trained on as it happens to be.
func TestAiLocalBaseIsPinnedAndVerifiedInPlace(t *testing.T) {
	t.Setenv("SKY_AI_HOME", t.TempDir())
	dir := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("config.json", `{"model_type":"qwen2"}`)
	write("model.safetensors", "weights")
	write("pytorch_model.bin", "pickle")
	write(".cache/x.json", "{}")

	snap, _, err := aiParseBaseReference("file:" + dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snap)
	if _, err := aiTrainBaseEnsure(string(raw)); err == nil || err.(*aiFailure).code != aiInvalidSetting {
		t.Fatalf("an unpinned local base: %v", err)
	}
	pinned, err := aiPinBaseSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(pinned.Files) != 2 || !aiValidSha256(pinned.Revision) {
		t.Fatalf("pinned %+v", pinned)
	}
	raw, _ = json.Marshal(pinned)
	got, err := aiTrainBaseEnsure(string(raw))
	if err != nil || got != dir {
		t.Fatalf("ensure: %q %v", got, err)
	}
	write("model.safetensors", "other weights")
	if _, err := aiTrainBaseEnsure(string(raw)); err == nil || err.(*aiFailure).code != aiChecksumMismatch {
		t.Fatalf("a changed weight file: %v", err)
	}

	// A directory with no safetensors weights is not a base.
	empty := t.TempDir()
	os.WriteFile(filepath.Join(empty, "config.json"), []byte("{}"), 0o644)
	e, _, _ := aiParseBaseReference("file:" + empty)
	if _, err := aiPinBaseSnapshot(e); err == nil || err.(*aiFailure).code != aiInvalidSetting {
		t.Fatalf("no weights: %v", err)
	}
	// A directory that does not exist.
	m, _, _ := aiParseBaseReference("file:" + filepath.Join(dir, "missing"))
	if _, err := aiPinBaseSnapshot(m); err == nil || err.(*aiFailure).code != aiNotProvisioned {
		t.Fatalf("a missing directory: %v", err)
	}
}
