//go:build !js

package rt

// ai_train_tool.go — the Go halves of the native trainer as plain functions,
// for cmd/sky-train-tool (scripts/ai/trainer/test-trainer.sh).

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// AiTrainTokenizeDir runs the `tokenize` step on a staged run directory.
func AiTrainTokenizeDir(runDir, base string) (map[string]any, error) {
	return aiTrainTokenize(runDir, base)
}

// AiTrainConvertFile converts <run>/adapter to <run>/adapter.gguf for the
// serving GGUF at path, pinned by its digest. The adapter records the
// GGUF's file name as its base id.
func AiTrainConvertFile(runDir, gguf string) (map[string]any, error) {
	sum, err := aiFileSha256(gguf)
	if err != nil {
		return nil, aiTrainFail("cannot read %s: %v", gguf, err)
	}
	return aiTrainConvert(runDir, "file:"+gguf+"#sha256="+sum, filepath.Base(gguf))
}

// AiTrainNativeArgv is the sky-trainer command line the runtime launches an
// attempt with, from the attempt's driver.json (the launch token is
// "sky-train-tool").
func AiTrainNativeArgv(runDir, driverFile string) ([]string, error) {
	b, err := os.ReadFile(driverFile)
	if err != nil {
		return nil, aiTrainFail("cannot read %s: %v", driverFile, err)
	}
	cfg, err := aiTrainJSON(string(b))
	if err != nil {
		return nil, aiTrainFail("%s is not JSON: %v", driverFile, err)
	}
	return aiTrainNativeArgs(cfg, runDir, "sky-train-tool")
}

// AiAdapterMaxDiff compares two safetensors adapters tensor by tensor: the
// same names and shapes, and the largest absolute difference of any value.
// test-trainer.sh uses it to hold the chunked loss to the unchunked one.
func AiAdapterMaxDiff(a, b string) (map[string]any, error) {
	ta, err := aiReadSafetensors(a)
	if err != nil {
		return nil, aiTrainFail("cannot read %s: %v", a, err)
	}
	tb, err := aiReadSafetensors(b)
	if err != nil {
		return nil, aiTrainFail("cannot read %s: %v", b, err)
	}
	if len(ta) != len(tb) {
		return nil, aiTrainFail("%s has %d tensors, %s has %d", a, len(ta), b, len(tb))
	}
	worst := 0.0
	for name, x := range ta {
		y, ok := tb[name]
		if !ok {
			return nil, aiTrainFail("%s has no tensor %s", b, name)
		}
		if fmt.Sprint(x.shape) != fmt.Sprint(y.shape) {
			return nil, aiTrainFail("tensor %s is %v in %s and %v in %s", name, x.shape, a, y.shape, b)
		}
		for i := range x.data {
			d := math.Abs(float64(x.data[i]) - float64(y.data[i]))
			if math.IsNaN(d) {
				return nil, aiTrainFail("tensor %s holds a NaN", name)
			}
			if d > worst {
				worst = d
			}
		}
	}
	return map[string]any{"maxAbsDiff": worst, "tensors": len(ta)}, nil
}
