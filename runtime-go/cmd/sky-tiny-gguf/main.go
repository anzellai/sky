// Command sky-tiny-gguf writes the synthetic Qwen2-architecture model and LoRA
// adapter the Std.Ai.Device tests run (rt.AiTinyQwen), and prints their paths
// and digests as JSON.
//
//	go run ./cmd/sky-tiny-gguf <dir>
package main

import (
	"encoding/json"
	"fmt"
	"os"

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
	out, _ := json.Marshal(map[string]string{
		"model": model, "modelSha256": modelSum, "adapter": adapter, "adapterSha256": adapterSum,
	})
	fmt.Println(string(out))
}
