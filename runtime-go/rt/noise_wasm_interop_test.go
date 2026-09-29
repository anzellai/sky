//go:build !js

package rt

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// A Noise IK (BLAKE2s) handshake between the wasm client and a Go server: the
// device side of a Sky.Spa app with `withClientCrypto`. The initiator runs in
// Go's js/wasm build under Node.js (rt/noisewasm, with the client's own fetch
// kernel for HTTP); the responder is this native process. Both sides use the
// same rt kernels a Sky program calls. A completed handshake and a transport
// round trip prove the wasm build's crypto (X25519, BLAKE2s, ChaCha20-Poly1305,
// crypto/rand through getRandomValues) matches the native build byte for byte.
func TestNoiseWasmInitiatorInteropsWithNativeResponder(t *testing.T) {
	node := requireNode(t)
	goroot := runtime.GOROOT()
	if _, err := os.Stat(filepath.Join(goroot, "lib", "wasm", "wasm_exec_node.js")); err != nil {
		t.Fatalf("the Go toolchain has no lib/wasm/wasm_exec_node.js: %v", err)
	}
	goBin := filepath.Join(goroot, "bin", "go")

	serverKey := cryOk(t, runCryptoTaskAny(t, Kx_generate(nil)))
	serverPub := Kx_publicKeyToBytes(Kx_publicKey(serverKey)).(string)

	var mu sync.Mutex
	var transport any
	var peerHex string
	readHex := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return "", false
		}
		raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil {
			http.Error(w, "not hex", 400)
			return "", false
		}
		return string(raw), true
	}
	fail := func(w http.ResponseWriter, what string, r any) {
		_, _, e := anyResultView(r)
		http.Error(w, what+": "+extractErrMsg(Err[any, any](e)), 500)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/handshake", func(w http.ResponseWriter, r *http.Request) {
		m0, good := readHex(w, r)
		if !good {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		hs := cryOk(t, runCryptoTaskAny(t, Noise_responderSuite("BLAKE2s", serverKey, "sky wasm interop")))
		res := Noise_readMessage(m0, hs)
		if tag, _, _ := anyResultView(res); tag != 0 {
			fail(w, "readMessage", res)
			return
		}
		tp := cryOk(t, res).(SkyTuple2)
		if tp.V1.(string) != "hello from wasm" {
			http.Error(w, "wrong payload", 500)
			return
		}
		peer := Noise_peer(tp.V0).(SkyMaybe[any])
		peerHex = hex.EncodeToString([]byte(Kx_publicKeyToBytes(peer.JustValue).(string)))
		res = Noise_writeMessage("welcome", tp.V0)
		if tag, _, _ := anyResultView(res); tag != 0 {
			fail(w, "writeMessage", res)
			return
		}
		tp = cryOk(t, res).(SkyTuple2)
		transport = cryOk(t, Noise_transport(tp.V0))
		_, _ = io.WriteString(w, hex.EncodeToString([]byte(tp.V1.(string))))
	})
	mux.HandleFunc("/message", func(w http.ResponseWriter, r *http.Request) {
		c, good := readHex(w, r)
		if !good {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		res := Noise_decrypt(c, transport)
		if tag, _, _ := anyResultView(res); tag != 0 {
			fail(w, "decrypt", res)
			return
		}
		tp := cryOk(t, res).(SkyTuple2)
		res = Noise_encrypt("pong:"+tp.V1.(string)+":"+peerHex, tp.V0)
		tp = cryOk(t, res).(SkyTuple2)
		transport = tp.V0
		_, _ = io.WriteString(w, hex.EncodeToString([]byte(tp.V1.(string))))
	})
	// The keyed Crypto primitives under a key both sides derive (the wasm
	// side's `TestWasmAeadAndMacInteropWithNative`).
	aeadKey := cryOk(t, Kdf_expand(Kdf_extract("salt", Secret_fromString("input key material")), "info", 32))
	mux.HandleFunc("/aead", func(w http.ResponseWriter, r *http.Request) {
		body, good := readHex(w, r)
		if !good {
			return
		}
		parts := strings.Split(body, "\n")
		if len(parts) != 3 {
			http.Error(w, "want 3 lines", 400)
			return
		}
		res := Crypto_xchachaOpen(aeadKey, parts[0])
		if tag, _, _ := anyResultView(res); tag != 0 || cryOk(t, res).(string) != "random nonce" {
			fail(w, "xchachaOpen", res)
			return
		}
		sealed, err := hex.DecodeString(parts[1])
		if err != nil {
			http.Error(w, "not hex", 400)
			return
		}
		res = Crypto_chacha20Poly1305Open(aeadKey, "000000000000", "ad", string(sealed))
		if tag, _, _ := anyResultView(res); tag != 0 || cryOk(t, res).(string) != "explicit nonce" {
			fail(w, "chacha20Poly1305Open", res)
			return
		}
		if parts[2] != Crypto_hmacSha256("key", "hello").(string) {
			http.Error(w, "the wasm MAC differs from the native one", 500)
			return
		}
		out := cryOk(t, Crypto_chacha20Poly1305Seal(aeadKey, "111111111111", "ad", "pong")).(string)
		_, _ = io.WriteString(w, hex.EncodeToString([]byte(out)))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	// Build the wasm test binary with the full environment, then run it under
	// node with only what it needs: wasm_exec.js refuses a command line plus
	// environment larger than a few kilobytes.
	bin := filepath.Join(t.TempDir(), "noisewasm.test.wasm")
	build := exec.CommandContext(ctx, goBin, "test", "-c", "-o", bin, "./noisewasm/")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the wasm initiator failed: %v\n%s", err, out)
	}
	cmd := exec.CommandContext(ctx, node, filepath.Join(goroot, "lib", "wasm", "wasm_exec_node.js"),
		bin, "-test.count=1", "-test.v")
	cmd.Env = []string{
		"PATH=" + filepath.Dir(node),
		"SKY_NOISE_SERVER=" + srv.URL,
		"SKY_NOISE_SERVER_PUB=" + hex.EncodeToString([]byte(serverPub)),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the wasm initiator failed: %v\n%s", err, out)
	}
	for _, name := range []string{"TestWasmKeysAreRandom", "TestWasmInitiatorTalksToNativeResponder", "TestWasmAeadAndMacInteropWithNative"} {
		if !strings.Contains(string(out), "--- PASS: "+name) {
			t.Fatalf("the wasm test %s did not pass:\n%s", name, out)
		}
	}
}
