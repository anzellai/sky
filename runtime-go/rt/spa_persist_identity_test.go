package rt

import (
	"testing"
)

// The identity rule of Sky.Spa client persistence (spa_persist.go
// spaRestoreStored / spaPersistWrite). A persisted model restores ONLY when the
// identity it was written under (its protected session fields) equals the
// identity the SSR seed carries. Found in a real app: one browser, two tabs on
// the same origin, a clinic user signed in in tab A and a patient link (another
// session identity) in tab B. The ONE localStorage key held tab B's model, so a
// full load of tab A restored the patient's page and data under the
// practitioner's session.

// memKV is a map-backed spaKV, the host stand-in for window.localStorage.
type memKV map[string]string

func (m memKV) Get(k string) (string, bool) { v, ok := m[k]; return v, ok }
func (m memKV) Set(k, v string)             { m[k] = v }
func (m memKV) Remove(k string)             { delete(m, k) }

const (
	idCap          = 1_500_000
	legacyModelKey = "sky:spa:model"
	practitioner   = `{"session":{"kind":"practitioner","userId":"u1"},"page":"diary","note":"u1 private","basket":["x"]}`
	practitionerB  = `{"session":{"userId":"u1","kind":"practitioner"},"page":"home","note":"","basket":[]}`
	patient        = `{"session":{"kind":"patient","userId":"p1"},"page":"results","note":"p1 private","basket":["y"]}`
	patientSeed    = `{"session":{"kind":"patient","userId":"p1"},"page":"home","note":"","basket":[]}`
	anonymousSeed  = `{"session":null,"page":"home","note":"","basket":[]}`
	anonymousModel = `{"session":null,"page":"shop","note":"","basket":["apple"]}`
)

var sessionOnly = []string{"session"}

// write runs one persisted step as a fresh page lifetime would after booting
// from seed (the life the restore returned).
func writeAs(t *testing.T, st memKV, seed, model string) {
	t.Helper()
	_, _, life := spaRestoreStored(st, seed, sessionOnly, sessionOnly, idCap)
	spaPersistWrite(st, &life, model, sessionOnly, idCap)
}

func TestSpaRestoreStored_Identity(t *testing.T) {
	t.Run("another identity's model is never restored, and is removed", func(t *testing.T) {
		st := memKV{}
		writeAs(t, st, patientSeed, patient)
		merged, useIt, _ := spaRestoreStored(st, practitionerB, sessionOnly, sessionOnly, idCap)
		if useIt || merged != "" {
			t.Fatalf("restored %q for the practitioner from the patient's slot, want (\"\", false)", merged)
		}
		if len(st) != 0 {
			t.Errorf("storage after a mismatch = %v, want empty (the other identity's copy is removed)", st)
		}
	})

	t.Run("same identity restores as today (seed session, stored scratch)", func(t *testing.T) {
		st := memKV{}
		writeAs(t, st, practitionerB, practitioner)
		merged, useIt, life := spaRestoreStored(st, practitionerB, sessionOnly, sessionOnly, idCap)
		if !useIt {
			t.Fatalf("useIt = false, want true for the same identity (key order must not matter)")
		}
		f := decodeObj(t, merged)
		if string(f["page"]) != `"diary"` || string(f["note"]) != `"u1 private"` {
			t.Errorf("merged = %s, want the stored page and note", merged)
		}
		if string(f["session"]) != `{"userId":"u1","kind":"practitioner"}` {
			t.Errorf("session = %s, want the seed's bytes", f["session"])
		}
		if !life.known || life.id == "" {
			t.Errorf("life = %+v, want a known signed-in identity", life)
		}
	})

	t.Run("signed-in model, signed-out seed: no restore, copy removed", func(t *testing.T) {
		st := memKV{}
		writeAs(t, st, practitionerB, practitioner)
		if _, useIt, _ := spaRestoreStored(st, anonymousSeed, sessionOnly, sessionOnly, idCap); useIt {
			t.Fatalf("a signed-out load restored the signed-in model")
		}
		if len(st) != 0 {
			t.Errorf("storage = %v, want empty", st)
		}
	})

	t.Run("seed session absent (key missing) counts as signed out", func(t *testing.T) {
		st := memKV{}
		writeAs(t, st, practitionerB, practitioner)
		if _, useIt, _ := spaRestoreStored(st, `{"page":"home"}`, sessionOnly, sessionOnly, idCap); useIt {
			t.Fatalf("a seed with no session field restored the signed-in model")
		}
	})

	t.Run("anonymous to signed in: no carry-over, anonymous copy removed", func(t *testing.T) {
		st := memKV{}
		writeAs(t, st, anonymousSeed, anonymousModel)
		if len(st) != 1 {
			t.Fatalf("anonymous write stored %v, want one entry", st)
		}
		if _, useIt, _ := spaRestoreStored(st, practitionerB, sessionOnly, sessionOnly, idCap); useIt {
			t.Fatalf("the anonymous basket was restored into a signed-in identity")
		}
		if len(st) != 0 {
			t.Errorf("storage = %v, want empty", st)
		}
	})

	t.Run("anonymous to anonymous restores as today", func(t *testing.T) {
		st := memKV{}
		writeAs(t, st, anonymousSeed, anonymousModel)
		merged, useIt, _ := spaRestoreStored(st, anonymousSeed, sessionOnly, sessionOnly, idCap)
		if !useIt || string(decodeObj(t, merged)["basket"]) != `["apple"]` {
			t.Fatalf("anonymous reload = (%q, %v), want the stored basket", merged, useIt)
		}
	})

	t.Run("two tabs, two identities: the last writer never leaks into the other", func(t *testing.T) {
		st := memKV{}
		writeAs(t, st, practitionerB, practitioner) // tab A acts
		writeAs(t, st, patientSeed, patient)        // tab B acts
		merged, useIt, _ := spaRestoreStored(st, practitionerB, sessionOnly, sessionOnly, idCap)
		if useIt {
			t.Fatalf("tab A restored %s (tab B's model)", merged)
		}
		// Tab A then acts again and reloads: its own state comes back.
		writeAs(t, st, practitionerB, practitioner)
		merged, useIt, _ = spaRestoreStored(st, practitionerB, sessionOnly, sessionOnly, idCap)
		if !useIt || string(decodeObj(t, merged)["note"]) != `"u1 private"` {
			t.Fatalf("tab A second reload = (%q, %v), want its own note", merged, useIt)
		}
	})

	t.Run("no SSR seed: nothing to compare, stored restores as-is", func(t *testing.T) {
		st := memKV{}
		writeAs(t, st, practitionerB, practitioner)
		merged, useIt, life := spaRestoreStored(st, "", sessionOnly, sessionOnly, idCap)
		if !useIt || merged != practitioner {
			t.Fatalf("no-seed restore = (%q, %v), want the stored blob verbatim", merged, useIt)
		}
		if !life.known || life.id == "" {
			t.Errorf("life = %+v, want the stored identity", life)
		}
	})

	t.Run("no protected fields: one identity for everyone, restore as today", func(t *testing.T) {
		st := memKV{}
		_, _, life := spaRestoreStored(st, `{"n":0}`, nil, nil, idCap)
		spaPersistWrite(st, &life, `{"n":3}`, nil, idCap)
		merged, useIt, _ := spaRestoreStored(st, `{"n":0}`, nil, nil, idCap)
		if !useIt || merged != `{"n":3}` {
			t.Fatalf("restore = (%q, %v), want {\"n\":3}", merged, useIt)
		}
	})

	t.Run("corrupt stored blob: no restore", func(t *testing.T) {
		st := memKV{}
		writeAs(t, st, practitionerB, practitioner)
		for k := range st {
			st[k] = "not json"
		}
		if _, useIt, _ := spaRestoreStored(st, practitionerB, sessionOnly, sessionOnly, idCap); useIt {
			t.Fatalf("a corrupt blob was restored")
		}
	})
}

func TestSpaRestoreStored_LegacyKey(t *testing.T) {
	t.Run("an app with a session never restores the un-keyed legacy blob, and deletes it", func(t *testing.T) {
		for _, seed := range []string{practitionerB, anonymousSeed, patientSeed} {
			// The legacy blob is anonymous but carries a signed-out user's data.
			st := memKV{legacyModelKey: `{"session":null,"page":"results","note":"p1 private"}`}
			if merged, useIt, _ := spaRestoreStored(st, seed, sessionOnly, sessionOnly, idCap); useIt {
				t.Errorf("seed %s restored the legacy blob: %s", seed, merged)
			}
			if _, ok := st[legacyModelKey]; ok {
				t.Errorf("seed %s: legacy key not deleted", seed)
			}
		}
	})

	t.Run("an app with no session migrates the legacy blob once", func(t *testing.T) {
		st := memKV{legacyModelKey: `{"n":7}`}
		merged, useIt, _ := spaRestoreStored(st, `{"n":0}`, nil, nil, idCap)
		if !useIt || merged != `{"n":7}` {
			t.Fatalf("restore = (%q, %v), want the legacy blob", merged, useIt)
		}
		if _, ok := st[legacyModelKey]; ok {
			t.Errorf("legacy key not deleted")
		}
	})
}

func TestSpaPersistWrite_IdentityChangesInPage(t *testing.T) {
	t.Run("sign-out removes the stored copy and never writes the residue", func(t *testing.T) {
		st := memKV{}
		_, _, life := spaRestoreStored(st, practitionerB, sessionOnly, sessionOnly, idCap)
		spaPersistWrite(st, &life, practitioner, sessionOnly, idCap)
		// update signs out: session cleared, the rest of the model left as it was.
		residue := `{"session":null,"page":"diary","note":"u1 private","basket":["x"]}`
		spaPersistWrite(st, &life, residue, sessionOnly, idCap)
		if len(st) != 0 {
			t.Fatalf("storage after sign-out = %v, want empty", st)
		}
		// Later anonymous steps in the same page still carry the residue.
		spaPersistWrite(st, &life, residue, sessionOnly, idCap)
		if len(st) != 0 {
			t.Fatalf("storage after a post-sign-out step = %v, want empty", st)
		}
		if _, useIt, _ := spaRestoreStored(st, anonymousSeed, sessionOnly, sessionOnly, idCap); useIt {
			t.Fatalf("the next anonymous load restored the signed-out user's residue")
		}
	})

	t.Run("sign-in in the page writes under the new identity", func(t *testing.T) {
		st := memKV{}
		_, _, life := spaRestoreStored(st, anonymousSeed, sessionOnly, sessionOnly, idCap)
		spaPersistWrite(st, &life, anonymousModel, sessionOnly, idCap)
		spaPersistWrite(st, &life, practitioner, sessionOnly, idCap)
		merged, useIt, _ := spaRestoreStored(st, practitionerB, sessionOnly, sessionOnly, idCap)
		if !useIt || string(decodeObj(t, merged)["note"]) != `"u1 private"` {
			t.Fatalf("reload after an in-page sign-in = (%q, %v), want the signed-in model", merged, useIt)
		}
	})

	t.Run("an oversized model is not written", func(t *testing.T) {
		st := memKV{}
		_, _, life := spaRestoreStored(st, anonymousSeed, sessionOnly, sessionOnly, 10)
		spaPersistWrite(st, &life, anonymousModel, sessionOnly, 10)
		if len(st) != 0 {
			t.Fatalf("storage = %v, want empty for a blob over the cap", st)
		}
	})
}

func TestSpaIdentityOf(t *testing.T) {
	a, okA := spaIdentityOf(practitioner, sessionOnly)
	b, okB := spaIdentityOf(practitionerB, sessionOnly)
	if !okA || !okB || a != b || a == "" {
		t.Fatalf("identity(%s)=%q, identity(%s)=%q: want equal, non-empty", practitioner, a, practitionerB, b)
	}
	if p, _ := spaIdentityOf(patient, sessionOnly); p == a {
		t.Fatalf("patient and practitioner share identity %q", p)
	}
	for _, anon := range []string{anonymousSeed, `{"page":"x"}`} {
		if id, ok := spaIdentityOf(anon, sessionOnly); !ok || id != "" {
			t.Errorf("identity(%s) = (%q, %v), want anonymous", anon, id, ok)
		}
	}
	// A camelCase field is matched under its snake_case JSON key too.
	x, _ := spaIdentityOf(`{"auth_user":"u1"}`, []string{"authUser"})
	y, _ := spaIdentityOf(`{"authUser":"u1"}`, []string{"authUser"})
	if x == "" || x != y {
		t.Errorf("snake/camel identities %q vs %q, want equal", x, y)
	}
	// Large integers keep their precision.
	i1, _ := spaIdentityOf(`{"session":{"id":9007199254740993}}`, sessionOnly)
	i2, _ := spaIdentityOf(`{"session":{"id":9007199254740992}}`, sessionOnly)
	if i1 == i2 {
		t.Errorf("identities of distinct large ids collide: %q", i1)
	}
	if _, ok := spaIdentityOf("not json", sessionOnly); ok {
		t.Errorf("identity of a corrupt blob reported ok")
	}
}
