//go:build !js

package rt

// ai_train_tool.go — the Go halves of the native trainer as plain functions,
// for cmd/sky-train-tool (scripts/ai/trainer/test-trainer.sh).

import (
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
