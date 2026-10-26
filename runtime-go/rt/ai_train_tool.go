//go:build !js

package rt

// ai_train_tool.go — the Go halves of the native trainer as plain functions,
// for cmd/sky-train-tool (scripts/ai/trainer/test-trainer.sh).

import "path/filepath"

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
