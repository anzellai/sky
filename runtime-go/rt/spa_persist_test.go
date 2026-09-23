package rt

import (
	"encoding/json"
	"testing"
)

// TestSpaMergeStoredOverSeed covers the boot merge decision (spa_persist.go):
// the stored model wins for scratch fields, the SSR seed wins for the protected
// (session) field, an oversized or corrupt stored blob falls back to the seed,
// and an empty seed restores the stored model as-is.
func TestSpaMergeStoredOverSeed(t *testing.T) {
	const cap = 1_500_000

	t.Run("stored over seed, session kept from seed", func(t *testing.T) {
		// Stored carries a stale session ("old") plus scratch state (cart, page).
		// The seed carries the server-verified session ("srv") and empty scratch.
		stored := `{"session":"old","cart":["a","b"],"page":"checkout"}`
		seed := `{"session":"srv","cart":[],"page":"home"}`
		merged, useIt := spaMergeStoredOverSeed(stored, seed, []string{"session"}, cap)
		if !useIt {
			t.Fatalf("useIt = false, want true")
		}
		fields := decodeObj(t, merged)
		if got := string(fields["session"]); got != `"srv"` {
			t.Errorf("session = %s, want \"srv\" (seed must win)", got)
		}
		if got := string(fields["cart"]); got != `["a","b"]` {
			t.Errorf("cart = %s, want [\"a\",\"b\"] (stored must win)", got)
		}
		if got := string(fields["page"]); got != `"checkout"` {
			t.Errorf("page = %s, want \"checkout\" (stored must win)", got)
		}
	})

	t.Run("oversized stored falls back to seed", func(t *testing.T) {
		stored := `{"session":"old","blob":"xxxxxxxxxx"}`
		_, useIt := spaMergeStoredOverSeed(stored, `{"session":"srv"}`, []string{"session"}, 10)
		if useIt {
			t.Errorf("useIt = true, want false for a blob over the cap")
		}
	})

	t.Run("corrupt stored falls back to seed", func(t *testing.T) {
		for _, bad := range []string{`not json`, `[1,2,3]`, `"a string"`, `42`, ``} {
			if _, useIt := spaMergeStoredOverSeed(bad, `{"session":"srv"}`, []string{"session"}, cap); useIt {
				t.Errorf("useIt = true for corrupt stored %q, want false", bad)
			}
		}
	})

	t.Run("empty seed restores stored as-is", func(t *testing.T) {
		stored := `{"session":"old","cart":["a"]}`
		merged, useIt := spaMergeStoredOverSeed(stored, "", []string{"session"}, cap)
		if !useIt {
			t.Fatalf("useIt = false, want true (no seed → restore stored)")
		}
		if merged != stored {
			t.Errorf("merged = %s, want the stored blob verbatim %s", merged, stored)
		}
	})

	t.Run("empty protected fields restores whole stored model", func(t *testing.T) {
		stored := `{"count":7,"name":"x"}`
		seed := `{"count":0,"name":""}`
		merged, useIt := spaMergeStoredOverSeed(stored, seed, nil, cap)
		if !useIt {
			t.Fatalf("useIt = false, want true")
		}
		fields := decodeObj(t, merged)
		if string(fields["count"]) != "7" || string(fields["name"]) != `"x"` {
			t.Errorf("merged = %s, want the whole stored model (no field protected)", merged)
		}
	})

	// K5: a field ONLY server branches write comes from the SSR seed on a
	// reload (server truth), every other field from localStorage. The model
	// codec writes snake_case keys, so a camelCase field matches its snake key.
	t.Run("server-only fields come from the seed, client fields from storage", func(t *testing.T) {
		stored := `{"note":"kept","server_value":"stale","session":"old"}`
		seed := `{"note":"","server_value":"fresh","session":"srv"}`
		wins := spaSeedWinsFields([]string{"session"}, []string{"serverValue"})
		merged, useIt := spaMergeStoredOverSeed(stored, seed, wins, cap)
		if !useIt {
			t.Fatalf("useIt = false, want true")
		}
		fields := decodeObj(t, merged)
		if string(fields["note"]) != `"kept"` {
			t.Errorf("client field not restored: %s", merged)
		}
		if string(fields["server_value"]) != `"fresh"` {
			t.Errorf("server-only field not taken from the seed: %s", merged)
		}
		if string(fields["session"]) != `"srv"` {
			t.Errorf("session not taken from the seed: %s", merged)
		}
	})
}

// TestSpaSessionClearedByStep covers the sign-out signal (spa_persist.go): a
// protected field going non-null -> null/absent is a sign-out; every other
// transition (unchanged, sign-in, scratch change) is not.
// TestSpaFirstPaintPlan covers the SSR first-paint decision (spa_persist.go):
// a restore that changes the model needs the two-step paint (hydrate the seed,
// then diff-patch to the restored model), while a restore that leaves the model
// equal to the seed — or no seed / no restored blob — needs only a single render.
func TestSpaFirstPaintPlan(t *testing.T) {
	seed := `{"session":"srv","messages":[],"loaded":false}`

	t.Run("restored differs from seed → two-step", func(t *testing.T) {
		restored := `{"session":"srv","messages":["a","b","c"],"loaded":true}`
		if !spaFirstPaintPlan(seed, restored) {
			t.Errorf("twoStep = false, want true when the restored model differs")
		}
	})

	t.Run("restored byte-equal to seed → single render", func(t *testing.T) {
		if spaFirstPaintPlan(seed, seed) {
			t.Errorf("twoStep = true, want false when restored == seed")
		}
	})

	t.Run("restored equal but reordered/whitespaced → single render", func(t *testing.T) {
		// Same content, different key order and spacing — a JSON round-trip must
		// see these as equal so a re-encode with a different key order does not
		// force a needless second render.
		reordered := `{ "loaded": false , "messages": [] , "session": "srv" }`
		if spaFirstPaintPlan(seed, reordered) {
			t.Errorf("twoStep = true, want false for a semantically identical model")
		}
	})

	t.Run("empty seed → single render (no server markup to diverge from)", func(t *testing.T) {
		if spaFirstPaintPlan("", `{"messages":["a"]}`) {
			t.Errorf("twoStep = true, want false when the seed is empty")
		}
	})

	t.Run("empty restored → single render (no restore happened)", func(t *testing.T) {
		if spaFirstPaintPlan(seed, "") {
			t.Errorf("twoStep = true, want false when there is no restored model")
		}
	})

	t.Run("malformed restored → two-step (fail toward the always-correct patch)", func(t *testing.T) {
		if !spaFirstPaintPlan(seed, `{"messages":`) {
			t.Errorf("twoStep = false, want true when a blob will not parse")
		}
	})
}

func TestSpaSessionClearedByStep(t *testing.T) {
	sess := []string{"session"}

	cases := []struct {
		name       string
		prev, next string
		want       bool
	}{
		{"Just -> Nothing (value -> null)", `{"session":"u1"}`, `{"session":null}`, true},
		{"Just -> absent", `{"session":"u1"}`, `{"other":1}`, true},
		{"unchanged value", `{"session":"u1"}`, `{"session":"u1"}`, false},
		{"changed value (still signed in)", `{"session":"u1"}`, `{"session":"u2"}`, false},
		{"Nothing -> Just (sign in)", `{"session":null}`, `{"session":"u1"}`, false},
		{"absent -> Just (sign in)", `{"other":1}`, `{"session":"u1"}`, false},
		{"both null", `{"session":null}`, `{"session":null}`, false},
		{"corrupt prev is safe (no spurious sign-out)", `nope`, `{"session":null}`, false},
		{"empty prev is safe", ``, `{"session":null}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := spaSessionClearedByStep(c.prev, c.next, sess); got != c.want {
				t.Errorf("spaSessionClearedByStep(%q,%q) = %v, want %v", c.prev, c.next, got, c.want)
			}
		})
	}

	t.Run("no session fields never signs out", func(t *testing.T) {
		if spaSessionClearedByStep(`{"a":1}`, `{"a":null}`, nil) {
			t.Errorf("want false when no session field is protected")
		}
	})
}

// decodeObj is a tiny test helper that decodes a JSON object into its raw fields.
func decodeObj(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return m
}
