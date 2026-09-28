package rt

import (
	"strings"
	"testing"

	"golang.org/x/crypto/curve25519"
)

// Vectors: draft-irtf-cfrg-cpace-21, Appendix A (string helpers) and
// Appendix B.1 (CPACE-X25519-SHA512).

func TestCpaceStringHelpers(t *testing.T) {
	if got := cpacePrependLen(nil); string(got) != "\x00" {
		t.Fatalf("prepend_len(b\"\") = %x", got)
	}
	if got := cpacePrependLen([]byte("1234")); string(got) != unhex(t, "0431323334") {
		t.Fatalf("prepend_len(b\"1234\") = %x", got)
	}
	r127 := make([]byte, 127)
	r128 := make([]byte, 128)
	for i := range r128 {
		r128[i] = byte(i)
		if i < 127 {
			r127[i] = byte(i)
		}
	}
	if got := cpacePrependLen(r127); got[0] != 0x7f || len(got) != 128 {
		t.Fatalf("prepend_len(range(127)) header %x", got[:2])
	}
	if got := cpacePrependLen(r128); got[0] != 0x80 || got[1] != 0x01 || len(got) != 130 {
		t.Fatalf("prepend_len(range(128)) header %x", got[:3])
	}
	if got := cpaceLvCat([]byte("1234"), []byte("5"), nil, []byte("678")); string(got) != unhex(t, "043132333401350003363738") {
		t.Fatalf("lv_cat = %x", got)
	}
	tr := append(cpaceLvCat([]byte("123"), []byte("PartyA")), cpaceLvCat([]byte("234"), []byte("PartyB"))...)
	if string(tr) != unhex(t, "03313233065061727479410332333406506172747942") {
		t.Fatalf("transcript_ir = %x", tr)
	}
	// The parser reads back what prepend_len wrote, including a two-byte length.
	if d, rest, ok := cpaceReadLv(cpacePrependLen(r128)); !ok || len(d) != 128 || len(rest) != 0 {
		t.Fatalf("readLv of a 128-byte field failed")
	}
}

const (
	cpPRS = "Password"
	cpCI  = "0b415f696e69746961746f720b425f726573706f6e646572"
	cpSID = "7e4b4791d6a8ef019b936c79fb7f2c57"
	cpYa  = "21b4f4bd9e64ed355c3eb676a28ebedaf6d8f17bdc365995b319097153044080"
	cpYb  = "848b0779ff415f0af4ea14df9dd1d3c29ac41d836c7808896c4eba19c51ac40a"
)

func TestCpaceDraftVectorsX25519Sha512(t *testing.T) {
	prs, ci, sid := []byte(cpPRS), []byte(unhex(t, cpCI)), []byte(unhex(t, cpSID))
	gs := cpaceGeneratorString(prs, ci, sid)
	if len(gs) != 170 {
		t.Fatalf("generator string length %d, want 170", len(gs))
	}
	if !strings.HasPrefix(string(gs), unhex(t, "0843506163653235350850617373776f72646d00")) {
		t.Fatalf("generator string prefix %x", gs[:20])
	}
	g := cpaceGenerator(prs, ci, sid)
	if string(g) != unhex(t, "d04bf6d41f6a289632a2e929fa29bebd51092512a7829fdde7d314b62f05a73f") {
		t.Fatalf("generator g = %x", g)
	}
	st, msgA, err := cpaceStartWith(prs, ci, sid, []byte("ADa"), []byte(unhex(t, cpYa)))
	if err != nil {
		t.Fatal(err)
	}
	wantYa := unhex(t, "1d13c89278cdadd826f6d8d7f887701430f8380ddc17611cdd6dc989ce0c9f32")
	if yA, ad, _ := cpaceParse(msgA); string(yA) != wantYa || string(ad) != "ADa" {
		t.Fatalf("Ya = %x", yA)
	}
	isk, msgB, err := cpaceRespondWith(prs, ci, sid, []byte("ADb"), msgA, []byte(unhex(t, cpYb)))
	if err != nil {
		t.Fatal(err)
	}
	wantYb := unhex(t, "248cccf6d5cdc3646f0ad593f9e6cef4e69d4945f8372e623512ecea32185623")
	if yB, _, _ := cpaceParse(msgB); string(yB) != wantYb {
		t.Fatalf("Yb = %x", yB)
	}
	tr := append(append([]byte(nil), msgA...), msgB...)
	if string(tr) != unhex(t, "201d13c89278cdadd826f6d8d7f887701430f8380ddc17611cdd6dc989ce0c9f320341446120248cccf6d5cdc3646f0ad593f9e6cef4e69d4945f8372e623512ecea3218562303414462") {
		t.Fatalf("transcript_ir = %x", tr)
	}
	wantIsk := unhex(t, "6e19b875f7a561d6b3ca3dbb9ef42ac55de3e717881018204b8922b4d5e53bb2aa82c300bea7b65d2b671da71922ddf6472301b79bc270adfa8bf413285f2263")
	if string(isk) != wantIsk {
		t.Fatalf("responder ISK = %x", isk)
	}
	iskA, err := st.finish(msgB)
	if err != nil || string(iskA) != wantIsk {
		t.Fatalf("initiator ISK = %x (%v)", iskA, err)
	}
	// K itself.
	k, _ := kxShared([]byte(unhex(t, cpYa)), []byte(wantYb))
	if string(k) != unhex(t, "5b067effbdc0b2a0e1d907b21ebb25cfedb96a852179a847c37e43ee71322c6b") {
		t.Fatalf("K = %x", k)
	}
}

// Appendix B.1.10: scalar_mult_vfy on low-order and non-canonical points.
func TestCpaceDraftLowOrderVectors(t *testing.T) {
	s := []byte(unhex(t, "af46e36bf0527c9d3b16154b82465edd62144c0ac1fc5a18506a2244ba449aff"))
	cases := []struct{ u, q string }{
		{"0000000000000000000000000000000000000000000000000000000000000000", ""},
		{"0100000000000000000000000000000000000000000000000000000000000000", ""},
		{"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", ""},
		{"e0eb7a7c3b41b8ae1656e3faf19fc46ada098deb9c32b1fd866205165f49b800", ""},
		{"5f9c95bca3508c24b1d0b1559c83ef5b04445cc4581c8e86d8224eddd09f1157", ""},
		{"edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", ""},
		{"daffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "d8e2c776bbacd510d09fd9278b7edcd25fc5ae9adfba3b6e040e8d3b71b21806"},
		{"eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", ""},
		{"dbffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "c85c655ebe8be44ba9c0ffde69f2fe10194458d137f09bbff725ce58803cdb38"},
		{"d9ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "db64dafa9b8fdd136914e61461935fe92aa372cb056314e1231bc4ec12417456"},
		{"cdeb7a7c3b41b8ae1656e3faf19fc46ada098deb9c32b1fd866205165f49b880", "e062dcd5376d58297be2618c7498f55baa07d7e03184e8aada20bca28888bf7a"},
		{"4c9c95bca3508c24b1d0b1559c83ef5b04445cc4581c8e86d8224eddd09f11d7", "993c6ad11c4c29da9a56f7691fd0ff8d732e49de6250b6c2e80003ff4629a175"},
	}
	for i, c := range cases {
		got, err := kxShared(s, []byte(unhex(t, c.u)))
		if c.q == "" {
			if err == nil {
				t.Fatalf("u%x: a low-order point was accepted", i)
			}
			// And a CPace message carrying it aborts.
			msg := cpaceLvCat([]byte(unhex(t, c.u)), []byte("AD"))
			if _, _, err := cpaceRespondWith([]byte(cpPRS), nil, nil, nil, msg, s); err == nil {
				t.Fatalf("u%x: respond accepted a low-order share", i)
			}
			continue
		}
		if err != nil || string(got) != unhex(t, c.q) {
			t.Fatalf("u%x: q = %x (%v)", i, got, err)
		}
		raw, _ := curve25519.X25519(s, []byte(unhex(t, c.u)))
		if string(raw) != string(got) {
			t.Fatalf("u%x: differs from curve25519.X25519", i)
		}
	}
}

func TestCpaceKernelsRoundTripAndMismatch(t *testing.T) {
	pw := Secret{v: "correct horse"}
	st, msgA := tuple(t, runCryptoTaskAny(t, Cpace_start(pw, "ci", "sid-1", "alice")))
	if ad := cryOk(t, Cpace_messageData(msgA)).(string); ad != "alice" {
		t.Fatalf("messageData = %q", ad)
	}
	iskB, msgB := tuple(t, runCryptoTaskAny(t, Cpace_respond(pw, "ci", "sid-1", "bob", msgA)))
	iskA := cryOk(t, Cpace_finish(st, msgB))
	if secretReveal(iskA) != secretReveal(iskB) || len(secretReveal(iskA)) != 64 {
		t.Fatalf("the two sides derived different keys")
	}
	cryErr(t, Cpace_finish(st, msgB), "already finished")

	// A wrong password, a different session id or a different channel id all
	// give unrelated keys (and nothing else — CPace has no error for them).
	for _, bad := range []struct{ pw, ci, sid string }{
		{"wrong horse", "ci", "sid-2"}, {"correct horse", "ci", "sid-other"}, {"correct horse", "other-ci", "sid-2"},
	} {
		st2, a := tuple(t, runCryptoTaskAny(t, Cpace_start(pw, "ci", "sid-2", "alice")))
		kb, b := tuple(t, runCryptoTaskAny(t, Cpace_respond(Secret{v: bad.pw}, bad.ci, bad.sid, "bob", a)))
		ka := cryOk(t, Cpace_finish(st2, b))
		if secretReveal(ka) == secretReveal(kb) {
			t.Fatalf("mismatched inputs %+v produced the same key", bad)
		}
	}
	// Garbage and truncated messages are refused.
	cryErr(t, runCryptoTaskAny(t, Cpace_respond(pw, "", "", "", "garbage")), "not a CPace message")
	st3, _ := tuple(t, runCryptoTaskAny(t, Cpace_start(pw, "", "", "")))
	cryErr(t, Cpace_finish(st3, msgB[:10]), "not a CPace message")
	cryErr(t, Cpace_messageData("\x05ab"), "not a CPace message")
	// The pending state redacts itself.
	if s := AsString(st); !strings.Contains(s, "REDACTED") {
		t.Fatalf("Pending printed %q", s)
	}
}
