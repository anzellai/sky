package rt

import (
	"reflect"
	"strings"
	"testing"
)

type spaPublicationKV struct {
	values memKV
	calls  []string
}

func (s *spaPublicationKV) Get(key string) (string, bool) { return s.values.Get(key) }
func (s *spaPublicationKV) Set(key, value string) error {
	s.calls = append(s.calls, "set")
	return s.values.Set(key, value)
}
func (s *spaPublicationKV) Remove(key string) {
	s.calls = append(s.calls, "remove")
	s.values.Remove(key)
}

func TestSpaKnownIdentityPublicationMatchesReferenceIncludingOversize(t *testing.T) {
	for _, session := range []string{`null`, `{"id":"A"}`, `{"id":"B"}`} {
		for _, initial := range []spaPersistLife{{}, spaLifeFor(""), spaLifeFor(`{"session":{"id":"A"}}`)} {
			for _, oversized := range []bool{false, true} {
				data := "small"
				if oversized {
					data = strings.Repeat("x", spaPersistMaxBytes+1)
				}
				full := `{"session":` + session + `,"data":"` + data + `"}`
				id, ok := spaIdentityOf(full, []string{"session"})
				if !ok {
					t.Fatal("synthetic publication must have a valid identity")
				}
				a := &spaPublicationKV{values: memKV{spaPersistKey: "previous"}}
				b := &spaPublicationKV{values: memKV{spaPersistKey: "previous"}}
				lifeA, lifeB := initial, initial
				spaPersistWrite(a, &lifeA, full, []string{"session"}, spaPersistMaxBytes)
				payload := full
				if oversized {
					payload = "" // no full string is needed by the generated path
				}
				spaPersistWriteKnownIdentity(b, &lifeB, payload, id, uint64(len(full)), spaPersistMaxBytes)
				if lifeA != lifeB || !reflect.DeepEqual(a.calls, b.calls) || !reflect.DeepEqual(a.values, b.values) {
					t.Fatal("known-identity publication changed storage order or lifetime state")
				}
				if !lifeB.known || lifeB.id != id || lifeB.heldSignedIn != (initial.heldSignedIn || id != "") {
					t.Fatal("publication violated identity/lifetime invariants")
				}
				if oversized {
					for _, call := range b.calls {
						if call == "set" {
							t.Fatal("oversized value was published")
						}
					}
				}
			}
		}
	}
}
