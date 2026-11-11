package main

// Generated typed Codec.auto codecs against the reflective walker
// (rt.AutoCodecUseReference), on the research fixture. Copied into the
// emitted package by run.sh. One process per mode and size, as the research.

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	"sky-app/rt"
)

func item(i int) Main_Item_R {
	tags := []string{"alpha", "beta", "gamma", "delta", "urgent", "later"}
	it := Main_Item_R{
		Id: i, Name: "item-" + strconv.Itoa(i), Email: "user" + strconv.Itoa(i%9973) + "@example.test",
		Score: float64(i%1000) + 0.25, Active: i%3 == 0,
		Note: "Synthetic note text for the large model benchmark, plain ASCII.",
		Tags: []string{tags[i%6], tags[(i+1)%6]}, Parent: rt.Nothing[int](),
	}
	if i%4 == 0 {
		it.Parent = rt.Just(i / 4)
	}
	return it
}

func fixture(shape string, jsonBytes int) Main_Model_R {
	m := Main_Model_R{Title: "Large model", User: "user-42", Count: 7, Ratio: 1.5, Online: true,
		Filter: rt.Nothing[string](), Index: rt.AutoDictBuild[Main_Item_R](nil, nil)}
	n := jsonBytes / 205
	switch shape {
	case "list":
		m.Items = make([]Main_Item_R, n)
		for i := range m.Items {
			m.Items[i] = item(i)
		}
	case "dict":
		keys := make([]string, n)
		vals := make([]Main_Item_R, n)
		for i := 0; i < n; i++ {
			keys[i], vals[i] = fmt.Sprintf("k%08d", i), item(i)
		}
		m.Index = rt.AutoDictBuild(keys, vals)
		m.Items = []Main_Item_R{}
	default:
		b := make([]byte, jsonBytes)
		for i := range b {
			b[i] = 'a' + byte(i%26)
		}
		m.Blob = string(b)
		m.Items = []Main_Item_R{}
	}
	return m
}

// encode is Codec.toJson (Codec.auto blank) m, as Spa_modelToJson runs it.
func encode(m Main_Model_R) string { return rt.Spa_modelToJson(m).(string) }

func decode(s string) any { return rt.Spa_modelFromJson(Main_Model_R{}, s) }

func BenchmarkAutoCodec(b *testing.B) {
	rt.AutoCodecUseReference(os.Getenv("AUTO_CODEC_MODE") == "reference")
	if !rt.AutoCodecRegistered(Main_Model_R{}, true) {
		b.Fatal("no generated codec")
	}
	shapes := []string{"list", "dict", "blob"}
	if s := os.Getenv("AUTO_CODEC_SHAPE"); s != "" {
		shapes = []string{s}
	}
	sizes := []int{1, 3}
	if s := os.Getenv("AUTO_CODEC_MB"); s != "" {
		n, _ := strconv.Atoi(s)
		sizes = []int{n}
	}
	for _, shape := range shapes {
		for _, mb := range sizes {
			m := fixture(shape, mb<<20)
			text := encode(m)
			rt.AutoCodecUseReference(true)
			ref := encode(m)
			rt.AutoCodecUseReference(os.Getenv("AUTO_CODEC_MODE") == "reference")
			if text != ref {
				b.Fatalf("%s/%dMB: generated text differs from the walker's", shape, mb)
			}
			b.Run(fmt.Sprintf("encode/%s/%dMB", shape, mb), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					b.SetBytes(int64(len(encode(m))))
				}
			})
			b.Run(fmt.Sprintf("decode/%s/%dMB", shape, mb), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				for i := 0; i < b.N; i++ {
					if r := decode(text).(rt.SkyResult[any, any]); r.Tag != 0 {
						b.Fatal(r.ErrValue)
					}
				}
			})
		}
	}
}
