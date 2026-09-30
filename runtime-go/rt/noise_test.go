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

// Noise_IK_25519_ChaChaPoly_BLAKE2s vectors, from the same source as the
// SHA256 ones (vectors.txt of github.com/flynn/noise v1.1.0, the four
// non-PSK IK entries). The static and ephemeral keys are the same as above.
var noiseIKBlake2sVectors = []struct {
	prologue string
	payloads [4]string
	cts      [4]string
}{
	{"",
		[4]string{"", "", "79656c6c6f777375626d6172696e65", "7375626d6172696e6579656c6c6f77"},
		[4]string{
			"358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd166254c9f0dff42c86abe5677abe74f6c87301577dbc1f3ffb2213827ca694a057fdbbff7f7350265fe61102c24d7d7a7e960ba8b90a679895087c7d28b1d6703f9727",
			"64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d4846622bf9c6171ddd4c8f682080b03504eee",
			"595694f9be48f03790f699455c84578b31d14a7baedfd736d73c53f66a5657",
			"621ae446b11fda3cf08e56102dac9324dee37a4e536cdc878e8b454d98bcf2",
		}},
	{"",
		[4]string{"746573745f6d73675f30", "746573745f6d73675f31", "79656c6c6f777375626d6172696e65", "7375626d6172696e6579656c6c6f77"},
		[4]string{
			"358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd166254c9f0dff42c86abe5677abe74f6c87301577dbc1f3ffb2213827ca694a057fdbbff7f7350265fe61102c24d7d7a7e960b7316fcb3b0687be852fd2fba8969816fbfaa8b459d0b59e8a42f",
			"64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d484667f1d8bd2b9b659695f9077e7062bb0b9e7c08fd627913be183c3",
			"595694f9be48f03790f699455c84578b31d14a7baedfd736d73c53f66a5657",
			"621ae446b11fda3cf08e56102dac9324dee37a4e536cdc878e8b454d98bcf2",
		}},
	{"6e6f74736563726574",
		[4]string{"", "", "79656c6c6f777375626d6172696e65", "7375626d6172696e6579656c6c6f77"},
		[4]string{
			"358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd166254c9f0dff42c86abe5677abe74f6c87301577dbc1f3ffb2213827ca694a057fdbbacac81d639bfae65c7827558f90acd27f14e182372e5bee2fa04eca3d32f09a9",
			"64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d48466bbaba571a4d366dfe3958808b6a298f9",
			"595694f9be48f03790f699455c84578b31d14a7baedfd736d73c53f66a5657",
			"621ae446b11fda3cf08e56102dac9324dee37a4e536cdc878e8b454d98bcf2",
		}},
	{"6e6f74736563726574",
		[4]string{"746573745f6d73675f30", "746573745f6d73675f31", "79656c6c6f777375626d6172696e65", "7375626d6172696e6579656c6c6f77"},
		[4]string{
			"358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd166254c9f0dff42c86abe5677abe74f6c87301577dbc1f3ffb2213827ca694a057fdbbacac81d639bfae65c7827558f90acd277316fcb3b0687be852fd7e392456bb6cbe070c749f1bd7c55fc2",
			"64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d484667f1d8bd2b9b659695f90e35beaf5a5f5f1e7c83aa3194a2430cd",
			"595694f9be48f03790f699455c84578b31d14a7baedfd736d73c53f66a5657",
			"621ae446b11fda3cf08e56102dac9324dee37a4e536cdc878e8b454d98bcf2",
		}},
}

func TestNoiseIKBlake2sVectors(t *testing.T) {
	for vi, v := range noiseIKBlake2sVectors {
		is, rs := unhex(t, noiseInitStatic), unhex(t, noiseRespStatic)
		prologue := unhex(t, v.prologue)
		ini := noiseNewSuite(noiseSuiteBLAKE2s, true, is, kxPublic(rs), prologue, unhex(t, noiseInitEph))
		res := noiseNewSuite(noiseSuiteBLAKE2s, false, rs, "", prologue, unhex(t, noiseRespEph))
		ini, m0, err := ini.writeMessage([]byte(unhex(t, v.payloads[0])))
		if err != nil || string(m0) != unhex(t, v.cts[0]) {
			t.Fatalf("BLAKE2s vector %d msg_0: %v\n got %x", vi, err, m0)
		}
		res, p0, err := res.readMessage(m0)
		if err != nil || string(p0) != unhex(t, v.payloads[0]) {
			t.Fatalf("BLAKE2s vector %d: responder could not read msg_0: %v", vi, err)
		}
		res, m1, err := res.writeMessage([]byte(unhex(t, v.payloads[1])))
		if err != nil || string(m1) != unhex(t, v.cts[1]) {
			t.Fatalf("BLAKE2s vector %d msg_1: %v\n got %x", vi, err, m1)
		}
		ini, p1, err := ini.readMessage(m1)
		if err != nil || string(p1) != unhex(t, v.payloads[1]) {
			t.Fatalf("BLAKE2s vector %d: initiator could not read msg_1: %v", vi, err)
		}
		it, err := ini.split()
		if err != nil {
			t.Fatal(err)
		}
		rt_, err := res.split()
		if err != nil {
			t.Fatal(err)
		}
		it, m2, err := it.encrypt([]byte(unhex(t, v.payloads[2])))
		if err != nil || string(m2) != unhex(t, v.cts[2]) {
			t.Fatalf("BLAKE2s vector %d msg_2: %v\n got %x", vi, err, m2)
		}
		rt_, p2, err := rt_.decrypt(m2)
		if err != nil || string(p2) != unhex(t, v.payloads[2]) {
			t.Fatalf("BLAKE2s vector %d: msg_2 did not decrypt: %v", vi, err)
		}
		_, m3, err := rt_.encrypt([]byte(unhex(t, v.payloads[3])))
		if err != nil || string(m3) != unhex(t, v.cts[3]) {
			t.Fatalf("BLAKE2s vector %d msg_3: %v\n got %x", vi, err, m3)
		}
		if _, p3, err := it.decrypt(m3); err != nil || string(p3) != unhex(t, v.payloads[3]) {
			t.Fatalf("BLAKE2s vector %d: msg_3 did not decrypt: %v", vi, err)
		}
	}
}

// A round trip through the suite kernels, and the two suites do not
// interoperate: a BLAKE2s initiator's first message fails on a SHA256
// responder.
func TestNoiseKernelBlake2sRoundTripAndSuiteMismatch(t *testing.T) {
	iS := cryOk(t, runCryptoTaskAny(t, Kx_generate(nil)))
	rS := cryOk(t, runCryptoTaskAny(t, Kx_generate(nil)))
	ini := cryOk(t, runCryptoTaskAny(t, Noise_initiatorSuite("BLAKE2s", iS, Kx_publicKey(rS), "app v1")))
	res := cryOk(t, runCryptoTaskAny(t, Noise_responderSuite("BLAKE2s", rS, "app v1")))
	ini, m0 := tuple(t, Noise_writeMessage("hello", ini))
	res, p0 := tuple(t, Noise_readMessage(m0, res))
	if p0 != "hello" {
		t.Fatalf("payload 0 = %q", p0)
	}
	res, m1 := tuple(t, Noise_writeMessage("welcome", res))
	ini, p1 := tuple(t, Noise_readMessage(m1, ini))
	if p1 != "welcome" {
		t.Fatalf("payload 1 = %q", p1)
	}
	it := cryOk(t, Noise_transport(ini))
	rt_ := cryOk(t, Noise_transport(res))
	if Noise_handshakeHash(it) != Noise_handshakeHash(rt_) {
		t.Fatal("handshake hashes differ")
	}
	it, c := tuple(t, Noise_encrypt("ping", it))
	_, pt := tuple(t, Noise_decrypt(c, rt_))
	if pt != "ping" {
		t.Fatalf("transport payload = %q", pt)
	}
	_ = it

	ini2 := cryOk(t, runCryptoTaskAny(t, Noise_initiatorSuite("BLAKE2s", iS, Kx_publicKey(rS), "app v1")))
	sha := cryOk(t, runCryptoTaskAny(t, Noise_responderSuite("SHA256", rS, "app v1")))
	_, m := tuple(t, Noise_writeMessage("hello", ini2))
	if tag, _, _ := anyResultView(Noise_readMessage(m, sha)); tag == 0 {
		t.Fatal("a SHA256 responder accepted a BLAKE2s initiator's message")
	}
	// An unknown suite name is an Err, never a silent default.
	if tag, _, _ := anyResultView(runCryptoTaskAny(t, Noise_responderSuite("MD5", rS, ""))); tag == 0 {
		t.Fatal("an unknown suite was accepted")
	}
}

// B-7: interop vectors with an independent implementation that cover REKEY.
// The IK vectors above use two transport messages, so nothing pinned
// rekeySend / rekeyReceive against another implementation. These were made
// with github.com/flynn/noise v1.1.0 (the cacophony keys above, prologue
// "sky-interop", payloads "m0" / "m1"): four initiator→responder messages
// "a0".."a3" and four responder→initiator messages "b0".."b3", each side
// rekeying its sending cipher before message 2.
var noiseRekeyVectors = []struct {
	suite  string
	m0, m1 string
	a, b   [4]string
	hash   string
}{
	{"SHA256",
		"358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd1662544f8445e5dc2467b1e32653192d05dee85c4781bf0dd8d33ceebb5905a7a069f050e15e9de78c01472775e40165c647fe1a624265c21434c56e1b8307a54c93629e35",
		"64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d48466d21e0b80ebe9c749f691775552ffac5108ee",
		[4]string{"3a39dc6051436e78fc9556589d449edbeca9", "993e334ecb15b629991e34a7af78a98f805c", "e793d86d531122e7a0c38cd18d2fb352a943", "30aca3e104c8df578d5e8d97c750dc18e62e"},
		[4]string{"819d54636e2ae732b23561df141d510604e5", "6a603ad250225a7500313c1a68b0815e5a0b", "c6adb9fd81f343f4c26f2ee0a73e974a01e3", "8fe9b7a81d243dd6b95cce33adc40650d7b1"},
		"f6dfca5b67cd7ded94ecf9882fc7ec7393789f10b5a815da72d7d2a403268e89"},
	{"BLAKE2s",
		"358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd166254c9f0dff42c86abe5677abe74f6c87301577dbc1f3ffb2213827ca694a057fdbb044b916ec25e34a2e7da5b5ea590cb7c6a4303db22d330495fa0f5815d26d6de7cd4",
		"64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d4846666495b75f040bc25871c262f54385c9adfe2",
		[4]string{"410304a0a221fd9ee9abf185cfef8612b4ff", "992f56996874cacc08f87038a40239166fad", "e316b4a19bdda4a45581adbd6c4d67b46c3a", "780ebdd4100156ffe0a0bce749e47ee84486"},
		[4]string{"735f093562d8912a6dceef1a31b1c23cc075", "168153b176691808a859b2a8063e6377a6be", "f2d33c29620578717e6874a0242a0c03e617", "22de877c4b663fea3296a290222f62d771c3"},
		"65b46ce4aaf4ae188ba17cb4182498d92e47b0038bd23d51317a22dc1d40ee61"},
}

func TestNoiseRekeyInteropVectors(t *testing.T) {
	for _, v := range noiseRekeyVectors {
		suite, err := noiseSuiteByName(v.suite)
		if err != nil {
			t.Fatal(err)
		}
		is, rs := unhex(t, noiseInitStatic), unhex(t, noiseRespStatic)
		ini := noiseNewSuite(suite, true, is, kxPublic(rs), "sky-interop", unhex(t, noiseInitEph))
		res := noiseNewSuite(suite, false, rs, "", "sky-interop", unhex(t, noiseRespEph))
		ini, m0, err := ini.writeMessage([]byte("m0"))
		if err != nil || fmt.Sprintf("%x", m0) != v.m0 {
			t.Fatalf("%s m0: %v\n got %x", v.suite, err, m0)
		}
		res, _, err = res.readMessage(m0)
		if err != nil {
			t.Fatal(err)
		}
		res, m1, err := res.writeMessage([]byte("m1"))
		if err != nil || fmt.Sprintf("%x", m1) != v.m1 {
			t.Fatalf("%s m1: %v\n got %x", v.suite, err, m1)
		}
		ini, _, err = ini.readMessage(m1)
		if err != nil {
			t.Fatal(err)
		}
		var it, rt_ any
		it = cryOk(t, Noise_transport(ini))
		rt_ = cryOk(t, Noise_transport(res))
		if got := fmt.Sprintf("%x", Noise_handshakeHash(it).(string)); got != v.hash {
			t.Fatalf("%s handshake hash %s, want flynn's %s", v.suite, got, v.hash)
		}
		for i := 0; i < 4; i++ {
			if i == 2 {
				it = cryOk(t, Noise_rekeySend(it))
				rt_ = cryOk(t, Noise_rekeyReceive(rt_))
			}
			e := Noise_encrypt(fmt.Sprintf("a%d", i), it).(SkyResult[any, any])
			tup := e.OkValue.(SkyTuple2)
			it = tup.V0
			if got := fmt.Sprintf("%x", tup.V1.(string)); e.Tag != 0 || got != v.a[i] {
				t.Fatalf("%s a%d: got %s, want flynn's %s", v.suite, i, got, v.a[i])
			}
			d := Noise_decrypt(unhex(t, v.a[i]), rt_).(SkyResult[any, any])
			if d.Tag != 0 || d.OkValue.(SkyTuple2).V1.(string) != fmt.Sprintf("a%d", i) {
				t.Fatalf("%s: responder could not read flynn's a%d", v.suite, i)
			}
			rt_ = d.OkValue.(SkyTuple2).V0
		}
		for i := 0; i < 4; i++ {
			if i == 2 {
				rt_ = cryOk(t, Noise_rekeySend(rt_))
				it = cryOk(t, Noise_rekeyReceive(it))
			}
			e := Noise_encrypt(fmt.Sprintf("b%d", i), rt_).(SkyResult[any, any])
			tup := e.OkValue.(SkyTuple2)
			rt_ = tup.V0
			if got := fmt.Sprintf("%x", tup.V1.(string)); e.Tag != 0 || got != v.b[i] {
				t.Fatalf("%s b%d: got %s, want flynn's %s", v.suite, i, got, v.b[i])
			}
			d := Noise_decrypt(unhex(t, v.b[i]), it).(SkyResult[any, any])
			if d.Tag != 0 || d.OkValue.(SkyTuple2).V1.(string) != fmt.Sprintf("b%d", i) {
				t.Fatalf("%s: initiator could not read flynn's b%d", v.suite, i)
			}
			it = d.OkValue.(SkyTuple2).V0
		}
	}
}
