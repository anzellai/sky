package rt

import (
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

// B-5. The two existing derivations stay byte-identical (stored data made
// with them must stay decryptable): a golden vector, computed with an
// independent PBKDF2 (Python hashlib), pins them. The new
// Crypto.keyFromPasswordStrong enforces OWASP's floor.

const pbkdf2GoldenPassword = "correct horse battery staple"
const pbkdf2GoldenSalt = "0123456789abcdef"

func TestAesKeyFromPasswordIsStable(t *testing.T) {
	want := "090105d3788cadab9c12509fa1ba1d46a91a158d7a1779b114322f3fd5a825cb"
	for name, f := range map[string]func(any, any) any{
		"aesKeyFromPassword":    Crypto_aesKeyFromPassword,
		"chachaKeyFromPassword": Crypto_chachaKeyFromPassword,
	} {
		got := hex.EncodeToString([]byte(secretReveal(f(Secret{v: pbkdf2GoldenPassword}, pbkdf2GoldenSalt))))
		if got != want {
			t.Fatalf("%s changed its output: %s, want %s", name, got, want)
		}
	}
}

func runStrong(t *testing.T, iterations int, salt, password string) SkyResult[any, any] {
	t.Helper()
	opts := map[string]any{"Iterations": iterations, "Salt": salt}
	thunk, ok := Crypto_keyFromPasswordStrong(opts, Secret{v: password}).(func() any)
	if !ok {
		t.Fatal("keyFromPasswordStrong must be a Task")
	}
	return thunk().(SkyResult[any, any])
}

func TestKeyFromPasswordStrongMatchesPBKDF2AtTheFloor(t *testing.T) {
	r := runStrong(t, 600_000, pbkdf2GoldenSalt, pbkdf2GoldenPassword)
	if r.Tag != 0 {
		t.Fatalf("Err: %s", errorMessage(r.ErrValue))
	}
	got := hex.EncodeToString([]byte(secretReveal(r.OkValue)))
	if got != "6c4a646aad10d067add5fb79d9078a16da83d50f81670a8e7593b249e6d94936" {
		t.Fatalf("600k-iteration key = %s", got)
	}
}

func TestKeyFromPasswordStrongRefusesWeakParameters(t *testing.T) {
	if r := runStrong(t, 100_000, pbkdf2GoldenSalt, "pw"); r.Tag == 0 || !strings.Contains(errorMessage(r.ErrValue), "600000") {
		t.Fatalf("100k iterations accepted: %#v", r)
	}
	if r := runStrong(t, 600_000, "short", "pw"); r.Tag == 0 || !strings.Contains(errorMessage(r.ErrValue), "16") {
		t.Fatalf("a 5-byte salt accepted: %#v", r)
	}
	if r := runStrong(t, 1<<31, pbkdf2GoldenSalt, "pw"); r.Tag == 0 {
		t.Fatal("an unbounded iteration count accepted")
	}
}

func TestShortSaltIsWarnedOnce(t *testing.T) {
	shortSaltWarning = sync.Once{}
	logs := captureLog(t)
	_ = Crypto_aesKeyFromPassword(Secret{v: "pw"}, "salt")
	_ = Crypto_chachaKeyFromPassword(Secret{v: "pw"}, "salt")
	n := strings.Count(logs.String(), "shorter than 16 bytes")
	if n != 1 {
		t.Fatalf("short-salt warning printed %d times, want once per process", n)
	}
}
