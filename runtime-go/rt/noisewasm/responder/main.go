//go:build !js

// Command responder is the Noise IK (BLAKE2s) server end of
// scripts/spa-client-crypto-e2e.sh: a Sky.Spa app with `withClientCrypto`
// runs the initiator in its wasm client, and its backend only relays public
// bytes (hex) to this process. It uses the same rt kernels a Sky program
// calls.
//
//	POST /handshake  <hex of message 1>  -> <hex of message 2>
//	POST /echo       <hex of a transport ciphertext of "ping">
//	                                     -> <hex of a ciphertext of "pong:ping">
//
// Usage: responder <addr> [<static secret key, 64 hex digits>]. With no key
// it generates one. It prints `PUB <hex>` (its static public key, which the
// client pins) and then `LISTEN <addr>` on stdout, and serves until it is
// killed. Not a test on its own: it has no assertions; the e2e script checks
// what the app shows.
package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"sky-app/rt"
)

func ok(v any) (any, error) {
	r, isRes := v.(rt.SkyResult[any, any])
	if !isRes {
		return nil, fmt.Errorf("not a Result: %T", v)
	}
	if r.Tag != 0 {
		return nil, fmt.Errorf("Err: %v", r.ErrValue)
	}
	return r.OkValue, nil
}

func task(v any) (any, error) {
	return ok(rt.AnyTaskRun(v))
}

func main() {
	addr := "127.0.0.1:0"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	var key any
	var err error
	if len(os.Args) > 2 {
		raw, herr := hex.DecodeString(os.Args[2])
		if herr != nil {
			fmt.Fprintln(os.Stderr, "secret key:", herr)
			rt.ExitProcess(1)
		}
		key, err = ok(rt.Kx_secretKeyFromBytes(rt.Secret_fromString(string(raw))))
	} else {
		key, err = task(rt.Kx_generate(nil))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "static key:", err)
		rt.ExitProcess(1)
	}
	pub := rt.Kx_publicKeyToBytes(rt.Kx_publicKey(key)).(string)

	var mu sync.Mutex
	var transport any
	body := func(r *http.Request) (string, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return "", err
		}
		raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
		return string(raw), err
	}
	reply := func(w http.ResponseWriter, what string, err error) {
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		http.Error(w, what+": "+err.Error(), 500)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/handshake", func(w http.ResponseWriter, r *http.Request) {
		m1, err := body(r)
		if err != nil {
			reply(w, "read", err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		hs, err := task(rt.Noise_responderSuite("BLAKE2s", key, "relay v1"))
		if err != nil {
			reply(w, "responder", err)
			return
		}
		res, err := ok(rt.Noise_readMessage(m1, hs))
		if err != nil {
			reply(w, "readMessage", err)
			return
		}
		tp := res.(rt.SkyTuple2)
		res, err = ok(rt.Noise_writeMessage("", tp.V0))
		if err != nil {
			reply(w, "writeMessage", err)
			return
		}
		tp = res.(rt.SkyTuple2)
		if transport, err = ok(rt.Noise_transport(tp.V0)); err != nil {
			reply(w, "transport", err)
			return
		}
		fmt.Println("HANDSHAKE ok")
		_, _ = io.WriteString(w, hex.EncodeToString([]byte(tp.V1.(string))))
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		c, err := body(r)
		if err != nil {
			reply(w, "read", err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if transport == nil {
			reply(w, "echo", fmt.Errorf("no handshake yet"))
			return
		}
		res, err := ok(rt.Noise_decrypt(c, transport))
		if err != nil {
			reply(w, "decrypt", err)
			return
		}
		tp := res.(rt.SkyTuple2)
		res, err = ok(rt.Noise_encrypt("pong:"+tp.V1.(string), tp.V0))
		if err != nil {
			reply(w, "encrypt", err)
			return
		}
		tp = res.(rt.SkyTuple2)
		transport = tp.V0
		fmt.Println("ECHO ok")
		_, _ = io.WriteString(w, hex.EncodeToString([]byte(tp.V1.(string))))
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		rt.ExitProcess(1)
	}
	fmt.Printf("PUB %s\nLISTEN %s\n", hex.EncodeToString([]byte(pub)), ln.Addr())
	_ = http.Serve(ln, mux)
}
