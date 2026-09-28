package rt

import (
	"fmt"
	"strings"
	"testing"
)

// Noise_IK_25519_ChaChaPoly_SHA256 vectors in the cacophony format, as
// shipped in the vectors.txt of github.com/flynn/noise v1.1.0 (the four
// non-PSK IK entries: with and without prologue, with and without payloads).
// msg_0 / msg_1 are the handshake; msg_2 is initiator → responder and msg_3
// responder → initiator over the transport.
var noiseIKVectors = []struct {
	prologue string
	payloads [4]string
	cts      [4]string
}{
	{"",
		[4]string{"", "", "79656c6c6f777375626d6172696e65", "7375626d6172696e6579656c6c6f77"},
		[4]string{
			"358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd1662544f8445e5dc2467b1e32653192d05dee85c4781bf0dd8d33ceebb5905a7a069f09e0d3f2cad1c842930a762eb75e52827f01d2c85189d527644b3221b4c3fc5cc",
			"64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d48466aabfe2e5b1650bbaa88e33679893fc77",
			"226ca869f2777611f37350a7ab446f650c0cfe2855b7f020ce658bcf100f2d",
			"90d84d69cd44829283b05d684879b53b8d714e51619b601438a1ae67caacd9",
		}},
	{"",
		[4]string{"746573745f6d73675f30", "746573745f6d73675f31", "79656c6c6f777375626d6172696e65", "7375626d6172696e6579656c6c6f77"},
		[4]string{
			"358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd1662544f8445e5dc2467b1e32653192d05dee85c4781bf0dd8d33ceebb5905a7a069f09e0d3f2cad1c842930a762eb75e528270337527f958f92050deefa1892482d74328fee90d08201bba3cc",
			"64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d48466cb4a35db52355821787bb891112ba10f4d3dfe08b27d634db8af",
			"226ca869f2777611f37350a7ab446f650c0cfe2855b7f020ce658bcf100f2d",
			"90d84d69cd44829283b05d684879b53b8d714e51619b601438a1ae67caacd9",
		}},
	{"6e6f74736563726574",
		[4]string{"", "", "79656c6c6f777375626d6172696e65", "7375626d6172696e6579656c6c6f77"},
		[4]string{
			"358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd1662544f8445e5dc2467b1e32653192d05dee85c4781bf0dd8d33ceebb5905a7a069f0d6bc97dbce6f8f0ee33d49311a72d0f8c4ef8ef3bc70ccb18fd61ad67dde7eda",
			"64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d48466787857f66c036e974ef9d6335d2ccc5f",
			"226ca869f2777611f37350a7ab446f650c0cfe2855b7f020ce658bcf100f2d",
			"90d84d69cd44829283b05d684879b53b8d714e51619b601438a1ae67caacd9",
		}},
	{"6e6f74736563726574",
		[4]string{"746573745f6d73675f30", "746573745f6d73675f31", "79656c6c6f777375626d6172696e65", "7375626d6172696e6579656c6c6f77"},
		[4]string{
			"358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd1662544f8445e5dc2467b1e32653192d05dee85c4781bf0dd8d33ceebb5905a7a069f0d6bc97dbce6f8f0ee33d49311a72d0f80337527f958f92050deee33c19777fa17306346367055751bb3f",
			"64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d48466cb4a35db52355821787bb67f33957e7809370c44d33538ad5a42",
			"226ca869f2777611f37350a7ab446f650c0cfe2855b7f020ce658bcf100f2d",
			"90d84d69cd44829283b05d684879b53b8d714e51619b601438a1ae67caacd9",
		}},
}

const (
	noiseInitStatic = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	noiseRespStatic = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	noiseInitEph    = "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
	noiseRespEph    = "4142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f60"
)

func noiseVectorPair(t *testing.T, prologue string) (NoiseHandshake, NoiseHandshake) {
	is, rs := unhex(t, noiseInitStatic), unhex(t, noiseRespStatic)
	ini := noiseNew(true, is, kxPublic(rs), prologue, unhex(t, noiseInitEph))
	res := noiseNew(false, rs, "", prologue, unhex(t, noiseRespEph))
	return ini, res
}

func TestNoiseIKVectors(t *testing.T) {
	for vi, v := range noiseIKVectors {
		ini, res := noiseVectorPair(t, unhex(t, v.prologue))
		ini, m0, err := ini.writeMessage([]byte(unhex(t, v.payloads[0])))
		if err != nil || string(m0) != unhex(t, v.cts[0]) {
			t.Fatalf("vector %d msg_0: %v\n got %x", vi, err, m0)
		}
		res, p0, err := res.readMessage(m0)
		if err != nil || string(p0) != unhex(t, v.payloads[0]) {
			t.Fatalf("vector %d: responder could not read msg_0: %v", vi, err)
		}
		if res.rs != kxPublic(unhex(t, noiseInitStatic)) {
			t.Fatalf("vector %d: responder did not learn the initiator's static key", vi)
		}
		res, m1, err := res.writeMessage([]byte(unhex(t, v.payloads[1])))
		if err != nil || string(m1) != unhex(t, v.cts[1]) {
			t.Fatalf("vector %d msg_1: %v\n got %x", vi, err, m1)
		}
		ini, p1, err := ini.readMessage(m1)
		if err != nil || string(p1) != unhex(t, v.payloads[1]) {
			t.Fatalf("vector %d: initiator could not read msg_1: %v", vi, err)
		}
		it, err := ini.split()
		if err != nil {
			t.Fatal(err)
		}
		rt_, err := res.split()
		if err != nil {
			t.Fatal(err)
		}
		if it.hash != rt_.hash {
			t.Fatalf("vector %d: handshake hashes differ", vi)
		}
		it, m2, err := it.encrypt([]byte(unhex(t, v.payloads[2])))
		if err != nil || string(m2) != unhex(t, v.cts[2]) {
			t.Fatalf("vector %d msg_2: %v\n got %x", vi, err, m2)
		}
		rt_, p2, err := rt_.decrypt(m2)
		if err != nil || string(p2) != unhex(t, v.payloads[2]) {
			t.Fatalf("vector %d: msg_2 did not decrypt: %v", vi, err)
		}
		_, m3, err := rt_.encrypt([]byte(unhex(t, v.payloads[3])))
		if err != nil || string(m3) != unhex(t, v.cts[3]) {
			t.Fatalf("vector %d msg_3: %v\n got %x", vi, err, m3)
		}
		if _, p3, err := it.decrypt(m3); err != nil || string(p3) != unhex(t, v.payloads[3]) {
			t.Fatalf("vector %d: msg_3 did not decrypt: %v", vi, err)
		}
	}
}

func noiseKernelPair(t *testing.T) (any, any, any) {
	iS := cryOk(t, runCryptoTaskAny(t, Kx_generate(nil)))
	rS := cryOk(t, runCryptoTaskAny(t, Kx_generate(nil)))
	ini := cryOk(t, runCryptoTaskAny(t, Noise_initiator(iS, Kx_publicKey(rS), "app v1")))
	res := cryOk(t, runCryptoTaskAny(t, Noise_responder(rS, "app v1")))
	return ini, res, iS
}

func tuple(t *testing.T, v any) (any, string) {
	t.Helper()
	tp := cryOk(t, v).(SkyTuple2)
	return tp.V0, tp.V1.(string)
}

// A round trip through the kernels, with rekey on both directions.
func TestNoiseKernelRoundTripAndRekey(t *testing.T) {
	ini, res, iS := noiseKernelPair(t)
	if Noise_peer(res).(SkyMaybe[any]).Tag != 1 {
		t.Fatalf("responder knows a peer before the first message")
	}
	ini, m0 := tuple(t, Noise_writeMessage("hello", ini))
	res, p0 := tuple(t, Noise_readMessage(m0, res))
	if p0 != "hello" {
		t.Fatalf("payload 0 = %q", p0)
	}
	peer := Noise_peer(res).(SkyMaybe[any])
	if peer.Tag != 0 || Kx_publicKeyToBytes(peer.JustValue) != Kx_publicKeyToBytes(Kx_publicKey(iS)) {
		t.Fatalf("responder did not learn the initiator's static key")
	}
	if Noise_isComplete(res).(bool) {
		t.Fatalf("complete after one message")
	}
	res, m1 := tuple(t, Noise_writeMessage("welcome", res))
	ini, p1 := tuple(t, Noise_readMessage(m1, ini))
	if p1 != "welcome" || !Noise_isComplete(ini).(bool) || !Noise_isComplete(res).(bool) {
		t.Fatalf("handshake did not complete")
	}
	it := cryOk(t, Noise_transport(ini))
	rt_ := cryOk(t, Noise_transport(res))
	if Noise_handshakeHash(it) != Noise_handshakeHash(rt_) {
		t.Fatalf("handshake hashes differ")
	}
	for i := 0; i < 5; i++ {
		var ct, pt string
		it, ct = tuple(t, Noise_encrypt("ping", it))
		rt_, pt = tuple(t, Noise_decrypt(ct, rt_))
		if pt != "ping" {
			t.Fatalf("round %d: %q", i, pt)
		}
		if i == 2 {
			it = cryOk(t, Noise_rekeySend(it))
			rt_ = cryOk(t, Noise_rekeyReceive(rt_))
		}
	}
	var ct, pt string
	rt_, ct = tuple(t, Noise_encrypt("pong", rt_))
	_, pt = tuple(t, Noise_decrypt(ct, it))
	if pt != "pong" {
		t.Fatalf("reverse direction: %q", pt)
	}
}

// Reusing an old state value would reuse a nonce: it is refused.
func TestNoiseRefusesStaleState(t *testing.T) {
	ini, res, _ := noiseKernelPair(t)
	ini2, m0 := tuple(t, Noise_writeMessage("a", ini))
	cryErr(t, Noise_writeMessage("b", ini), "already used")
	res, _ = tuple(t, Noise_readMessage(m0, res))
	res2, m1 := tuple(t, Noise_writeMessage("", res))
	ini3, _ := tuple(t, Noise_readMessage(m1, ini2))
	cryErr(t, Noise_readMessage(m1, ini2), "already used")
	it := cryOk(t, Noise_transport(ini3))
	cryErr(t, Noise_transport(ini3), "already used")
	rt_ := cryOk(t, Noise_transport(res2))
	it2, c1 := tuple(t, Noise_encrypt("one", it))
	cryErr(t, Noise_encrypt("two", it), "already used")
	_, _ = tuple(t, Noise_decrypt(c1, rt_))
	cryErr(t, Noise_decrypt(c1, rt_), "already used")
	_ = it2
}

func TestNoiseRejectsTamperingAndWrongKeys(t *testing.T) {
	// Tampered first message.
	ini, res, _ := noiseKernelPair(t)
	_, m0 := tuple(t, Noise_writeMessage("x", ini))
	bad := []byte(m0)
	bad[40] ^= 1
	cryErr(t, Noise_readMessage(string(bad), res), "authentication")

	// The initiator targets a different responder key: the responder cannot
	// decrypt the initiator's static key.
	other := cryOk(t, runCryptoTaskAny(t, Kx_generate(nil)))
	iS := cryOk(t, runCryptoTaskAny(t, Kx_generate(nil)))
	rS := cryOk(t, runCryptoTaskAny(t, Kx_generate(nil)))
	wrong := cryOk(t, runCryptoTaskAny(t, Noise_initiator(iS, Kx_publicKey(other), "")))
	res2 := cryOk(t, runCryptoTaskAny(t, Noise_responder(rS, "")))
	_, w0 := tuple(t, Noise_writeMessage("", wrong))
	cryErr(t, Noise_readMessage(w0, res2), "authentication")

	// A different prologue on each side fails the first message.
	a := cryOk(t, runCryptoTaskAny(t, Noise_initiator(iS, Kx_publicKey(rS), "v1")))
	b := cryOk(t, runCryptoTaskAny(t, Noise_responder(rS, "v2")))
	_, a0 := tuple(t, Noise_writeMessage("", a))
	cryErr(t, Noise_readMessage(a0, b), "authentication")

	// A low-order ephemeral in the first message is refused.
	c := cryOk(t, runCryptoTaskAny(t, Noise_responder(rS, "")))
	cryErr(t, Noise_readMessage(strings.Repeat("\x00", 32+48+16), c), "low-order")

	// Short messages, and out-of-turn calls.
	d := cryOk(t, runCryptoTaskAny(t, Noise_responder(rS, "")))
	cryErr(t, Noise_readMessage("short", d), "too short")
	e := cryOk(t, runCryptoTaskAny(t, Noise_responder(rS, "")))
	cryErr(t, Noise_writeMessage("", e), "turn")
	cryErr(t, Noise_transport(cryOk(t, runCryptoTaskAny(t, Noise_responder(rS, "")))), "not complete")

	// Transport: a tampered message fails and the value stays usable.
	ini2, res3, _ := noiseKernelPair(t)
	ini2, h0 := tuple(t, Noise_writeMessage("", ini2))
	res3, _ = tuple(t, Noise_readMessage(h0, res3))
	res3, h1 := tuple(t, Noise_writeMessage("", res3))
	ini2, _ = tuple(t, Noise_readMessage(h1, ini2))
	it := cryOk(t, Noise_transport(ini2))
	rt_ := cryOk(t, Noise_transport(res3))
	_, ct := tuple(t, Noise_encrypt("secret", it))
	badCt := []byte(ct)
	badCt[0] ^= 1
	cryErr(t, Noise_decrypt(string(badCt), rt_), "authentication")
	if _, pt := tuple(t, Noise_decrypt(ct, rt_)); pt != "secret" {
		t.Fatalf("the genuine message did not decrypt after a rejected one")
	}
	// Oversized payload.
	cryErr(t, Noise_encrypt(strings.Repeat("x", noiseMaxMsg), it), "65535")
}

func TestNoiseStateRedacts(t *testing.T) {
	ini, _, _ := noiseKernelPair(t)
	for _, verb := range []string{"%v", "%d", "%x", "%+v", "%#v"} {
		if s := fmt.Sprintf(verb, ini); !strings.Contains(s, "REDACTED") {
			t.Fatalf("handshake printed %q with %s", s, verb)
		}
	}
}
