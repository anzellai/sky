//go:build !js

package rt

// ai_train_base.go — a training base named by a reference instead of a
// hand-written snapshot: `hf:<org>/<repo>@<commit>` or `file:<dir>`.
//
//	Ai_trainBaseReference : String -> Task Error String   validate, normalise
//	Ai_trainBasePin       : String -> Task Error String   fill a snapshot's file list
//
// A Hub base is pinned by its commit: the commit's tree names every file, an
// LFS file by the SHA-256 of its content and any other file by its git blob
// id, so the file list read at that commit cannot change. Pinning reads the
// list once (LFS digests as listed, every other file fetched and checked
// against its blob id before its SHA-256 is taken) and keeps it below the AI
// home, so a host with fetching off still pins a base it provisioned. A local
// base is pinned by hashing its directory: a later change to a file is a
// ChecksumMismatch, never a run that trains on other weights.

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	aiBaseRepoRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	aiBaseRevisionRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// aiBaseFileScheme marks a snapshot whose weights are a local directory.
const aiBaseFileScheme = "file:"

// aiHuggingFaceTreeURL lists a repository's files at a commit. A variable for
// the tests.
var aiHuggingFaceTreeURL = func(repo, revision string) string {
	return "https://huggingface.co/api/models/" + repo + "/tree/" + revision + "?recursive=true"
}

// aiBaseSnapshot is the snapshot JSON Std.Ai.Train writes (`snapshotJson`).
type aiBaseSnapshot struct {
	Repo     string               `json:"repo"`
	Revision string               `json:"revision"`
	Files    []aiBaseSnapshotFile `json:"files"`
}

type aiBaseSnapshotFile struct {
	Path   string `json:"path"`
	Sha256 string `json:"sha256"`
}

// aiBaseRefError is the one message every malformed base reference gets.
func aiBaseRefError(ref, why string) error {
	return aiFailf(aiInvalidSetting, "the base %q: %s; write hf:<org>/<repo>@<40-digit commit> or file:<directory>", ref, why).
		with("setting", "base").with("reason", why)
}

// aiParseBaseReference validates a base reference and returns its snapshot
// with an empty file list (pinning fills it). A `file:` directory is made
// absolute, so a run resumed from another directory reads the same weights.
func aiParseBaseReference(ref string) (aiBaseSnapshot, string, error) {
	ref = strings.TrimSpace(ref)
	switch {
	case strings.HasPrefix(ref, "hf:"):
		body := strings.TrimPrefix(ref, "hf:")
		repo, rev, ok := strings.Cut(body, "@")
		if !ok || rev == "" {
			return aiBaseSnapshot{}, "", aiBaseRefError(ref, "a Hub base needs its commit (@<40-digit commit>); a branch or tag can move")
		}
		if !aiBaseRepoRe.MatchString(repo) {
			return aiBaseSnapshot{}, "", aiBaseRefError(ref, "the repository is not <org>/<repo>")
		}
		if !aiBaseRevisionRe.MatchString(rev) {
			return aiBaseSnapshot{}, "", aiBaseRefError(ref, "the revision is not a 40-digit lower-case commit; a branch or tag can move")
		}
		return aiBaseSnapshot{Repo: repo, Revision: rev, Files: []aiBaseSnapshotFile{}}, repo + "@" + rev, nil
	case strings.HasPrefix(ref, aiBaseFileScheme):
		dir := strings.TrimPrefix(ref, aiBaseFileScheme)
		if dir == "" {
			return aiBaseSnapshot{}, "", aiBaseRefError(ref, "the directory is empty")
		}
		if strings.Contains(dir, "#") {
			return aiBaseSnapshot{}, "", aiBaseRefError(ref, "a base is a directory of weights, not a pinned file (#sha256= is for a GGUF model)")
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return aiBaseSnapshot{}, "", aiBaseRefError(ref, "the directory cannot be made absolute")
		}
		return aiBaseSnapshot{Repo: aiBaseFileScheme + abs, Files: []aiBaseSnapshotFile{}}, aiBaseFileScheme + abs, nil
	}
	return aiBaseSnapshot{}, "", aiBaseRefError(ref, "it is neither an hf: nor a file: reference")
}

// Ai_trainBaseReference : String -> Task Error String
//
// `{"id": ..., "snapshot": {repo, revision, files: []}}` for a valid base
// reference, InvalidSetting (setting "base") otherwise. No I/O.
func Ai_trainBaseReference(refArg any) any {
	ref := AsString(refArg)
	return func() any {
		snap, id, err := aiParseBaseReference(ref)
		if err != nil {
			return aiTaskErr(err)
		}
		out, _ := json.Marshal(map[string]any{"id": id, "snapshot": snap})
		return Ok[any, any](string(out))
	}
}

// Ai_trainBasePin : String -> Task Error String
//
// The snapshot with its file list filled: a snapshot that already lists its
// files is returned as it is.
func Ai_trainBasePin(snapArg any) any {
	raw := AsString(snapArg)
	return func() any {
		var s aiBaseSnapshot
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return aiTaskErr(aiFailf(aiInvalidSetting, "the base snapshot is not {repo, revision, files}").with("setting", "base"))
		}
		pinned, err := aiPinBaseSnapshot(s)
		if err != nil {
			return aiTaskErr(err)
		}
		out, _ := json.Marshal(pinned)
		return Ok[any, any](string(out))
	}
}

func aiPinBaseSnapshot(s aiBaseSnapshot) (aiBaseSnapshot, error) {
	if len(s.Files) > 0 {
		return s, nil
	}
	if strings.HasPrefix(s.Repo, aiBaseFileScheme) {
		return aiPinLocalBase(s)
	}
	if !aiBaseRepoRe.MatchString(s.Repo) || !aiBaseRevisionRe.MatchString(s.Revision) {
		return s, aiFailf(aiInvalidSetting, "base snapshot %s@%s: write <org>/<repo> and the 40-digit commit", s.Repo, s.Revision).with("setting", "base")
	}
	return aiPinHubBase(s)
}

// aiBaseWeightFile reports whether a file is one a trainer reads: the
// safetensors weights and their index, the config and tokenizer files. A
// pickle (`.bin`, `.pt`, `.pth`, `.pkl`) is never one: loading it runs code.
func aiBaseWeightFile(p string) bool {
	name := strings.ToLower(filepath.Base(p))
	if strings.HasPrefix(name, ".") {
		return false
	}
	for _, ext := range []string{".safetensors", ".json", ".model", ".tiktoken", ".jinja"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return name == "merges.txt" || name == "vocab.txt"
}

// aiCheckBaseFiles refuses a list a trainer could not train on.
func aiCheckBaseFiles(what string, files []aiBaseSnapshotFile) error {
	var weights, config bool
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.Path), ".safetensors") {
			weights = true
		}
		if f.Path == "config.json" {
			config = true
		}
	}
	if !weights || !config {
		return aiFailf(aiInvalidSetting, "the base %s has no safetensors weights and config.json at its top level; a trainer cannot load it (pickled weights are never loaded)", what).
			with("setting", "base").with("reason", "no safetensors weights or no config.json")
	}
	return nil
}

func aiPinLocalBase(s aiBaseSnapshot) (aiBaseSnapshot, error) {
	dir := strings.TrimPrefix(s.Repo, aiBaseFileScheme)
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return s, aiFailf(aiNotProvisioned, "the base directory %s does not exist or is not a directory", dir).
			with("what", dir).with("command", "check the --base file:<directory> path")
	}
	var files []aiBaseSnapshotFile
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if info.IsDir() {
			if path != dir && strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !aiBaseWeightFile(rel) {
			return nil
		}
		// A symbolic link (a Hugging Face cache snapshot is one per file)
		// is hashed through: the digest is of the bytes a trainer reads.
		sum, err := aiFileSha256(path)
		if err != nil {
			return err
		}
		files = append(files, aiBaseSnapshotFile{Path: filepath.ToSlash(rel), Sha256: sum})
		return nil
	})
	if err != nil {
		return s, aiFailf(aiNotProvisioned, "cannot read the base directory %s: %v", dir, err).with("what", dir).with("command", "check the base directory")
	}
	if err := aiCheckBaseFiles(dir, files); err != nil {
		return s, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s %s\n", f.Sha256, f.Path)
	}
	s.Revision = hex.EncodeToString(h.Sum(nil))
	s.Files = files
	return s, nil
}

// aiHubTreeEntry is one entry of the Hub's tree listing.
type aiHubTreeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Oid  string `json:"oid"`
	Size int64  `json:"size"`
	Lfs  *struct {
		Oid string `json:"oid"`
	} `json:"lfs"`
}

func aiHubPinCache(s aiBaseSnapshot) string {
	return filepath.Join(aiHome(), "bases", strings.ReplaceAll(s.Repo, "/", "--")+"@"+s.Revision+".files.json")
}

func aiPinHubBase(s aiBaseSnapshot) (aiBaseSnapshot, error) {
	cache := aiHubPinCache(s)
	if b, err := os.ReadFile(cache); err == nil {
		var files []aiBaseSnapshotFile
		if json.Unmarshal(b, &files) == nil && len(files) > 0 && aiCheckBaseFiles(s.Repo, files) == nil {
			s.Files = files
			return s, nil
		}
	}
	allowed, err := aiFetchAllowed()
	if err != nil {
		return s, err
	}
	if !allowed {
		return s, aiFailf(aiNotProvisioned, "the file list of the base %s@%s is not in %s and fetching is off (%s); run `sky ai provision --train <backend> --base hf:%s@%s` on this host first",
			s.Repo, s.Revision[:12], filepath.Dir(cache), skyEnvName("AI_FETCH"), s.Repo, s.Revision).
			with("what", s.Repo+"@"+s.Revision[:12]).with("command", "sky ai provision --train mlx --base hf:"+s.Repo+"@"+s.Revision)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	entries, err := aiHubTree(ctx, s.Repo, s.Revision)
	if err != nil {
		return s, err
	}
	var files []aiBaseSnapshotFile
	for _, e := range entries {
		if e.Type != "file" || !aiBaseWeightFile(e.Path) || strings.Contains(e.Path, "..") || strings.HasPrefix(e.Path, "/") {
			continue
		}
		if e.Lfs != nil {
			sum := strings.ToLower(e.Lfs.Oid)
			if !aiValidSha256(sum) {
				return s, aiFailf(aiUnreachable, "the Hub listed %s in %s with an LFS id that is not a SHA-256", e.Path, s.Repo).with("target", s.Repo)
			}
			files = append(files, aiBaseSnapshotFile{Path: e.Path, Sha256: sum})
			continue
		}
		sum, err := aiHubBlobSha256(ctx, s.Repo, s.Revision, e)
		if err != nil {
			return s, err
		}
		files = append(files, aiBaseSnapshotFile{Path: e.Path, Sha256: sum})
	}
	if err := aiCheckBaseFiles(s.Repo+"@"+s.Revision[:12], files); err != nil {
		return s, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if b, err := json.Marshal(files); err == nil {
		if os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
			tmp := cache + ".tmp"
			if os.WriteFile(tmp, b, 0o644) == nil {
				os.Rename(tmp, cache)
			}
		}
	}
	s.Files = files
	return s, nil
}

// aiHubTree reads the tree listing, following the `Link: rel="next"` pages.
func aiHubTree(ctx context.Context, repo, rev string) ([]aiHubTreeEntry, error) {
	var all []aiHubTreeEntry
	url := aiHuggingFaceTreeURL(repo, rev)
	for page := 0; url != "" && page < 50; page++ {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, aiFailf(aiInvalidSetting, "bad Hub URL %q: %v", url, err).with("setting", "base")
		}
		resp, err := aiHTTPClient.Do(req)
		if err != nil {
			return nil, aiFailf(aiUnreachable, "list %s@%s: %v", repo, rev[:12], err).with("target", aiRedactURL(url))
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, aiFailf(aiNotRegistered, "the Hub has no repository %s at commit %s (or it is gated or private)", repo, rev).
				with("ref", repo+"@"+rev)
		}
		if resp.StatusCode != http.StatusOK || err != nil {
			return nil, aiFailf(aiUnreachable, "list %s@%s: HTTP %d", repo, rev[:12], resp.StatusCode).with("target", aiRedactURL(url))
		}
		var entries []aiHubTreeEntry
		if err := json.Unmarshal(body, &entries); err != nil {
			return nil, aiFailf(aiUnreachable, "list %s@%s: the Hub's answer is not a file list", repo, rev[:12]).with("target", aiRedactURL(url))
		}
		all = append(all, entries...)
		url = aiNextLink(resp.Header.Get("Link"))
	}
	return all, nil
}

// aiNextLink reads the `rel="next"` URL of a Link header.
func aiNextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		u, params, ok := strings.Cut(part, ";")
		if ok && strings.Contains(params, `rel="next"`) {
			return strings.Trim(strings.TrimSpace(u), "<>")
		}
	}
	return ""
}

// aiHubBlobSha256 fetches a small file that is not in LFS, checks it against
// the git blob id the commit's tree names, and returns its SHA-256.
func aiHubBlobSha256(ctx context.Context, repo, rev string, e aiHubTreeEntry) (string, error) {
	const limit = 64 << 20
	if e.Size > limit {
		return "", aiFailf(aiInvalidSetting, "%s in %s is %d bytes and not in LFS; Sky pins files over 64 MiB by their LFS digest only", e.Path, repo, e.Size).with("setting", "base")
	}
	url := aiHuggingFaceRevisionURL(repo, rev, e.Path)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", aiFailf(aiInvalidSetting, "bad Hub URL %q: %v", url, err).with("setting", "base")
	}
	resp, err := aiHTTPClient.Do(req)
	if err != nil {
		return "", aiFailf(aiUnreachable, "download %s: %v", url, err).with("target", aiRedactURL(url))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil {
		return "", aiFailf(aiUnreachable, "download %s: HTTP %d", url, resp.StatusCode).with("target", aiRedactURL(url))
	}
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(body))
	h.Write(body)
	if got := hex.EncodeToString(h.Sum(nil)); got != strings.ToLower(e.Oid) {
		return "", aiFailf(aiChecksumMismatch, "%s: the commit names git blob %s, the downloaded bytes are blob %s; nothing was pinned", url, e.Oid, got).
			with("expected", e.Oid).with("actual", got)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// aiLocalBaseEnsure verifies a pinned local base in place and returns its
// directory. A file that no longer hashes to its pin is ChecksumMismatch.
func aiLocalBaseEnsure(s aiBaseSnapshot) (string, error) {
	dir := strings.TrimPrefix(s.Repo, aiBaseFileScheme)
	if !filepath.IsAbs(dir) {
		return "", aiFailf(aiInvalidSetting, "the local base %s is not an absolute directory", dir).with("setting", "base")
	}
	for _, f := range s.Files {
		got, err := aiFileSha256(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil {
			return "", aiFailf(aiNotProvisioned, "the base file %s: %v", filepath.Join(dir, f.Path), err).with("what", dir).with("command", "check the base directory")
		}
		if got != f.Sha256 {
			return "", aiFailf(aiChecksumMismatch, "the base file %s: pinned sha256 %s, the file is sha256 %s; it changed since the job was written and was not trained on",
				filepath.Join(dir, f.Path), f.Sha256, got).with("expected", f.Sha256).with("actual", got)
		}
	}
	return dir, nil
}
