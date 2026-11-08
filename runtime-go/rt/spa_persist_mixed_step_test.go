package rt

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

type spaMixedTraceKV struct {
	spaPublicationKV
	trace *[]string
}

func (s *spaMixedTraceKV) Set(key, value string) {
	*s.trace = append(*s.trace, "write")
	s.spaPublicationKV.Set(key, value)
}
func (s *spaMixedTraceKV) Remove(key string) {
	*s.trace = append(*s.trace, "remove")
	s.spaPublicationKV.Remove(key)
}

func TestSpaMixedPersistenceMatchesReferenceEffectsAndPublication(t *testing.T) {
	type model struct {
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		Data    []string         `sky:"data,[]string"`
		Text    string           `sky:"text,string"`
		Number  float64          `sky:"number,float64"`
		FX      any              `sky:"fx,any"`
	}
	for _, size := range []int{1000, 10000, 100000} {
		data := make([]string, size)
		for i := range data {
			data[i] = strings.Repeat("x", 60)
		}
		for _, excluded := range [][]string{nil, {"data"}} {
			for _, protected := range [][]string{nil, {"session"}} {
				refLog, optLog := []string{}, []string{}
				refEncoder, optEncoder := spaMixedTestEncoder(&refLog), spaMixedTestEncoder(&optLog)
				optEncoder.InputType = SpaModelInputType[model]()
				create := func(log *[]string) model {
					return model{Session: Nothing[string](), Data: data, FX: spaMixedTestFX{log: log, V0: JsonValue{raw: spaMixedTestMarshal{log: log, name: "marshal"}}}}
				}
				refModel, optModel := create(&refLog), create(&optLog)
				refKV := &spaMixedTraceKV{spaPublicationKV: spaPublicationKV{values: memKV{}}, trace: &refLog}
				optKV := &spaMixedTraceKV{spaPublicationKV: spaPublicationKV{values: memKV{}}, trace: &optLog}
				lifeA, lifeB := spaPersistLife{}, spaPersistLife{}
				controller := spaMixedPersistence{encoder: optEncoder, profiling: true}
				for step := 0; step < 12; step++ {
					nextA, nextB := refModel, optModel
					mutate := func(m model) model {
						switch step {
						case 1:
							m.Text = "typed"
						case 3:
							m.Session = Just("synthetic-A")
						case 4:
							m.Session = Just("synthetic-B")
						case 5:
							m.Number = math.NaN()
						case 6:
							m.Session = Nothing[string]()
						case 7:
							m.Number = 0
						case 8:
							m.Session = Just("synthetic-A")
						case 9:
							m.Session = Nothing[string]()
						case 10:
							m.Data = []string{"small"}
						case 11:
							m.Data = data
						}
						return m
					}
					nextA, nextB = mutate(nextA), mutate(nextB)
					if step == 2 {
						delete(refKV.values, spaPersistKey)
						delete(optKV.values, spaPersistKey)
					}
					refLog, optLog = nil, nil
					refClear := spaPersistStep(refModel, nextA, refEncoder.encode, func(blob string) {
						spaPersistWrite(refKV, &lifeA, spaPersistStripFields(blob, excluded, protected), protected, spaPersistMaxBytes)
					}, protected, nil)
					optClear, _ := controller.step(optModel, nextB, optEncoder.encode, optKV, &lifeB, excluded, protected, spaPersistMaxBytes)
					if refClear != optClear || lifeA != lifeB || !reflect.DeepEqual(refKV.values, optKV.values) || !reflect.DeepEqual(refKV.calls, optKV.calls) || !reflect.DeepEqual(refLog, optLog) {
						t.Fatalf("mixed persistence diverged: size=%d step=%d reference_clear=%t mixed_clear=%t reference_trace=%v mixed_trace=%v", size, step, refClear, optClear, refLog, optLog)
					}
					if math.IsNaN(nextB.Number) && controller.cache != nil {
						t.Fatal("invalid next retained an old owner's certificate")
					}
					refModel, optModel = nextA, nextB
				}
			}
		}
	}
}

func TestSpaMixedPersistenceInputMismatchUsesFullBeforePreparation(t *testing.T) {
	type model struct {
		Value int `sky:"value,int"`
	}
	log := []string{}
	encoder := spaMixedTestEncoder(&log)
	encoder.InputType = SpaModelInputType[model]()
	calls := 0
	full := func(value any) (out string) {
		if value == nil {
			return ""
		}
		calls++
		log = append(log, "full-coercion")
		defer func() {
			if recover() != nil {
				out = ""
			}
		}()
		return encoder.encode(Coerce[model](value))
	}
	controller := spaMixedPersistence{encoder: encoder}
	kv := &spaPublicationKV{values: memKV{}}
	life := spaPersistLife{}
	controller.step(model{}, "not a model", full, kv, &life, nil, []string{"session"}, spaPersistMaxBytes)
	if calls != 2 || !reflect.DeepEqual(log, []string{"full-coercion", "full-coercion", "prepare"}) || controller.cache != nil {
		t.Fatal("input coercion failure added preparation effects")
	}
	log = nil
	calls = 0
	controller.step(model{}, nil, full, kv, &life, nil, []string{"session"}, spaPersistMaxBytes)
	if calls != 1 || !reflect.DeepEqual(log, []string{"full-coercion", "prepare"}) {
		t.Fatal("nil model added preparation effects")
	}
}

func TestSpaMixedPersistenceStillEncodesPreviousAfterInvalidNext(t *testing.T) {
	type model struct {
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		FX      any              `sky:"fx,any"`
	}
	log := []string{}
	encoder := spaMixedTestEncoder(&log)
	encoder.InputType = SpaModelInputType[model]()
	controller := spaMixedPersistence{encoder: encoder}
	previous := model{Session: Just("synthetic"), FX: spaMixedTestFX{log: &log, V0: "ok"}}
	next := model{Session: Nothing[string](), FX: func() {}}
	kv := &spaMixedTraceKV{spaPublicationKV: spaPublicationKV{values: memKV{}}, trace: &log}
	life := spaPersistLife{}
	clear, _ := controller.step(previous, next, encoder.encode, kv, &life, nil, []string{"session"}, spaPersistMaxBytes)
	if clear || !reflect.DeepEqual(log, []string{"prepare", "prepare", "tag", "convert"}) || len(kv.calls) != 0 || life.known {
		t.Fatal("next failure suppressed or reordered previous effects/publication")
	}
}
