// Command sky-tiny-gguf writes the synthetic Qwen2-architecture model and LoRA
// adapter the Std.Ai.Device tests run (rt.AiTinyQwen), and the draft, second
// adapter and other-vocabulary model the Std.Ai.Local draft and adapter tests
// run (rt.AiTinyQwenExtras), and the model's Hugging Face twin the native
// trainer trains on (rt.AiTinyQwenHF, key "hf"), and prints their paths and
// digests as JSON.
//
//	go run ./cmd/sky-tiny-gguf <dir>
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"sky-app/rt"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: sky-tiny-gguf <dir>")
		os.Exit(2)
	}
	model, modelSum, adapter, adapterSum, err := rt.AiTinyQwen(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "sky-tiny-gguf:", err)
		os.Exit(1)
	}
	files := map[string]string{
		"model": model, "modelSha256": modelSum, "adapter": adapter, "adapterSha256": adapterSum,
	}
	extras, err := rt.AiTinyQwenExtras(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "sky-tiny-gguf:", err)
		os.Exit(1)
	}
	for name, f := range extras {
		files[name], files[name+"Sha256"] = f[0], f[1]
	}
	hf, err := rt.AiTinyQwenHF(filepath.Join(os.Args[1], "tiny-qwen2-hf"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sky-tiny-gguf:", err)
		os.Exit(1)
	}
	files["hf"] = hf
	out, _ := json.Marshal(files)
	fmt.Println(string(out))
}
