// Command sky-train-tool runs the Go halves of Std.Ai.Train's native trainer
// outside a Sky program, for scripts/ai/trainer/test-trainer.sh:
//
//	go run ./cmd/sky-train-tool tokenize <run dir> <base dir>
//	go run ./cmd/sky-train-tool convert <run dir> <serving gguf>
//	go run ./cmd/sky-train-tool argv <run dir> <driver.json>
//	go run ./cmd/sky-train-tool diff <adapter a> <adapter b>
//	go run ./cmd/sky-train-tool tokens <base dir> <text file>
//
// tokenize writes <run>/data/{train,valid}.tok and order.bin from the staged
// data/*.jsonl and job.json (the `tokenize` durable step). convert turns the
// trained <run>/adapter into <run>/adapter.gguf for the serving model (the
// `convert` step). Each prints its result as JSON. argv prints, one per line,
// the sky-trainer arguments the runtime launches an attempt with. diff prints
// the largest absolute difference between two adapters (safetensors). tokens
// prints the token ids of a file's text, as a JSON array.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"sky-app/rt"
)

func main() {
	if len(os.Args) != 4 || (os.Args[1] != "tokenize" && os.Args[1] != "convert" && os.Args[1] != "argv" && os.Args[1] != "diff" && os.Args[1] != "tokens") {
		fmt.Fprintln(os.Stderr, "usage: sky-train-tool tokenize <run> <base> | convert <run> <serving.gguf> | argv <run> <driver.json> | diff <a> <b> | tokens <base> <text>")
		os.Exit(2)
	}
	var out map[string]any
	var err error
	switch os.Args[1] {
	case "tokenize":
		out, err = rt.AiTrainTokenizeDir(os.Args[2], os.Args[3])
	case "convert":
		out, err = rt.AiTrainConvertFile(os.Args[2], os.Args[3])
	case "diff":
		out, err = rt.AiAdapterMaxDiff(os.Args[2], os.Args[3])
	case "tokens":
		var ids []int
		ids, err = rt.AiTokenizeFile(os.Args[2], os.Args[3])
		if err == nil {
			b, _ := json.Marshal(ids)
			fmt.Println(string(b))
			return
		}
	default:
		var args []string
		args, err = rt.AiTrainNativeArgv(os.Args[2], os.Args[3])
		if err == nil {
			for _, a := range args {
				fmt.Println(a)
			}
			return
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sky-train-tool:", err)
		os.Exit(1)
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}
