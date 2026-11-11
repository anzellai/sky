package rt

// Whole-Model encode benchmarks (phase 1 + phase 2), the Sky.Spa persistence
// and SSR encode: Spa_modelToJson = JsonEnc_encode(0, Codec_autoEnc(true, m)).
// The fixtures follow the large-model encoding research: a list of records, a
// Dict of records and one large string, at 1 MB and 10 MB of JSON. This file
// uses only functions that exist before and after the single-pass writer, so
// it can be copied into an older tree to measure the baseline.

import (
	"fmt"
	"strconv"
	"testing"
)

type benchLMItem struct {
	ID     int           `sky:"id,int"`
	Name   string        `sky:"name,string"`
	Email  string        `sky:"email,string"`
	Score  float64       `sky:"score,float64"`
	Active bool          `sky:"active,bool"`
	Note   string        `sky:"note,string"`
	Tags   []string      `sky:"tags,[]string"`
	Parent SkyMaybe[int] `sky:"parent,rt.SkyMaybe[int]"`
}

type benchLMModel struct {
	Title  string               `sky:"title,string"`
	User   string               `sky:"user,string"`
	Count  int                  `sky:"count,int"`
	Ratio  float64              `sky:"ratio,float64"`
	Online bool                 `sky:"online,bool"`
	Filter SkyMaybe[string]     `sky:"filter,rt.SkyMaybe[string]"`
	Items  []benchLMItem        `sky:"items,[]rt.benchLMItem"`
	Index  SkyDict[benchLMItem] `sky:"index,rt.SkyDict[rt.benchLMItem]"`
	Blob   string               `sky:"blob,string"`
}

func benchLMMakeItem(i int) benchLMItem {
	tags := []string{"alpha", "beta", "gamma", "delta", "urgent", "later"}
	it := benchLMItem{
		ID: i, Name: "item-" + strconv.Itoa(i), Email: "user" + strconv.Itoa(i%9973) + "@example.test",
		Score: float64(i%1000) + 0.25, Active: i%3 == 0,
		Note: "Synthetic note text for the large model benchmark, plain ASCII.",
		Tags: []string{tags[i%6], tags[(i+1)%6]}, Parent: Nothing[int](),
	}
	if i%4 == 0 {
		it.Parent = Just(i / 4)
	}
	return it
}

func benchLMFixture(shape string, jsonBytes int) benchLMModel {
	m := benchLMModel{Title: "Large model", User: "user-42", Count: 7, Ratio: 1.5, Online: true,
		Filter: Nothing[string](), Index: SkyDict[benchLMItem]{}}
	n := jsonBytes / 205 // about 205 JSON bytes per item
	switch shape {
	case "list":
		m.Items = make([]benchLMItem, n)
		for i := range m.Items {
			m.Items[i] = benchLMMakeItem(i)
		}
	case "dict":
		keys := make([]string, n)
		vals := make([]benchLMItem, n)
		for i := 0; i < n; i++ {
			keys[i], vals[i] = fmt.Sprintf("k%08d", i), benchLMMakeItem(i)
		}
		m.Index = dictBuild(keys, vals, false)
	default:
		b := make([]byte, jsonBytes)
		for i := range b {
			b[i] = 'a' + byte(i%26)
		}
		m.Blob = string(b)
	}
	return m
}

func BenchmarkSpaModelEncode(b *testing.B) {
	for _, shape := range []string{"list", "dict", "blob"} {
		for _, mb := range []int{1, 3, 10} {
			m := benchLMFixture(shape, mb<<20)
			b.Run(fmt.Sprintf("%s/%dMB", shape, mb), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					out := Spa_modelToJson(m).(string)
					b.SetBytes(int64(len(out)))
				}
			})
		}
	}
}

// BenchmarkSpaModelDecode is the boot decode, Spa_modelFromJson, on the
// reflective walker (these test types have no generated codec; the compiled
// comparison is docs/perf/runs/auto-codec-20261110).
func BenchmarkSpaModelDecode(b *testing.B) {
	for _, shape := range []string{"list", "dict", "blob"} {
		for _, mb := range []int{1, 3} {
			text := Spa_modelToJson(benchLMFixture(shape, mb<<20)).(string)
			b.Run(fmt.Sprintf("%s/%dMB", shape, mb), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				for i := 0; i < b.N; i++ {
					if r := Spa_modelFromJson(benchLMModel{}, text).(SkyResult[any, any]); r.Tag != 0 {
						b.Fatal(r.ErrValue)
					}
				}
			})
		}
	}
}
