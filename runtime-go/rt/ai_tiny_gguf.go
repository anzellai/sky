//go:build !js

package rt

// ai_tiny_gguf.go — a synthetic Qwen2-architecture model and LoRA adapter for
// tests: two layers of 64 dimensions, a byte-level vocabulary (one token per
// byte, the GPT-2 byte mapping Qwen uses, and the ChatML control tokens), the
// ChatML template, and random F32 weights from a fixed seed. llama.cpp loads
// and runs it like any Qwen model, so the Std.Ai.Device flows (the browser
// worker, the iOS and Android engines, the desktop app) run end to end in CI
// without the 430 MB development model. Its answers are noise; the flows
// assert where a call ran and what it carried, not what it said.
//
// `go run ./cmd/sky-tiny-gguf <dir>` writes both files (scripts/ai-device-e2e.sh).

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/rand"
	"os"
	"path/filepath"
)

const (
	tinyEmbd    = 64
	tinyHeads   = 4
	tinyKVHeads = 2
	tinyFF      = 128
	tinyLayers  = 2
	tinyRank    = 4
)

// tinyByteRunes is GPT-2's byte-to-unicode table: printable bytes map to
// themselves, the others to code points from 256 up.
func tinyByteRunes() [256]rune {
	var out [256]rune
	keep := map[int]bool{}
	for b := 33; b <= 126; b++ {
		keep[b] = true
	}
	for b := 161; b <= 172; b++ {
		keep[b] = true
	}
	for b := 174; b <= 255; b++ {
		keep[b] = true
	}
	n := 0
	for b := 0; b < 256; b++ {
		if keep[b] {
			out[b] = rune(b)
		} else {
			out[b] = rune(256 + n)
			n++
		}
	}
	return out
}

type tinyKV struct {
	key string
	val any // string, uint32, float32, []string, []int32
}

type tinyTensor struct {
	name string
	dims []uint64 // innermost first
	data []float32
}

func tinyGGUF(kvs []tinyKV, tensors []tinyTensor) []byte {
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { le(uint64(len(s))); b.WriteString(s) }
	b.WriteString("GGUF")
	le(uint32(3))
	le(uint64(len(tensors)))
	le(uint64(len(kvs)))
	for _, kv := range kvs {
		str(kv.key)
		switch v := kv.val.(type) {
		case string:
			le(uint32(8))
			str(v)
		case uint32:
			le(uint32(4))
			le(v)
		case float32:
			le(uint32(6))
			le(v)
		case []string:
			le(uint32(9))
			le(uint32(8))
			le(uint64(len(v)))
			for _, s := range v {
				str(s)
			}
		case []int32:
			le(uint32(9))
			le(uint32(5))
			le(uint64(len(v)))
			for _, x := range v {
				le(x)
			}
		}
	}
	pad := func(n int) int { return (32 - n%32) % 32 }
	off := uint64(0)
	for _, t := range tensors {
		str(t.name)
		le(uint32(len(t.dims)))
		for _, d := range t.dims {
			le(d)
		}
		le(uint32(0)) // F32
		le(off)
		size := 4 * len(t.data)
		off += uint64(size + pad(size))
	}
	b.Write(make([]byte, pad(b.Len())))
	for _, t := range tensors {
		le(t.data)
		b.Write(make([]byte, pad(4*len(t.data))))
	}
	return b.Bytes()
}

// AiTinyQwen writes the synthetic model and adapter into dir and returns
// their paths and SHA-256 digests.
func AiTinyQwen(dir string) (model, modelSum, adapter, adapterSum string, err error) {
	rng := rand.New(rand.NewSource(7))
	normal := func(n int, scale float64) []float32 {
		out := make([]float32, n)
		for i := range out {
			out[i] = float32(rng.NormFloat64() * scale)
		}
		return out
	}
	ones := func(n int) []float32 {
		out := make([]float32, n)
		for i := range out {
			out[i] = 1
		}
		return out
	}
	runes := tinyByteRunes()
	var tokens []string
	var types []int32
	for b := 0; b < 256; b++ {
		tokens = append(tokens, string(runes[b]))
		types = append(types, 1)
	}
	for _, s := range []string{"<|endoftext|>", "<|im_start|>", "<|im_end|>"} {
		tokens = append(tokens, s)
		types = append(types, 3)
	}
	vocab := len(tokens)
	headDim := tinyEmbd / tinyHeads
	kvDim := tinyKVHeads * headDim
	tmpl := "{% for message in messages %}{{'<|im_start|>' + message['role'] + '\n' + message['content'] + '<|im_end|>' + '\n'}}{% endfor %}{% if add_generation_prompt %}{{ '<|im_start|>assistant\n' }}{% endif %}"
	kvs := []tinyKV{
		{"general.architecture", "qwen2"},
		{"general.name", "sky-tiny-qwen2"},
		{"qwen2.context_length", uint32(2048)},
		{"qwen2.embedding_length", uint32(tinyEmbd)},
		{"qwen2.block_count", uint32(tinyLayers)},
		{"qwen2.feed_forward_length", uint32(tinyFF)},
		{"qwen2.attention.head_count", uint32(tinyHeads)},
		{"qwen2.attention.head_count_kv", uint32(tinyKVHeads)},
		{"qwen2.rope.freq_base", float32(1e6)},
		{"qwen2.attention.layer_norm_rms_epsilon", float32(1e-6)},
		{"tokenizer.ggml.model", "gpt2"},
		{"tokenizer.ggml.pre", "qwen2"},
		{"tokenizer.ggml.tokens", tokens},
		{"tokenizer.ggml.token_type", types},
		{"tokenizer.ggml.merges", []string{}},
		{"tokenizer.ggml.eos_token_id", uint32(vocab - 1)},
		{"tokenizer.ggml.padding_token_id", uint32(256)},
		{"tokenizer.chat_template", tmpl},
	}
	ts := []tinyTensor{
		{"token_embd.weight", []uint64{tinyEmbd, uint64(vocab)}, normal(tinyEmbd*vocab, 0.02)},
		{"output_norm.weight", []uint64{tinyEmbd}, ones(tinyEmbd)},
		{"output.weight", []uint64{tinyEmbd, uint64(vocab)}, normal(tinyEmbd*vocab, 0.02)},
	}
	for l := 0; l < tinyLayers; l++ {
		p := "blk." + itoa(l) + "."
		ts = append(ts,
			tinyTensor{p + "attn_norm.weight", []uint64{tinyEmbd}, ones(tinyEmbd)},
			tinyTensor{p + "attn_q.weight", []uint64{tinyEmbd, tinyEmbd}, normal(tinyEmbd*tinyEmbd, 0.02)},
			tinyTensor{p + "attn_q.bias", []uint64{tinyEmbd}, normal(tinyEmbd, 0.02)},
			tinyTensor{p + "attn_k.weight", []uint64{tinyEmbd, uint64(kvDim)}, normal(tinyEmbd*kvDim, 0.02)},
			tinyTensor{p + "attn_k.bias", []uint64{uint64(kvDim)}, normal(kvDim, 0.02)},
			tinyTensor{p + "attn_v.weight", []uint64{tinyEmbd, uint64(kvDim)}, normal(tinyEmbd*kvDim, 0.02)},
			tinyTensor{p + "attn_v.bias", []uint64{uint64(kvDim)}, normal(kvDim, 0.02)},
			tinyTensor{p + "attn_output.weight", []uint64{tinyEmbd, tinyEmbd}, normal(tinyEmbd*tinyEmbd, 0.02)},
			tinyTensor{p + "ffn_norm.weight", []uint64{tinyEmbd}, ones(tinyEmbd)},
			tinyTensor{p + "ffn_gate.weight", []uint64{tinyEmbd, tinyFF}, normal(tinyEmbd*tinyFF, 0.02)},
			tinyTensor{p + "ffn_up.weight", []uint64{tinyEmbd, tinyFF}, normal(tinyEmbd*tinyFF, 0.02)},
			tinyTensor{p + "ffn_down.weight", []uint64{tinyFF, tinyEmbd}, normal(tinyFF*tinyEmbd, 0.02)},
		)
	}
	var lora []tinyTensor
	for l := 0; l < tinyLayers; l++ {
		p := "blk." + itoa(l) + ".attn_q.weight."
		lora = append(lora,
			tinyTensor{p + "lora_a", []uint64{tinyEmbd, tinyRank}, normal(tinyEmbd*tinyRank, 0.05)},
			tinyTensor{p + "lora_b", []uint64{tinyRank, tinyEmbd}, normal(tinyRank*tinyEmbd, 0.05)},
		)
	}
	adapterKVs := []tinyKV{
		{"general.architecture", "qwen2"},
		{"general.type", "adapter"},
		{"adapter.type", "lora"},
		{"adapter.lora.alpha", float32(8)},
		{"general.name", "sky-tiny-qwen2-lora"},
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	write := func(name string, data []byte) (string, string, error) {
		p := filepath.Join(dir, name)
		s := sha256.Sum256(data)
		return p, hex.EncodeToString(s[:]), os.WriteFile(p, data, 0o644)
	}
	if model, modelSum, err = write("tiny-qwen2.gguf", tinyGGUF(kvs, ts)); err != nil {
		return
	}
	adapter, adapterSum, err = write("tiny-qwen2-lora.gguf", tinyGGUF(adapterKVs, lora))
	return
}
