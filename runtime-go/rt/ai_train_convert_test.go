//go:build !js

package rt

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var tinyShape = aiTrainShape{arch: "qwen2", layers: 2, hidden: 8, intermediate: 16, qDim: 8, kv: 4}

// peftDir writes a PEFT adapter for tinyShape: q_proj and v_proj of layer 1,
// rank 2, with distinct values so the bytes can be found in the output.
func peftDir(t *testing.T, mutate func(map[string]aiTensor)) string {
	t.Helper()
	dir := t.TempDir()
	seq := float32(0)
	mk := func(rows, cols int) aiTensor {
		d := make([]float32, rows*cols)
		for i := range d {
			seq += 0.125
			d[i] = seq
		}
		return aiTensor{shape: []int{rows, cols}, data: d}
	}
	ts := map[string]aiTensor{
		"base_model.model.model.layers.1.self_attn.q_proj.lora_A.weight": mk(2, 8),
		"base_model.model.model.layers.1.self_attn.q_proj.lora_B.weight": mk(8, 2),
		"base_model.model.model.layers.1.self_attn.v_proj.lora_A.weight": mk(2, 8),
		"base_model.model.model.layers.1.self_attn.v_proj.lora_B.weight": mk(4, 2),
	}
	if mutate != nil {
		mutate(ts)
	}
	os.WriteFile(filepath.Join(dir, "adapter_model.safetensors"), aiSafetensorsBytes(ts, map[string]string{"format": "pt"}), 0o644)
	conf, _ := json.Marshal(map[string]any{"peft_type": "LORA", "r": 2, "lora_alpha": 4})
	os.WriteFile(filepath.Join(dir, "adapter_config.json"), conf, 0o644)
	return dir
}

func f32le(v []float32) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, v)
	return b.Bytes()
}

func TestConvertPeftWritesALlamaCppLoraAdapter(t *testing.T) {
	dir := peftDir(t, nil)
	sha := strings.Repeat("ab", 32)
	data, n, err := aiConvertPeft(dir, tinyShape, "Qwen/Qwen2.5-0.5B-Instruct@7ae5576", sha)
	if err != nil || n != 4 {
		t.Fatalf("%d %v", n, err)
	}
	out := filepath.Join(t.TempDir(), "adapter.gguf")
	os.WriteFile(out, data, 0o644)
	kv, infos, err := readGGUFHeader(out)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"general.architecture": "qwen2", "general.type": "adapter", "adapter.type": "lora",
		"sky.adapter.base_id": "Qwen/Qwen2.5-0.5B-Instruct@7ae5576", "sky.adapter.serving_sha256": sha,
	} {
		if kv[k] != want {
			t.Errorf("%s = %v, want %v", k, kv[k], want)
		}
	}
	if a, ok := kv["adapter.lora.alpha"].(float32); !ok || a != 4 {
		t.Errorf("alpha %v", kv["adapter.lora.alpha"])
	}
	got := map[string]string{}
	for _, ti := range infos {
		got[ti.name] = fmt.Sprint(ti.dims)
		if ti.typ != ggmlTypeF32 {
			t.Errorf("%s type %d", ti.name, ti.typ)
		}
	}
	want := map[string]string{
		"blk.1.attn_q.weight.lora_a": "[8 2]", "blk.1.attn_q.weight.lora_b": "[2 8]",
		"blk.1.attn_v.weight.lora_a": "[8 2]", "blk.1.attn_v.weight.lora_b": "[2 4]",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("tensors %v, want %v", got, want)
	}
	// The data is written as stored, row-major, 32-byte aligned.
	src, _ := aiReadSafetensors(filepath.Join(dir, "adapter_model.safetensors"))
	b := src["base_model.model.model.layers.1.self_attn.v_proj.lora_B.weight"].data
	i := bytes.Index(data, f32le(b))
	if i < 0 || i%32 != 0 {
		t.Fatalf("v_proj lora_b at %d", i)
	}
	// The registry accepts it as a GGUF LoRA adapter, and its base is readable.
	format, kind, arch, err := aiAdapterInfo(out)
	if err != nil || format != "gguf" || kind != "lora" || arch != "qwen2" {
		t.Fatalf("registry check %s %s %s %v", format, kind, arch, err)
	}
	if aiAdapterServingSha(out) != sha {
		t.Fatal("the serving digest does not read back")
	}
	// Deterministic: the same adapter converts to the same bytes.
	again, _, _ := aiConvertPeft(dir, tinyShape, "Qwen/Qwen2.5-0.5B-Instruct@7ae5576", sha)
	if !bytes.Equal(data, again) {
		t.Fatal("two conversions differ")
	}
}

func TestConvertPeftRefusesAnAdapterForAnotherModel(t *testing.T) {
	cases := map[string]func(map[string]aiTensor){
		"a layer the model lacks": func(ts map[string]aiTensor) {
			ts["base_model.model.model.layers.9.self_attn.q_proj.lora_A.weight"] = aiTensor{shape: []int{2, 8}, data: make([]float32, 16)}
			ts["base_model.model.model.layers.9.self_attn.q_proj.lora_B.weight"] = aiTensor{shape: []int{8, 2}, data: make([]float32, 16)}
		},
		"another hidden size": func(ts map[string]aiTensor) {
			ts["base_model.model.model.layers.1.self_attn.q_proj.lora_A.weight"] = aiTensor{shape: []int{2, 12}, data: make([]float32, 24)}
		},
		"another output size": func(ts map[string]aiTensor) {
			ts["base_model.model.model.layers.1.self_attn.v_proj.lora_B.weight"] = aiTensor{shape: []int{8, 2}, data: make([]float32, 16)}
		},
	}
	for name, m := range cases {
		_, _, err := aiConvertPeft(peftDir(t, m), tinyShape, "base", strings.Repeat("0", 64))
		if f, ok := err.(*aiFailure); !ok || f.code != aiAdapterMismatch {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, _, err := aiConvertPeft(peftDir(t, func(ts map[string]aiTensor) {
		delete(ts, "base_model.model.model.layers.1.self_attn.v_proj.lora_B.weight")
	}), tinyShape, "base", strings.Repeat("0", 64))
	if f, ok := err.(*aiFailure); !ok || f.code != aiTrainingFailed {
		t.Fatalf("a lone A matrix: %v", err)
	}
}

func TestReadSafetensorsWidensHalfPrecision(t *testing.T) {
	// Header for one F16 and one BF16 tensor of 2 values each.
	hdr := `{"h":{"dtype":"F16","shape":[2],"data_offsets":[0,4]},"b":{"dtype":"BF16","shape":[2],"data_offsets":[4,8]}}`
	for len(hdr)%8 != 0 {
		hdr += " "
	}
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint64(len(hdr)))
	buf.WriteString(hdr)
	binary.Write(&buf, binary.LittleEndian, []uint16{0x3c00, 0xc000}) // 1.0, -2.0
	bf := func(f float32) uint16 { return uint16(math.Float32bits(f) >> 16) }
	binary.Write(&buf, binary.LittleEndian, []uint16{bf(0.5), bf(-3)})
	p := filepath.Join(t.TempDir(), "t.safetensors")
	os.WriteFile(p, buf.Bytes(), 0o644)
	ts, err := aiReadSafetensors(p)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ts["h"].data) != "[1 -2]" || fmt.Sprint(ts["b"].data) != "[0.5 -3]" {
		t.Fatalf("%v %v", ts["h"].data, ts["b"].data)
	}
	// A tensor that points outside the file is refused.
	bad := strings.Replace(buf.String(), `[4,8]`, `[4,80]`, 1)
	os.WriteFile(p, []byte(bad), 0o644)
	if _, err := aiReadSafetensors(p); err == nil {
		t.Fatal("an out-of-range tensor was read")
	}
}

func TestTrainConvertInTestModeBindsTheServingDigest(t *testing.T) {
	withTrainTestMode(t)
	run := t.TempDir()
	os.MkdirAll(filepath.Join(run, "adapter"), 0o755)
	// The dev shape's v_proj: 896 in, 128 out.
	ts := map[string]aiTensor{
		"base_model.model.model.layers.23.self_attn.v_proj.lora_A.weight": {shape: []int{1, 896}, data: make([]float32, 896)},
		"base_model.model.model.layers.23.self_attn.v_proj.lora_B.weight": {shape: []int{128, 1}, data: make([]float32, 128)},
	}
	os.WriteFile(filepath.Join(run, "adapter", "adapter_model.safetensors"), aiSafetensorsBytes(ts, nil), 0o644)
	os.WriteFile(filepath.Join(run, "adapter", "adapter_config.json"), []byte(`{"peft_type":"LORA","r":1,"lora_alpha":2}`), 0o644)
	ref := "hf:" + aiDevModelRepo + "/" + aiDevModelFile + "#sha256=" + aiDevModelSha256
	v, err := aiTrainConvert(run, ref, "Qwen/Qwen2.5-0.5B-Instruct@7ae5576")
	if err != nil {
		t.Fatal(err)
	}
	if aiAdapterServingSha(v["path"].(string)) != aiDevModelSha256 || v["tensors"].(int) != 2 {
		t.Fatalf("%v", v)
	}
	m, err := aiTrainMerge(run, ref)
	if err != nil || !fileExists(m["path"].(string)) {
		t.Fatalf("merge in test mode %v %v", m, err)
	}
}

// A local slot given a trained adapter (Local.withAdapter) loads it with
// --lora; the same adapter converted for another model is AdapterMismatch
// before any engine starts.
func TestLocalWithAdapterLoadsATrainedAdapterOnItsOwnModelOnly(t *testing.T) {
	dir, model := aiTestEnv(t, nil)
	base := model[strings.LastIndex(model, "=")+1:]
	write := func(serving string) (string, string) {
		data := aiGGUFBytes([]ggufOutKV{
			{"general.architecture", ggufTypeString, "qwen2"},
			{"general.type", ggufTypeString, "adapter"},
			{"adapter.type", ggufTypeString, "lora"},
			{"sky.adapter.serving_sha256", ggufTypeString, serving},
		}, []ggufOutTensor{{name: "blk.0.attn_q.weight.lora_a", dims: []uint64{8, 1}, data: make([]float32, 8)}})
		p := filepath.Join(t.TempDir(), "adapter.gguf")
		os.WriteFile(p, data, 0o644)
		return p, sha(data)
	}
	p, sum := write(base)
	s := localSlot(t, model, func(b *aiEndpointBuilder) { b.local.adapter = "file:" + p + "#sha256=" + sum })
	if _, err := s.post("/chat/completions", chatBody("hello")); err != nil {
		t.Fatal(err)
	}
	launch, _ := os.ReadFile(filepath.Join(dir, "launches.log"))
	if !strings.Contains(string(launch), "--lora "+p) {
		t.Fatalf("the engine was not started with the adapter: %s", launch)
	}

	aiResetSlots()
	other, otherSum := write(strings.Repeat("0", 64))
	s2 := localSlot(t, model, func(b *aiEndpointBuilder) {
		b.slot = "other"
		b.local.adapter = "file:" + other + "#sha256=" + otherSum
	})
	_, err := s2.post("/chat/completions", chatBody("hello"))
	f, ok := err.(*aiFailure)
	if !ok || f.code != aiAdapterMismatch {
		t.Fatalf("an adapter for another model: %v", err)
	}
	if eb, _ := f.attrValue("engineBase"); eb != base {
		t.Fatalf("engineBase %q", eb)
	}
	// A wrong digest is ChecksumMismatch; a non-file adapter is refused.
	aiResetSlots()
	s3 := localSlot(t, model, func(b *aiEndpointBuilder) {
		b.slot = "third"
		b.local.adapter = "file:" + p + "#sha256=" + strings.Repeat("1", 64)
	})
	if _, err := s3.post("/chat/completions", chatBody("hello")); err == nil || err.(*aiFailure).code != aiChecksumMismatch {
		t.Fatalf("a wrong digest: %v", err)
	}
	if _, err := resolveAiLocal("", aiLocalBuilder{model: model, adapter: "hf:a/b/c.gguf#sha256=" + sum, context: -1, parallel: -1, idleUnload: -1, port: -1}, "", func(aiSetting) {}); err == nil {
		t.Fatal("an hf: adapter was accepted")
	}
}
