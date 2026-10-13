//go:build !js

package rt

// ai_device_desktop.go — Std.Ai.Device in the macOS desktop shell.
//
// The desktop window (webview.go) answers the native-shell protocol in Go
// (nativeShellDispatch), so a device slot on a Mac runs on the managed
// llama-server the server side already supervises (ai_engine.go), with the
// Metal build. The slot's model and adapter are downloaded from the app's
// server into the AI home's content-addressed store (models/sha256, the same
// store Std.Ai.Local uses), checked against their SHA-256; a model the build
// embedded (`--ai-embed`) is read from the app bundle's Resources/sky-ai.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	aiDeskMu       sync.Mutex
	aiDeskProgress = map[string]aiDeviceProgress{}
	// aiDeskClient downloads the slot's files; a variable for the tests.
	aiDeskClient = &http.Client{Timeout: 30 * time.Minute}
)

// aiDeskWireErr is an error as the protocol carries it: a typed AI failure as
// `ai:<json>`, anything else as `failed: <message>`.
func aiDeskWireErr(err error) error {
	var f *aiFailure
	if errors.As(err, &f) {
		return errors.New("ai:" + string(aiDeviceFailureJSON(f)))
	}
	return errors.New("failed: " + err.Error())
}

// aiDeviceDesktopDispatch answers one `sky:ai*` op of the desktop shell.
func aiDeviceDesktopDispatch(op string, p map[string]string) (string, error) {
	switch op {
	case "sky:aiCaps":
		b, _ := json.Marshal(aiDeskCaps())
		return string(b), nil
	case "sky:aiStatus":
		o, err := aiDeskOffer(p["offer"])
		if err != nil {
			return "", aiDeskWireErr(err)
		}
		aiDeskMu.Lock()
		st, ok := aiDeskProgress[aiDeskKey(o)]
		aiDeskMu.Unlock()
		if !ok {
			st = aiDeviceProgress{State: "idle"}
		}
		b, _ := json.Marshal(st)
		return string(b), nil
	case "sky:aiPrepare":
		o, err := aiDeskOffer(p["offer"])
		if err != nil {
			return "", aiDeskWireErr(err)
		}
		if err := aiDeskPrepare(o, p["origin"]); err != nil {
			return "", aiDeskWireErr(err)
		}
		return "ready", nil
	case "sky:aiChat":
		o, err := aiDeskOffer(p["offer"])
		if err != nil {
			return "", aiDeskWireErr(err)
		}
		out, err := aiDeskChat(o, p["path"], p["body"])
		if err != nil {
			return "", aiDeskWireErr(err)
		}
		return out, nil
	case "sky:aiStreamBegin":
		o, err := aiDeskOffer(p["offer"])
		if err != nil {
			return "", aiDeskWireErr(err)
		}
		if err := aiDeskStreamBegin(o, p["path"], p["body"]); err != nil {
			return "", aiDeskWireErr(err)
		}
		return "", nil
	case "sky:aiStreamNext":
		ev, err := aiDeskStreamNext()
		if err != nil {
			return "", aiDeskWireErr(err)
		}
		return ev, nil
	case "sky:aiEvict":
		o, err := aiDeskOffer(p["offer"])
		if err != nil {
			return "", aiDeskWireErr(err)
		}
		aiDeskEvict(o)
		return "", nil
	}
	return "", errors.New("invalid: unknown op " + op)
}

func aiDeskOffer(s string) (*aiDeviceOffer, error) {
	var o aiDeviceOffer
	if err := json.Unmarshal([]byte(s), &o); err != nil || o.Model.Sha256 == "" {
		return nil, aiFailf(aiInvalidSetting, "the device offer cannot be read").with("setting", "offer")
	}
	return &o, nil
}

func aiDeskFiles(o *aiDeviceOffer) []aiDeviceFile {
	fs := []aiDeviceFile{o.Model}
	if o.Adapter != nil {
		fs = append(fs, *o.Adapter)
	}
	return fs
}

func aiDeskKey(o *aiDeviceOffer) string {
	k := o.Slot
	for _, f := range aiDeskFiles(o) {
		k += ":" + f.Sha256
	}
	return k
}

// aiDeskEmbedded is the bundled copy of a file (`--ai-embed`): in the app's
// Resources/sky-ai, or beside the executable in sky-ai/.
func aiDeskEmbedded(sum string) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	for _, d := range []string{filepath.Join(dir, "..", "Resources", "sky-ai"), filepath.Join(dir, "sky-ai")} {
		p := filepath.Join(d, sum+".gguf")
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// aiDeskStorePath is where the desktop app keeps a downloaded file: its own
// store under the AI home, apart from Std.Ai.Local's models, so an evict never
// removes a model a server on this Mac uses.
func aiDeskStorePath(sum string) string {
	return filepath.Join(aiHome(), "device", "files", sum+".gguf")
}

// aiDeskReady is the verified local path of a file, or "": the bundled copy,
// the device store, or Std.Ai.Local's store (a model already pulled on this
// Mac is not downloaded twice).
func aiDeskReady(sum string) string {
	if p := aiDeskEmbedded(sum); p != "" {
		return p
	}
	for _, p := range []string{aiDeskStorePath(sum), aiModelStorePath(sum)} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func aiDeskCaps() aiDeviceCaps {
	c := aiDeviceCaps{Platform: "mac"}
	mem := aiK().availableBytes()
	c.Backends = []aiDeviceBackendInfo{{Backend: aiDeviceNative, MaxModelBytes: mem / 2}}
	c.Unsupported = []aiDeviceUnsupportedBackend{
		{Backend: aiDeviceWebGpu, Reason: "the desktop app runs llama.cpp natively"},
		{Backend: aiDeviceWasmCpu, Reason: "the desktop app runs llama.cpp natively"},
	}
	c.StorageAvailableBytes = aiDiskAvailable(aiHome())
	dirs := []string{filepath.Dir(aiDeskStorePath("x")), filepath.Dir(aiModelStorePath("x"))}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "..", "Resources", "sky-ai"), filepath.Join(filepath.Dir(exe), "sky-ai"))
	}
	{
		for _, d := range dirs {
			if entries, err := os.ReadDir(d); err == nil {
				for _, e := range entries {
					if sum, ok := strings.CutSuffix(e.Name(), ".gguf"); ok && aiValidSha256(sum) {
						c.Ready = append(c.Ready, sum)
					}
				}
			}
		}
	}
	c.normalise()
	return c
}

func aiDeskSetProgress(k string, p aiDeviceProgress) {
	aiDeskMu.Lock()
	aiDeskProgress[k] = p
	aiDeskMu.Unlock()
}

// aiDeskPrepare downloads what is missing (checked by SHA-256) and starts the
// engine.
func aiDeskPrepare(o *aiDeviceOffer, origin string) error {
	k := aiDeskKey(o)
	var total, done int64
	for _, f := range aiDeskFiles(o) {
		total += f.Bytes
	}
	aiDeskSetProgress(k, aiDeviceProgress{State: "downloading", TotalBytes: total})
	fail := func(err error) error {
		aiDeskSetProgress(k, aiDeviceProgress{State: "failed", DoneBytes: done, TotalBytes: total, Error: err.Error()})
		return err
	}
	base, err := url.Parse(origin)
	if err != nil || base.Scheme == "" {
		return fail(aiFailf(aiInvalidSetting, "the page has no origin to download from").with("setting", "origin"))
	}
	for _, f := range aiDeskFiles(o) {
		if aiDeskReady(f.Sha256) != "" {
			done += f.Bytes
			continue
		}
		ref, err := url.Parse(f.URL)
		if err != nil {
			return fail(aiFailf(aiInvalidSetting, "the offer's URL %q cannot be read", f.URL).with("setting", "offer"))
		}
		if err := aiDeskDownload(base.ResolveReference(ref).String(), f, func(n int64) {
			aiDeskSetProgress(k, aiDeviceProgress{State: "downloading", DoneBytes: done + n, TotalBytes: total})
		}); err != nil {
			return fail(err)
		}
		done += f.Bytes
	}
	if _, err := aiDeskEngine(o); err != nil {
		return fail(err)
	}
	aiDeskSetProgress(k, aiDeviceProgress{State: "ready", DoneBytes: total, TotalBytes: total})
	return nil
}

func aiDeskDownload(u string, f aiDeviceFile, progress func(int64)) error {
	dest := aiDeskStorePath(f.Sha256)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".part-" + aiRandomHex(4)
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		out.Close()
		os.Remove(tmp)
		return err
	}
	h := sha256.New()
	var n int64
	buf := make([]byte, 1<<20)
	// 64 MiB Range requests: each finishes well inside a server's write
	// timeout, and a slow link never holds one response open.
	for off := int64(0); off < f.Bytes; off += aiDeviceChunkBytes {
		end := off + aiDeviceChunkBytes - 1
		if end > f.Bytes-1 {
			end = f.Bytes - 1
		}
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return fail(err)
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))
		resp, err := aiDeskClient.Do(req)
		if err != nil {
			return fail(aiFailf(aiUnreachable, "%s: %v", f.Label, aiStripURL(err)).with("target", "the app's server"))
		}
		if resp.StatusCode != http.StatusPartialContent && !(resp.StatusCode == http.StatusOK && off == 0 && end == f.Bytes-1) {
			resp.Body.Close()
			ff := aiFailf(aiRemoteRejected, "%s: the server answered %d", f.Label, resp.StatusCode)
			ff.httpStatus = resp.StatusCode
			return fail(ff)
		}
		for {
			m, rerr := resp.Body.Read(buf)
			if m > 0 {
				if _, werr := out.Write(buf[:m]); werr != nil {
					resp.Body.Close()
					return fail(werr)
				}
				h.Write(buf[:m])
				n += int64(m)
				progress(n)
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				resp.Body.Close()
				return fail(aiFailf(aiUnreachable, "%s: %v", f.Label, rerr).with("target", "the app's server"))
			}
		}
		resp.Body.Close()
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != f.Sha256 {
		os.Remove(tmp)
		return aiFailf(aiChecksumMismatch, "%s: expected sha256 %s, got %s", f.Label, f.Sha256, got).
			with("expected", f.Sha256).with("actual", got)
	}
	return os.Rename(tmp, dest)
}

// aiDeskEngine is the managed engine for an offer's model and adapter.
func aiDeskEngine(o *aiDeviceOffer) (*aiEngine, error) {
	model := aiDeskReady(o.Model.Sha256)
	if model == "" {
		return nil, aiFailf(aiNotProvisioned, "the on-device model of slot %s is not downloaded", o.Slot).
			with("what", "the on-device model of slot "+o.Slot).with("command", "Device.prepare \""+o.Slot+"\"")
	}
	spec := aiLocalSpec{
		model:      aiModelRef{kind: "file", file: model, sha256: o.Model.Sha256},
		context:    o.Context,
		gpu:        "auto",
		parallel:   1,
		idleUnload: aiDefaultIdleUnload(),
	}
	if o.Adapter != nil {
		ap := aiDeskReady(o.Adapter.Sha256)
		if ap == "" {
			return nil, aiFailf(aiNotProvisioned, "the device adapter of slot %s is not downloaded", o.Slot).
				with("what", "the device adapter of slot "+o.Slot).with("command", "Device.prepare \""+o.Slot+"\"")
		}
		spec.adapter = aiLocalAdapter{path: ap, sha256: o.Adapter.Sha256, label: o.Adapter.Label}
	}
	e := aiEngineFor("device_"+o.Slot, spec)
	ctx, cancel := context.WithTimeout(context.Background(), aiStartTimeout())
	defer cancel()
	return e, e.ensure(ctx)
}

func aiDeskChat(o *aiDeviceOffer, path, body string) (string, error) {
	if !aiDeviceCallPaths[path] {
		return "", aiFailf(aiRemoteRejected, "a device answers chat requests only").withInt("status", 404)
	}
	e, err := aiDeskEngine(o)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return e.post(ctx, 10*time.Minute, path, body)
}

// The desktop app streams one answer at a time (Provider.chatStream), as the
// device shells do: a new begin closes the one before.
var aiDeskStream string

func aiDeskStreamBegin(o *aiDeviceOffer, path, body string) error {
	if path != "/chat/completions" {
		return aiFailf(aiRemoteRejected, "a device answers chat requests only").withInt("status", 404)
	}
	e, err := aiDeskEngine(o)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	st, done, err := e.openStream(ctx, path, aiStreamBody(body))
	if err != nil {
		cancel()
		if done != nil {
			done()
		}
		return err
	}
	st.onClose = append(st.onClose, func(error) {
		cancel()
		done()
	})
	aiDeskMu.Lock()
	prev := aiDeskStream
	aiDeskStream = aiStreamRegister(st, 10*time.Minute)
	aiDeskMu.Unlock()
	if prev != "" {
		aiStreamDrop(prev, nil)
	}
	return nil
}

func aiDeskStreamNext() (string, error) {
	aiDeskMu.Lock()
	id := aiDeskStream
	aiDeskMu.Unlock()
	st, err := aiStreamGet(id)
	if err != nil {
		return "", err
	}
	ev, done, err := st.next()
	if err != nil || done {
		aiStreamDrop(id, err)
	}
	return ev, err
}

func aiDeskEvict(o *aiDeviceOffer) {
	aiDeskMu.Lock()
	delete(aiDeskProgress, aiDeskKey(o))
	aiDeskMu.Unlock()
	for _, f := range aiDeskFiles(o) {
		// Only the device store: never a bundled copy or Std.Ai.Local's model.
		_ = os.Remove(aiDeskStorePath(f.Sha256))
	}
}
