//go:build !js

package rt

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// readTinyGGUFTensors reads the F32 tensors of a GGUF file the way llama.cpp
// lays them out: the tensor infos after the metadata, the data from the next
// 32-byte boundary.
func readTinyGGUFTensors(t *testing.T, path string) (map[string]any, map[string]aiTensor) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	nTensors := binary.LittleEndian.Uint64(b[8:16])
	r := bytes.NewReader(b)
	kv, err := parseGGUFKV(r)
	if err != nil {
		t.Fatal(err)
	}
	type info struct {
		name string
		dims []uint64
		off  uint64
	}
	var infos []info
	for i := uint64(0); i < nTensors; i++ {
		name, err := ggufString(r)
		if err != nil {
			t.Fatal(err)
		}
		var nd uint32
		_ = binary.Read(r, binary.LittleEndian, &nd)
		dims := make([]uint64, nd)
		_ = binary.Read(r, binary.LittleEndian, dims)
		var typ uint32
		var off uint64
		_ = binary.Read(r, binary.LittleEndian, &typ)
		_ = binary.Read(r, binary.LittleEndian, &off)
		if typ != 0 {
			t.Fatalf("tensor %s is type %d, not F32", name, typ)
		}
		infos = append(infos, info{name, dims, off})
	}
	pos := len(b) - r.Len()
	start := (pos + 31) / 32 * 32
	out := map[string]aiTensor{}
	for _, in := range infos {
		n := 1
		shape := make([]int, len(in.dims))
		for i, d := range in.dims {
			n *= int(d)
			shape[len(in.dims)-1-i] = int(d)
		}
		data := make([]float32, n)
		at := start + int(in.off)
		for k := 0; k < n; k++ {
			data[k] = math.Float32frombits(binary.LittleEndian.Uint32(b[at+4*k:]))
		}
		out[in.name] = aiTensor{shape: shape, data: data}
	}
	return kv, out
}

func TestTinyQwenHFTwinHasTheGGUFTensors(t *testing.T) {
	dir := t.TempDir()
	model, _, _, _, err := AiTinyQwen(dir)
	if err != nil {
		t.Fatal(err)
	}
	hf, err := AiTinyQwenHF(filepath.Join(dir, "hf"))
	if err != nil {
		t.Fatal(err)
	}
	kv, gg := readTinyGGUFTensors(t, model)
	st, err := aiReadSafetensors(filepath.Join(hf, "model.safetensors"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != len(gg) {
		t.Fatalf("the twin has %d tensors and the GGUF %d", len(st), len(gg))
	}
	for name, g := range gg {
		hfName, ok := tinyHFName(name)
		if !ok {
			t.Fatalf("no Hugging Face name for %s", name)
		}
		h, ok := st[hfName]
		if !ok {
			t.Fatalf("the twin has no %s (GGUF %s)", hfName, name)
		}
		if !reflect.DeepEqual(h.shape, g.shape) || !reflect.DeepEqual(h.data, g.data) {
			t.Fatalf("%s: twin %v differs from GGUF %s %v", hfName, h.shape, name, g.shape)
		}
	}
	// The same vocabulary, id for id, and the same chat template.
	tok, err := aiLoadTokenizer(hf)
	if err != nil {
		t.Fatal(err)
	}
	// The GGUF reader keeps no arrays, so the vocabulary comes from the
	// generator the GGUF writer ran.
	var tokens []string
	gkv, _ := tinyQwenModel(rand.New(rand.NewSource(7)), tinyLayers, 0)
	for _, e := range gkv {
		if e.key == "tokenizer.ggml.tokens" {
			tokens = e.val.([]string)
		}
	}
	if len(tokens) == 0 || tok.VocabSize() != len(tokens) {
		t.Fatalf("tokenizer vocabulary %d, GGUF %d", tok.VocabSize(), len(tokens))
	}
	for id, s := range tokens {
		got, ok := tok.vocab[s]
		if !ok {
			got, ok = tok.ID(s)
		}
		if !ok || got != id {
			t.Fatalf("token %q is %d in the twin and %d in the GGUF", s, got, id)
		}
	}
	var tc struct {
		ChatTemplate string `json:"chat_template"`
	}
	b, _ := os.ReadFile(filepath.Join(hf, "tokenizer_config.json"))
	_ = json.Unmarshal(b, &tc)
	if tc.ChatTemplate != kv["tokenizer.chat_template"] {
		t.Fatal("the twin's chat template differs from the GGUF's")
	}
	if _, err := aiLoadChatFormat(hf); err != nil {
		t.Fatalf("the twin's chat template is not accepted: %v", err)
	}
	var cfg struct {
		Arch   []string `json:"architectures"`
		Hidden int      `json:"hidden_size"`
		Layers int      `json:"num_hidden_layers"`
		Vocab  int      `json:"vocab_size"`
		Tied   bool     `json:"tie_word_embeddings"`
	}
	b, _ = os.ReadFile(filepath.Join(hf, "config.json"))
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Arch) != 1 || cfg.Arch[0] != "Qwen2ForCausalLM" || cfg.Hidden != tinyEmbd || cfg.Layers != tinyLayers ||
		cfg.Vocab != len(tokens) || cfg.Tied {
		t.Fatalf("config.json = %+v", cfg)
	}
	// The twin's shape is the shape the trainer and the converter use.
	s, err := aiTrainShapeOf(model)
	if err != nil || s.hidden != cfg.Hidden || s.layers != cfg.Layers || s.vocab != cfg.Vocab {
		t.Fatalf("the GGUF's shape %+v (%v) differs from config.json %+v", s, err, cfg)
	}
}
