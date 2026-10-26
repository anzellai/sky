//go:build !js

package rt

// ai_train_settings.go — the training settings an operator may override from
// the environment, and how much memory a run may use.
//
// Each is a `Train.withX` builder in code with a `<PREFIX>_AI_TRAIN_*`
// variable over it (operator env > code > default), resolved by
// aiLayerChecked: a value that does not parse is InvalidSetting naming the
// variable (or the builder) and the form it takes, never skipped, and never
// shown. Train.sky applies the result to the job before it computes the run
// id, so a variable that changes the adapter (sequence length, batch, rank,
// quantisation) is a new run, and `Train.plan` names where each value came
// from. The memory cap and gradient checkpointing do not change the adapter,
// so they stay out of the job's JSON.
//
//	SEQUENCE_LENGTH   Train.withSequenceLength      tokens per row
//	BATCH             Train.withBatch               "4" or "1x4" (rows x accumulate)
//	RANK              Train.withRank                the LoRA rank
//	QUANT_BITS        Train.withQuantBits           4, 8 or 16 (16: not quantised)
//	GRAD_CHECKPOINT   Train.withGradientCheckpointing  on or off
//	MEMORY_CAP        Train.withMemoryCap           bytes, or with a unit (2.5GB)

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	aiExpectSeqLen    = aiExpect{accept: aiAtLeast(1), form: "a whole number of tokens above 0", secret: true}
	aiExpectBatch     = aiExpect{accept: aiBatchText, form: "rows per step, optionally times the steps accumulated (4, or 1x4)", secret: true}
	aiExpectRank      = aiExpect{accept: aiAtLeast(1), form: "a whole number above 0", secret: true}
	aiExpectQuantBits = aiExpect{accept: aiOneOf("4", "8", "16"), form: "4, 8 or 16 (16: not quantised)", secret: true}
	aiExpectOnOff     = aiExpect{accept: aiOneOf("on", "off", "1", "0", "true", "false", "yes", "no"), form: "on or off", secret: true}
	aiExpectSize      = aiExpect{accept: func(s string) bool { _, ok := aiParseSize(s); return ok }, form: "a size in bytes, or with a unit (2500MB, 2.5GB, 2GiB)", secret: true}
)

var aiBatchRe = regexp.MustCompile(`^\s*([0-9]+)\s*(?:[xX]\s*([0-9]+))?\s*$`)

func aiBatchParts(s string) (int, int, bool) {
	m := aiBatchRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	rows, err := strconv.Atoi(m[1])
	if err != nil || rows < 1 {
		return 0, 0, false
	}
	acc := 1
	if m[2] != "" {
		if acc, err = strconv.Atoi(m[2]); err != nil || acc < 1 {
			return 0, 0, false
		}
	}
	return rows, acc, true
}

func aiBatchText(s string) bool { _, _, ok := aiBatchParts(s); return ok }

var aiSizeRe = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)\s*([A-Za-z]*)\s*$`)

// aiParseSize reads a byte count: a whole number of bytes, or a number with
// a unit (B, KB, MB, GB, TB in powers of 1000; KiB, MiB, GiB, TiB in powers
// of 1024).
func aiParseSize(s string) (int64, bool) {
	m := aiSizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	units := map[string]float64{
		"": 1, "b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
	}
	mult, ok := units[strings.ToLower(m[2])]
	if !ok {
		return 0, false
	}
	if m[2] == "" && strings.Contains(m[1], ".") {
		return 0, false
	}
	x, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	n := math.Round(x * mult)
	if n < 1 || n > math.MaxInt64/2 {
		return 0, false
	}
	return int64(n), true
}

func aiOnOff(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "1", "true", "yes":
		return "on"
	}
	return "off"
}

// aiTrainSettingsResolve resolves the six settings in a fixed order, each
// value normalised: BATCH as "<rows>x<accumulate>", GRAD_CHECKPOINT as on or
// off, MEMORY_CAP in bytes ("" when nobody set one).
func aiTrainSettingsResolve(code any) ([]map[string]any, error) {
	type one struct {
		key, field string
		name       string
		resolve    func(string) (string, string, error)
		normal     func(string) string
	}
	trim := strings.TrimSpace
	settings := []one{
		{"SEQUENCE_LENGTH", "sequenceLength", skyEnvName("AI_TRAIN_SEQUENCE_LENGTH"), func(c string) (string, string, error) {
			return aiLayerChecked("AI_TRAIN_SEQUENCE_LENGTH", c, "Train.withSequenceLength", aiExpectSeqLen)
		}, trim},
		{"BATCH", "batch", skyEnvName("AI_TRAIN_BATCH"), func(c string) (string, string, error) {
			return aiLayerChecked("AI_TRAIN_BATCH", c, "Train.withBatch", aiExpectBatch)
		}, func(v string) string {
			rows, acc, _ := aiBatchParts(v)
			return strconv.Itoa(rows) + "x" + strconv.Itoa(acc)
		}},
		{"RANK", "rank", skyEnvName("AI_TRAIN_RANK"), func(c string) (string, string, error) {
			return aiLayerChecked("AI_TRAIN_RANK", c, "Train.withRank", aiExpectRank)
		}, trim},
		{"QUANT_BITS", "quantBits", skyEnvName("AI_TRAIN_QUANT_BITS"), func(c string) (string, string, error) {
			return aiLayerChecked("AI_TRAIN_QUANT_BITS", c, "Train.withQuantBits", aiExpectQuantBits)
		}, trim},
		{"GRAD_CHECKPOINT", "gradientCheckpointing", skyEnvName("AI_TRAIN_GRAD_CHECKPOINT"), func(c string) (string, string, error) {
			return aiLayerChecked("AI_TRAIN_GRAD_CHECKPOINT", c, "Train.withGradientCheckpointing", aiExpectOnOff)
		}, aiOnOff},
		{"MEMORY_CAP", "memoryCap", skyEnvName("AI_TRAIN_MEMORY_CAP"), func(c string) (string, string, error) {
			return aiLayerChecked("AI_TRAIN_MEMORY_CAP", c, "Train.withMemoryCap", aiExpectSize)
		}, func(v string) string {
			n, _ := aiParseSize(v)
			return strconv.FormatInt(n, 10)
		}},
	}
	out := make([]map[string]any, 0, len(settings))
	for _, s := range settings {
		goName := strings.ToUpper(s.field[:1]) + s.field[1:]
		v, src, err := s.resolve(strings.TrimSpace(aiStrField(code, goName, s.field)))
		if err != nil {
			return nil, err
		}
		if v != "" {
			v = s.normal(v)
		}
		out = append(out, map[string]any{"key": s.key, "name": s.name, "value": v, "source": src})
	}
	return out, nil
}

// Ai_trainSettingsResolve : Code -> Task Error (List Setting)
func Ai_trainSettingsResolve(codeArg any) any {
	return func() any {
		got, err := aiTrainSettingsResolve(codeArg)
		if err != nil {
			return aiTaskErr(err)
		}
		out := make([]any, len(got))
		for i, s := range got {
			out[i] = s
		}
		return Ok[any, any](out)
	}
}
