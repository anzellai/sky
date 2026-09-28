//go:build js && wasm

// Package noisewasm runs the Std.Crypto kernels INSIDE Go's js/wasm build, the
// way a Sky.Spa client with `withClientCrypto` runs them in the browser. It is
// driven by rt/noise_wasm_interop_test.go, which starts a native Go responder
// and runs this package under Node.js (`go_js_wasm_exec`); run on its own it
// fails, because it needs that server's address.
package noisewasm

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"sky-app/rt"
)

func ok(t *testing.T, v any) any {
	t.Helper()
	r, isRes := v.(rt.SkyResult[any, any])
	if !isRes {
		t.Fatalf("expected a Result, got %T", v)
	}
	if r.Tag != 0 {
		t.Fatalf("Err: %v", r.ErrValue)
	}
	return r.OkValue
}

func run(t *testing.T, task any) any {
	t.Helper()
	f, isTask := task.(func() any)
	if !isTask {
		t.Fatalf("expected a Task, got %T", task)
	}
	return ok(t, f())
}

func pair(t *testing.T, v any) (any, string) {
	t.Helper()
	tp := ok(t, v).(rt.SkyTuple2)
	return tp.V0, tp.V1.(string)
}

// post sends hex text to the responder with the wasm client's own HTTP kernel
// (browser fetch) and returns the decoded reply.
func post(t *testing.T, url, body string) string {
	t.Helper()
	resp := run(t, rt.Http_post(url, hex.EncodeToString([]byte(body)))).(rt.HttpResponse)
	if resp.Status != 200 {
		t.Fatalf("POST %s: status %d: %s", url, resp.Status, resp.Body)
	}
	b, err := hex.DecodeString(strings.TrimSpace(resp.Body))
	if err != nil {
		t.Fatalf("POST %s: reply is not hex: %q", url, resp.Body)
	}
	return string(b)
}

// The entropy source works in wasm: two generated keys differ.
func TestWasmKeysAreRandom(t *testing.T) {
	a := rt.Kx_publicKeyToBytes(rt.Kx_publicKey(run(t, rt.Kx_generate(nil))))
	b := rt.Kx_publicKeyToBytes(rt.Kx_publicKey(run(t, rt.Kx_generate(nil))))
	if a == b {
		t.Fatal("two keys generated in wasm are equal: crypto/rand is not drawing entropy")
	}
}

// A full Noise IK handshake (BLAKE2s) and transport round trip between this
// wasm initiator and the native Go responder.
func TestWasmInitiatorTalksToNativeResponder(t *testing.T) {
	base := os.Getenv("SKY_NOISE_SERVER")
	pubHex := os.Getenv("SKY_NOISE_SERVER_PUB")
	if base == "" || pubHex == "" {
		t.Fatal("SKY_NOISE_SERVER / SKY_NOISE_SERVER_PUB are not set: run this through rt/noise_wasm_interop_test.go")
	}
	pubBytes, err := hex.DecodeString(pubHex)
	if err != nil {
		t.Fatal(err)
	}
	serverPub := ok(t, rt.Kx_publicKeyFromBytes(string(pubBytes)))
	myKey := run(t, rt.Kx_generate(nil))
	hs := run(t, rt.Noise_initiatorSuite("BLAKE2s", myKey, serverPub, "sky wasm interop"))

	hs, m0 := pair(t, rt.Noise_writeMessage("hello from wasm", hs))
	m1 := post(t, base+"/handshake", m0)
	hs, p1 := pair(t, rt.Noise_readMessage(m1, hs))
	if p1 != "welcome" {
		t.Fatalf("responder payload = %q, want welcome", p1)
	}
	tr := ok(t, rt.Noise_transport(hs))

	tr, c := pair(t, rt.Noise_encrypt("ping", tr))
	reply := post(t, base+"/message", c)
	tr, pt := pair(t, rt.Noise_decrypt(reply, tr))
	_ = tr
	// The responder echoes the plaintext and the initiator static key it learned
	// from the handshake, so both sides agree on who the device is.
	myPub := hex.EncodeToString([]byte(rt.Kx_publicKeyToBytes(rt.Kx_publicKey(myKey)).(string)))
	if pt != "pong:ping:"+myPub {
		t.Fatalf("transport reply = %q, want pong:ping:%s", pt, myPub)
	}
}
