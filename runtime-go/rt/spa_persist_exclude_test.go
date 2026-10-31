package rt

import (
	"encoding/json"
	"testing"
)

// App.withPersistExclude / Spa.withPersistExcludeFields (v0.29.0): a field the
// server owns (site settings, a feature flag, the server clock) is never
// written to localStorage and always comes from the SSR seed on a reload, so a
// stale stored copy cannot paint the page. Found in a real app: a stored copy
// of a server-owned setting hid a sign-in button until a refresh.

const (
	excludeSeed   = `{"session":null,"siteConfig":{"google":true},"page":"home","note":""}`
	excludeStored = `{"session":null,"siteConfig":{"google":false},"page":"diary","note":"draft"}`
)

func TestSpaPersistExcludedFieldIsNotStored(t *testing.T) {
	st := memKV{}
	_, _, life := spaRestoreStored(st, excludeSeed, sessionOnly, sessionOnly, idCap)
	spaPersistWrite(st, &life, spaPersistStripFields(excludeStored, []string{"siteConfig"}, sessionOnly), sessionOnly, idCap)
	var stored map[string]json.RawMessage
	if err := json.Unmarshal([]byte(st[spaPersistKey]), &stored); err != nil {
		t.Fatalf("stored JSON: %v (%q)", err, st[spaPersistKey])
	}
	if _, ok := stored["siteConfig"]; ok {
		t.Fatalf("an excluded field was stored: %s", st[spaPersistKey])
	}
	if string(stored["note"]) != `"draft"` {
		t.Fatalf("the other fields must still be stored: %s", st[spaPersistKey])
	}
}

func TestSpaPersistExcludedFieldComesFromTheSeed(t *testing.T) {
	// A stale copy written by an older build that stored every field.
	st := memKV{spaPersistKey: excludeStored}
	wins := spaPersistWinsWithExclude(spaSeedWinsFields(sessionOnly, nil, nil), []string{"siteConfig"})
	merged, ok, _ := spaRestoreStored(st, excludeSeed, sessionOnly, wins, idCap)
	if !ok {
		t.Fatalf("the stored model must still restore")
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal([]byte(merged), &m)
	if string(m["siteConfig"]) != `{"google":true}` {
		t.Fatalf("the excluded field must come from the seed: %s", merged)
	}
	if string(m["page"]) != `"diary"` || string(m["note"]) != `"draft"` {
		t.Fatalf("the other fields must restore from storage: %s", merged)
	}
}

func TestSpaPersistExcludeNeverDropsASessionField(t *testing.T) {
	// A protected (session) field stays in the stored model even when it is
	// listed: the identity rule needs it.
	got := spaPersistStripFields(`{"session":{"u":1},"siteConfig":1}`, []string{"session", "siteConfig"}, sessionOnly)
	if got != `{"session":{"u":1}}` {
		t.Fatalf("strip: %s", got)
	}
	// snake_case keys (Codec.auto) are matched too, and no exclusion is a no-op.
	if got := spaPersistStripFields(`{"site_config":1,"a":2}`, []string{"siteConfig"}, nil); got != `{"a":2}` {
		t.Fatalf("snake strip: %s", got)
	}
	if got := spaPersistStripFields(excludeStored, nil, nil); got != excludeStored {
		t.Fatalf("no exclusion changed the JSON: %s", got)
	}
}
