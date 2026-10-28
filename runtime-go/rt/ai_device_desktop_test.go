//go:build !js

package rt

// The macOS desktop shell's Std.Ai.Device bridge (ai_device_desktop.go): the
// capabilities it reports, a download checked by its SHA-256 into the device
// store, a file that does not match refused, and the chat on the managed
// engine (the fake engine of ai_test.go).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func deskOffer(sum string, size int, url string) string {
	b, _ := json.Marshal(aiDeviceOffer{Slot: "chat", Placement: "auto", Context: 2048,
		Model: aiDeviceFile{Label: "m.gguf", Sha256: sum, Bytes: int64(size), URL: url}})
	return string(b)
}

func TestDeviceDesktopDownloadsVerifiesAndAnswers(t *testing.T) {
	_, modelRef := aiTestEnv(t, nil)
	path := strings.TrimPrefix(modelRef[:strings.Index(modelRef, "#")], "file:")
	sum := modelRef[strings.Index(modelRef, "sha256=")+7:]
	data, _ := os.ReadFile(path)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/bad") {
			_, _ = w.Write([]byte("not the model"))
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	caps, err := aiDeviceDesktopDispatch("sky:aiCaps", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c aiDeviceCaps
	_ = json.Unmarshal([]byte(caps), &c)
	if c.Platform != "mac" || len(c.Backends) != 1 || c.Backends[0].Backend != "native" || c.Backends[0].MaxModelBytes <= 0 {
		t.Fatalf("caps: %s", caps)
	}

	// A file that does not match its digest is refused and not stored.
	_, err = aiDeviceDesktopDispatch("sky:aiPrepare", map[string]string{
		"offer": deskOffer(sum, len(data), "/_sky/ai/blob/bad"), "origin": srv.URL})
	if err == nil || !strings.HasPrefix(err.Error(), "ai:") || !strings.Contains(err.Error(), "ChecksumMismatch") {
		t.Fatalf("a wrong file: %v", err)
	}
	if fileExists(aiDeskStorePath(sum)) {
		t.Fatal("a file that did not match was stored")
	}

	offer := deskOffer(sum, len(data), "/_sky/ai/blob/"+sum)
	if out, err := aiDeviceDesktopDispatch("sky:aiPrepare", map[string]string{"offer": offer, "origin": srv.URL}); err != nil || out != "ready" {
		t.Fatalf("prepare: %q %v", out, err)
	}
	if !fileExists(aiDeskStorePath(sum)) {
		t.Fatal("the verified file is not in the device store")
	}
	st, _ := aiDeviceDesktopDispatch("sky:aiStatus", map[string]string{"offer": offer})
	if !strings.Contains(st, `"state":"ready"`) {
		t.Fatalf("status: %s", st)
	}
	caps, _ = aiDeviceDesktopDispatch("sky:aiCaps", nil)
	if !strings.Contains(caps, sum) {
		t.Fatalf("the stored file is ready: %s", caps)
	}
	out, err := aiDeviceDesktopDispatch("sky:aiChat", map[string]string{"offer": offer, "path": "/chat/completions", "body": chatBody("hi")})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if chatContent(t, out) == "" {
		t.Fatalf("chat: %s", out)
	}
	if _, err := aiDeviceDesktopDispatch("sky:aiChat", map[string]string{"offer": offer, "path": "/embeddings", "body": "{}"}); err == nil || !strings.Contains(err.Error(), "RemoteRejected") {
		t.Fatalf("a device answers chat only: %v", err)
	}
	if _, err := aiDeviceDesktopDispatch("sky:aiEvict", map[string]string{"offer": offer}); err != nil {
		t.Fatal(err)
	}
	if fileExists(aiDeskStorePath(sum)) {
		t.Fatal("evict left the file")
	}
	if _, err := aiDeviceDesktopDispatch("sky:aiNope", map[string]string{}); err == nil || !strings.HasPrefix(err.Error(), "invalid: unknown op") {
		t.Fatalf("unknown op: %v", err)
	}
}

func TestDeviceDesktopOpsReachTheDispatch(t *testing.T) {
	// The desktop window's one handler (nativeShellDispatch) routes the AI ops.
	if _, err := nativeShellDispatch(nil, nil, "sky:aiCaps", "{}"); err != nil {
		t.Fatalf("sky:aiCaps through the shell dispatch: %v", err)
	}
}
