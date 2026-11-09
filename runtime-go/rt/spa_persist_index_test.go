package rt

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The reference persistence path now parses a Model's JSON once per step
// (spa_persist_index.go). These tests prove every derived result equals the
// previous functions, which stay in the tree as the oracle: spaIdentityOf,
// spaPersistStripFields, spaPersistWrite and spaSessionClearedByStep.

var spaIndexFieldNames = []string{"session", "user_id", "userId", "draft", "items", "title", "flag", "a\\b", "é", "<k>"}

// spaIndexRandomDoc builds a JSON text of the shapes an encoder can return:
// objects with whitespace, duplicate keys, escaped keys, null and nested
// values, plus null, arrays, scalars and invalid text.
func spaIndexRandomDoc(r *jsonTreeRand) string {
	switch r.next() % 20 {
	case 0:
		return "null"
	case 1:
		return `[1,{"session":"x"}]`
	case 2:
		return `"text"`
	case 3:
		return `{"session":"x"` // invalid
	case 4:
		return ""
	case 5:
		return " \n{ } "
	}
	ws := func() string { return []string{"", " ", "\n\t", "  \r\n"}[r.next()%4] }
	var b strings.Builder
	b.WriteString(ws() + "{")
	n := int(r.next() % 7)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(ws() + "," + ws())
		}
		key := spaIndexFieldNames[r.next()%uint64(len(spaIndexFieldNames))]
		keyJSON, _ := json.Marshal(key)
		if r.next()%5 == 0 && key == "session" {
			keyJSON = []byte(`"sess\u0069on"`) // an escaped spelling of the same key
		}
		b.Write(keyJSON)
		b.WriteString(ws() + ":" + ws())
		switch r.next() % 7 {
		case 0:
			b.WriteString("null")
		case 5:
			// Compact, but with raw HTML characters and a raw U+2028.
			b.WriteString([]string{`{"h":"a<b"}`, `["x>y"]`, `"&"`, `"` + "\u2028" + `"`, `"` + "\u2029" + `"`}[r.next()%5])
		case 1:
			b.WriteString(`{ "id" : 12.50 , "name":"a<b" , "n" : [1, 2, {"x":null}] }`)
		case 2:
			b.WriteString(strconv.FormatInt(int64(r.next()%2000)-1000, 10))
		case 3:
			b.WriteString(`"str \"q\" \\ \u2028 é"`)
		default:
			v, _ := jsonMarshalTree(r.tree(0, 3))
			b.Write(v)
		}
	}
	b.WriteString("}" + ws())
	return b.String()
}

func spaIndexOracleOK(doc string) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal([]byte(doc), &m) == nil
}

func TestSpaPersistIndexMatchesTheOldParsers(t *testing.T) {
	r := &jsonTreeRand{s: 0x51a}
	protectedSets := [][]string{nil, {"session"}, {"userId"}, {"session", "userId"}}
	excludeSets := [][]string{nil, {"draft"}, {"items", "title"}, {"session"}, {"user_id"}, {"<k>", "é"}}
	for i := 0; i < 20000; i++ {
		doc := spaIndexRandomDoc(r)
		x, ok := spaIndexTop(doc)
		if ok != spaIndexOracleOK(doc) {
			t.Fatalf("doc %q: index ok=%v, Unmarshal ok=%v", doc, ok, !ok)
		}
		protected := protectedSets[r.next()%uint64(len(protectedSets))]
		exclude := excludeSets[r.next()%uint64(len(excludeSets))]
		capBytes := []int{0, 40, 1 << 20}[r.next()%3]
		if ok {
			wantStripped := spaPersistStripFields(doc, exclude, protected)
			payload, size := x.stored(exclude, protected, capBytes)
			if size != uint64(len(wantStripped)) {
				t.Fatalf("doc %q exclude %v: size %d want %d", doc, exclude, size, len(wantStripped))
			}
			if (capBytes <= 0 || len(wantStripped) <= capBytes) && payload != wantStripped {
				t.Fatalf("doc %q exclude %v:\n got %q\nwant %q", doc, exclude, payload, wantStripped)
			}
			gotID, gotOK := x.identity(protected)
			for _, src := range []string{doc, wantStripped} {
				wantID, wantOK := spaIdentityOf(src, protected)
				if gotID != wantID || gotOK != wantOK {
					t.Fatalf("doc %q: identity %q/%v want %q/%v", src, gotID, gotOK, wantID, wantOK)
				}
			}
		}
		// The write: the same storage effects and the same page lifetime.
		for _, start := range []spaPersistLife{{}, spaLifeFor(""), spaLifeFor(`{"session":"x"}`)} {
			old, cur := memKV{spaPersistKey: "previous"}, memKV{spaPersistKey: "previous"}
			lifeOld, lifeCur := start, start
			spaPersistWrite(old, &lifeOld, spaPersistStripFields(doc, exclude, protected), protected, capBytes)
			spaPersistWriteJSON(cur, &lifeCur, doc, exclude, protected, capBytes)
			if !reflect.DeepEqual(old, cur) || lifeOld != lifeCur {
				t.Fatalf("doc %q: write differs: %v %+v / %v %+v", doc, old, lifeOld, cur, lifeCur)
			}
		}
		// The sign-out decision against another document.
		other := spaIndexRandomDoc(r)
		fields := []string{"session", "userId", "<k>"}
		ref := &spaReferencePersist{}
		if got, want := ref.cleared(other, doc, fields), spaSessionClearedByStep(other, doc, fields); got != want {
			t.Fatalf("prev %q next %q: cleared %v want %v", other, doc, got, want)
		}
	}
}

// spaIndexStepModel is a Model the test encoder turns into JSON. The encoder
// is a plain function, as the app's encoder is to the runtime.
type spaIndexStepModel struct {
	session string // "" is signed out
	draft   string
	items   int
}

func spaIndexStepEncode(calls *int) func(any) string {
	return func(v any) string {
		*calls++
		m := v.(spaIndexStepModel)
		session := "null"
		if m.session != "" {
			session = `{"id":"` + m.session + `"}`
		}
		return `{"draft":"` + m.draft + `","items":"` + strings.Repeat("i", m.items) + `","session":` + session + `}`
	}
}

func TestSpaPersistStepReferenceMatchesTheOldStep(t *testing.T) {
	steps := []spaIndexStepModel{
		{draft: "a", items: 10},
		{draft: "b", items: 10},
		{session: "u1", draft: "b", items: 20},
		{session: "u1", draft: "c", items: 2_000_000}, // over the cap
		{session: "u1", draft: "c", items: 30},
		{session: "u2", draft: "c", items: 30}, // another identity
		{draft: "d", items: 30},                // sign-out
		{draft: "e", items: 30},
		{session: "u1", items: 1_499_950}, // near the cap: the exclusion decides
	}
	for _, exclude := range [][]string{nil, {"draft"}, {"items"}} {
		for _, protected := range [][]string{nil, {"session"}} {
			oldKV := &spaMixedTraceKV{spaPublicationKV: spaPublicationKV{values: memKV{}}, trace: &[]string{}}
			newKV := &spaMixedTraceKV{spaPublicationKV: spaPublicationKV{values: memKV{}}, trace: &[]string{}}
			oldLife, newLife := spaPersistLife{}, spaPersistLife{}
			oldCalls, newCalls := 0, 0
			oldEnc, newEnc := spaIndexStepEncode(&oldCalls), spaIndexStepEncode(&newCalls)
			ref := &spaReferencePersist{st: newKV, life: &newLife, exclude: exclude, protected: protected, capBytes: spaPersistMaxBytes}
			prev := spaIndexStepModel{}
			for i, next := range steps {
				oldCleared := spaPersistStep(prev, next, oldEnc, func(s string) {
					spaPersistWrite(oldKV, &oldLife, spaPersistStripFields(s, exclude, protected), protected, spaPersistMaxBytes)
				}, protected, nil)
				newCleared := spaPersistStepReference(prev, next, newEnc, ref, nil)
				if oldCleared != newCleared || oldCalls != newCalls || oldLife != newLife ||
					!reflect.DeepEqual(*oldKV.trace, *newKV.trace) || !reflect.DeepEqual(oldKV.values, newKV.values) {
					t.Fatalf("exclude %v protected %v step %d: cleared %v/%v calls %d/%d life %+v/%+v trace %v/%v",
						exclude, protected, i, oldCleared, newCleared, oldCalls, newCalls, oldLife, newLife, *oldKV.trace, *newKV.trace)
				}
				prev = next
			}
		}
	}
}

// TestSpaPersistStepReferenceParsesOnce measures the reference step with an
// encoder that returns a ready 1 MB string: before, every step unmarshalled
// that JSON into a map four times (strip, identity, previous, next) and
// marshalled it once; now it is scanned, not copied, and the previous JSON
// reuses the last step's scan.
func TestSpaPersistStepReferenceParsesOnce(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	docA := `{"draft":"a","items":"` + big + `","session":{"id":"u1"}}`
	docB := `{"draft":"b","items":"` + big + `","session":{"id":"u1"}}`
	encode := func(v any) string { return v.(string) }
	protected, exclude := []string{"session"}, []string{"draft"}
	measure := func(step func(prev, next string)) uint64 {
		step(docA, docB) // warm: the previous step stored docB
		return spaMixedCapAllocated(func() { step(docB, docA) })
	}
	oldBytes := measure(func(prev, next string) {
		st, life := memKV{}, spaPersistLife{}
		spaPersistStep(prev, next, encode, func(s string) {
			spaPersistWrite(st, &life, spaPersistStripFields(s, exclude, protected), protected, spaPersistMaxBytes)
		}, protected, nil)
	})
	ref := &spaReferencePersist{st: memKV{}, life: &spaPersistLife{}, exclude: exclude, protected: protected, capBytes: spaPersistMaxBytes}
	newBytes := measure(func(prev, next string) { spaPersistStepReference(prev, next, encode, ref, nil) })
	t.Logf("allocated per step: old %d bytes, new %d bytes", oldBytes, newBytes)
	// The stripped snapshot (about 1 MB) is still built once, as it is stored.
	if newBytes > 3<<19 || newBytes*3 > oldBytes {
		t.Fatalf("reference step still copies the document: old %d, new %d bytes", oldBytes, newBytes)
	}
}

// BenchmarkSpaPersistReferenceStep is one reference-path step (the encoder
// returns a ready JSON string, so only the persistence work is measured):
// the old composition against the indexed one, for 1 MB and 10 MB models
// with a session field and an exclusion.
func BenchmarkSpaPersistReferenceStep(b *testing.B) {
	for _, mb := range []int{1, 10} {
		big := strings.Repeat("x", mb<<20)
		docs := [2]string{
			`{"draft":"a","items":"` + big + `","session":{"id":"u1"}}`,
			`{"draft":"b","items":"` + big + `","session":{"id":"u1"}}`,
		}
		encode := func(v any) string { return v.(string) }
		protected, exclude := []string{"session"}, []string{"draft"}
		b.Run(strconv.Itoa(mb)+"MB/old", func(b *testing.B) {
			st, life := memKV{}, spaPersistLife{}
			for i := 0; i < b.N; i++ {
				spaPersistStep(docs[i&1], docs[(i+1)&1], encode, func(s string) {
					spaPersistWrite(st, &life, spaPersistStripFields(s, exclude, protected), protected, spaPersistMaxBytes)
				}, protected, nil)
			}
		})
		b.Run(strconv.Itoa(mb)+"MB/new", func(b *testing.B) {
			ref := &spaReferencePersist{st: memKV{}, life: &spaPersistLife{}, exclude: exclude, protected: protected, capBytes: spaPersistMaxBytes}
			for i := 0; i < b.N; i++ {
				spaPersistStepReference(docs[i&1], docs[(i+1)&1], encode, ref, nil)
			}
		})
	}
}
