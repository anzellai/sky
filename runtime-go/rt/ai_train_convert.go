//go:build !js

package rt

// ai_train_convert.go — a trained PEFT adapter becomes a GGUF adapter that
// the managed llama-server loads with --lora, and (on request) a merged GGUF
// model.
//
// The conversion is Go, not Python. A LoRA adapter in GGUF is a header with a
// few metadata keys and one pair of F32 matrices per adapted projection:
//
//	general.architecture   the serving model's (qwen2, llama, …)
//	general.type           "adapter"
//	adapter.type           "lora"
//	adapter.lora.alpha     PEFT's lora_alpha
//	sky.adapter.base_id    the base the adapter was trained on (repo@revision)
//	sky.adapter.serving_sha256
//	                       the serving GGUF's digest: Std.Ai.Local refuses the
//	                       adapter on any other model (AdapterMismatch)
//
//	blk.<n>.<tensor>.weight.lora_a   PEFT lora_A (rank, in) as stored
//	blk.<n>.<tensor>.weight.lora_b   PEFT lora_B (out, rank) as stored
//
// which is the layout llama.cpp's convert_lora_to_gguf.py writes (GGUF lists a
// tensor's dimensions innermost first, so (rank, in) is written [in, rank]).
// Doing it in Go keeps PyTorch out of the toolchain and makes the converter a
// tested part of the runtime. The output is checked by reading it back, and
// the live test loads it into a real llama-server.
//
// The merged export runs `llama-export-lora` from the pinned engine build: the
// adapter is folded into the serving model's weights and written as a new
// GGUF model.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ─── safetensors ────────────────────────────────────────────────────────

// aiTensor is an F32 tensor in row-major order.
type aiTensor struct {
	shape []int
	data  []float32
}

// aiSafetensorsBytes encodes tensors as a safetensors file (F32, names sorted).
func aiSafetensorsBytes(tensors map[string]aiTensor, meta map[string]string) []byte {
	names := make([]string, 0, len(tensors))
	for n := range tensors {
		names = append(names, n)
	}
	sort.Strings(names)
	header := map[string]any{}
	off := 0
	for _, n := range names {
		t := tensors[n]
		header[n] = map[string]any{"dtype": "F32", "shape": t.shape, "data_offsets": []int{off, off + 4*len(t.data)}}
		off += 4 * len(t.data)
	}
	if len(meta) > 0 {
		header["__metadata__"] = meta
	}
	h, _ := json.Marshal(header)
	for len(h)%8 != 0 {
		h = append(h, ' ')
	}
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint64(len(h)))
	buf.Write(h)
	for _, n := range names {
		binary.Write(&buf, binary.LittleEndian, tensors[n].data)
	}
	return buf.Bytes()
}

// aiReadSafetensors reads a safetensors file's tensors as F32 (F16 and BF16
// are widened). The header is bounded and every offset is checked against the
// file, because an adapter is untrusted input.
func aiReadSafetensors(path string) (map[string]aiTensor, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 8 {
		return nil, fmt.Errorf("%s is too short to be a safetensors file", path)
	}
	n := binary.LittleEndian.Uint64(b[:8])
	if n == 0 || n > 100<<20 || 8+n > uint64(len(b)) {
		return nil, fmt.Errorf("%s: bad safetensors header length", path)
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(b[8:8+n], &header); err != nil {
		return nil, fmt.Errorf("%s: the safetensors header is not JSON: %v", path, err)
	}
	body := b[8+n:]
	out := map[string]aiTensor{}
	for name, raw := range header {
		if name == "__metadata__" {
			continue
		}
		var info struct {
			Dtype   string   `json:"dtype"`
			Shape   []int    `json:"shape"`
			Offsets []uint64 `json:"data_offsets"`
		}
		if err := json.Unmarshal(raw, &info); err != nil || len(info.Offsets) != 2 {
			return nil, fmt.Errorf("%s: tensor %s has a bad header", path, name)
		}
		a, e := info.Offsets[0], info.Offsets[1]
		if a > e || e > uint64(len(body)) {
			return nil, fmt.Errorf("%s: tensor %s lies outside the file", path, name)
		}
		count := 1
		for _, d := range info.Shape {
			if d < 0 || d > 1<<24 {
				return nil, fmt.Errorf("%s: tensor %s has a bad shape", path, name)
			}
			count *= d
		}
		raw := body[a:e]
		vals := make([]float32, count)
		switch info.Dtype {
		case "F32":
			if len(raw) != 4*count {
				return nil, fmt.Errorf("%s: tensor %s has %d bytes for %d values", path, name, len(raw), count)
			}
			for i := range vals {
				vals[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
			}
		case "F16":
			if len(raw) != 2*count {
				return nil, fmt.Errorf("%s: tensor %s has %d bytes for %d values", path, name, len(raw), count)
			}
			for i := range vals {
				vals[i] = aiHalfToFloat(binary.LittleEndian.Uint16(raw[2*i:]))
			}
		case "BF16":
			if len(raw) != 2*count {
				return nil, fmt.Errorf("%s: tensor %s has %d bytes for %d values", path, name, len(raw), count)
			}
			for i := range vals {
				vals[i] = math.Float32frombits(uint32(binary.LittleEndian.Uint16(raw[2*i:])) << 16)
			}
		default:
			return nil, fmt.Errorf("%s: tensor %s is %s; an adapter is F32, F16 or BF16", path, name, info.Dtype)
		}
		out[name] = aiTensor{shape: info.Shape, data: vals}
	}
	return out, nil
}

func aiHalfToFloat(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h) & 0x3ff
	switch {
	case exp == 0 && frac == 0:
		return math.Float32frombits(sign)
	case exp == 0:
		// subnormal
		for frac&0x400 == 0 {
			frac <<= 1
			exp--
		}
		exp++
		frac &= 0x3ff
	case exp == 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | frac<<13)
	}
	return math.Float32frombits(sign | (exp+112)<<23 | frac<<13)
}

// ─── GGUF headers with tensor infos ─────────────────────────────────────

type ggufTensorInfo struct {
	name string
	dims []uint64
	typ  uint32
}

// readGGUFHeader reads the metadata and the tensor table of a GGUF file.
func readGGUFHeader(path string) (map[string]any, []ggufTensorInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	var hdr struct {
		Magic   [4]byte
		Version uint32
		Tensors uint64
		KV      uint64
	}
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil || string(hdr.Magic[:]) != "GGUF" {
		return nil, nil, fmt.Errorf("not a GGUF file")
	}
	if hdr.Tensors > 1<<20 || hdr.KV > ggufMaxKV {
		return nil, nil, fmt.Errorf("GGUF header declares %d tensors and %d entries", hdr.Tensors, hdr.KV)
	}
	// Re-read the metadata with the shared bounded parser.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	r = bufio.NewReaderSize(f, 1<<16)
	kv, err := parseGGUFKV(r)
	if err != nil {
		return nil, nil, err
	}
	infos := make([]ggufTensorInfo, 0, hdr.Tensors)
	for i := uint64(0); i < hdr.Tensors; i++ {
		name, err := ggufString(r)
		if err != nil {
			return nil, nil, fmt.Errorf("GGUF tensor %d: %v", i, err)
		}
		var nd uint32
		if err := binary.Read(r, binary.LittleEndian, &nd); err != nil || nd > 8 {
			return nil, nil, fmt.Errorf("GGUF tensor %s: bad dimension count", name)
		}
		dims := make([]uint64, nd)
		if err := binary.Read(r, binary.LittleEndian, dims); err != nil {
			return nil, nil, fmt.Errorf("GGUF tensor %s: %v", name, err)
		}
		var typ uint32
		var off uint64
		if err := binary.Read(r, binary.LittleEndian, &typ); err != nil {
			return nil, nil, fmt.Errorf("GGUF tensor %s: %v", name, err)
		}
		if err := binary.Read(r, binary.LittleEndian, &off); err != nil {
			return nil, nil, fmt.Errorf("GGUF tensor %s: %v", name, err)
		}
		infos = append(infos, ggufTensorInfo{name: name, dims: dims, typ: typ})
	}
	return kv, infos, nil
}

// ─── GGUF writing ───────────────────────────────────────────────────────

const (
	ggufTypeUint32  = 4
	ggufTypeFloat32 = 6
	ggufTypeString  = 8
	ggmlTypeF32     = 0
	ggufAlignment   = 32
)

type ggufOutKV struct {
	key string
	typ uint32
	val any
}

type ggufOutTensor struct {
	name string
	dims []uint64 // innermost first
	data []float32
}

func writeGGUFString(w io.Writer, s string) {
	binary.Write(w, binary.LittleEndian, uint64(len(s)))
	io.WriteString(w, s)
}

// aiGGUFBytes encodes a GGUF v3 file with F32 tensors.
func aiGGUFBytes(kvs []ggufOutKV, tensors []ggufOutTensor) []byte {
	var b bytes.Buffer
	b.WriteString("GGUF")
	binary.Write(&b, binary.LittleEndian, uint32(3))
	binary.Write(&b, binary.LittleEndian, uint64(len(tensors)))
	binary.Write(&b, binary.LittleEndian, uint64(len(kvs)))
	for _, kv := range kvs {
		writeGGUFString(&b, kv.key)
		binary.Write(&b, binary.LittleEndian, kv.typ)
		switch v := kv.val.(type) {
		case string:
			writeGGUFString(&b, v)
		case float32:
			binary.Write(&b, binary.LittleEndian, v)
		case uint32:
			binary.Write(&b, binary.LittleEndian, v)
		}
	}
	pad := func(n int) uint64 { return uint64((ggufAlignment - n%ggufAlignment) % ggufAlignment) }
	offset := uint64(0)
	for _, t := range tensors {
		writeGGUFString(&b, t.name)
		binary.Write(&b, binary.LittleEndian, uint32(len(t.dims)))
		binary.Write(&b, binary.LittleEndian, t.dims)
		binary.Write(&b, binary.LittleEndian, uint32(ggmlTypeF32))
		binary.Write(&b, binary.LittleEndian, offset)
		size := 4 * len(t.data)
		offset += uint64(size) + pad(size)
	}
	b.Write(make([]byte, pad(b.Len())))
	for _, t := range tensors {
		binary.Write(&b, binary.LittleEndian, t.data)
		b.Write(make([]byte, pad(4*len(t.data))))
	}
	return b.Bytes()
}

// aiGGUFTensorNames maps a Hugging Face projection to its GGUF tensor, for the
// llama family (llama, qwen2, qwen3, mistral, gemma: the same names).
var aiGGUFTensorNames = map[string]string{
	"self_attn.q_proj": "attn_q",
	"self_attn.k_proj": "attn_k",
	"self_attn.v_proj": "attn_v",
	"self_attn.o_proj": "attn_output",
	"mlp.gate_proj":    "ffn_gate",
	"mlp.up_proj":      "ffn_up",
	"mlp.down_proj":    "ffn_down",
}

// aiPeftTensorName parses `base_model.model.model.layers.<n>.<proj>.lora_<A|B>.weight`.
func aiPeftTensorName(name string) (layer int, proj string, ab string, ok bool) {
	rest, found := strings.CutPrefix(name, "base_model.model.model.layers.")
	if !found {
		return 0, "", "", false
	}
	num, rest, found := strings.Cut(rest, ".")
	if !found {
		return 0, "", "", false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 0 {
		return 0, "", "", false
	}
	switch {
	case strings.HasSuffix(rest, ".lora_A.weight"):
		return n, strings.TrimSuffix(rest, ".lora_A.weight"), "a", true
	case strings.HasSuffix(rest, ".lora_B.weight"):
		return n, strings.TrimSuffix(rest, ".lora_B.weight"), "b", true
	}
	return 0, "", "", false
}

// aiConvertPeft turns a PEFT adapter directory into GGUF adapter bytes for a
// serving model of the given shape. Every matrix is checked against the
// shape: an adapter trained on another model is AdapterMismatch here, before
// an engine sees it.
func aiConvertPeft(dir string, s aiTrainShape, baseID, servingSha string) ([]byte, int, error) {
	confB, err := os.ReadFile(filepath.Join(dir, "adapter_config.json"))
	if err != nil {
		return nil, 0, aiTrainFail("the adapter has no adapter_config.json: %v", err)
	}
	var conf struct {
		R     float64 `json:"r"`
		Alpha float64 `json:"lora_alpha"`
		Type  string  `json:"peft_type"`
	}
	if err := json.Unmarshal(confB, &conf); err != nil || conf.R <= 0 {
		return nil, 0, aiTrainFail("the adapter's adapter_config.json has no rank")
	}
	if conf.Type != "" && conf.Type != "LORA" {
		return nil, 0, aiTrainFail("the adapter is %s, not LORA", conf.Type)
	}
	tensors, err := aiReadSafetensors(filepath.Join(dir, "adapter_model.safetensors"))
	if err != nil {
		return nil, 0, aiTrainFail("%v", err)
	}
	mismatch := func(format string, args ...any) error {
		return aiFailf(aiAdapterMismatch, "the adapter does not fit the serving model: "+format, args...).
			with("adapter", dir).with("adapterBase", baseID).with("engineBase", servingSha)
	}
	type pair struct{ a, b *aiTensor }
	pairs := map[string]*pair{}
	var keys []string
	for name, t := range tensors {
		layer, proj, ab, ok := aiPeftTensorName(name)
		if !ok {
			return nil, 0, aiTrainFail("the adapter tensor %s is not a LoRA matrix of a decoder layer", name)
		}
		g, known := aiGGUFTensorNames[proj]
		if !known {
			return nil, 0, aiTrainFail("the adapter adapts %s, which has no GGUF tensor", proj)
		}
		if layer >= s.layers {
			return nil, 0, mismatch("it adapts layer %d and the model has %d", layer, s.layers)
		}
		key := fmt.Sprintf("blk.%d.%s.weight", layer, g)
		p := pairs[key]
		if p == nil {
			p = &pair{}
			pairs[key] = p
			keys = append(keys, key)
		}
		t := t
		if len(t.shape) != 2 {
			return nil, 0, aiTrainFail("the adapter tensor %s is not a matrix", name)
		}
		in, out, _ := aiTargetDims(proj, s)
		if ab == "a" {
			if t.shape[1] != in {
				return nil, 0, mismatch("%s takes %d inputs and the model's %s takes %d", name, t.shape[1], proj, in)
			}
			p.a = &t
		} else {
			if t.shape[0] != out {
				return nil, 0, mismatch("%s gives %d outputs and the model's %s gives %d", name, t.shape[0], proj, out)
			}
			p.b = &t
		}
	}
	sort.Strings(keys)
	var outT []ggufOutTensor
	for _, k := range keys {
		p := pairs[k]
		if p.a == nil || p.b == nil || p.a.shape[0] != p.b.shape[1] {
			return nil, 0, aiTrainFail("the adapter's %s has no matching A and B matrices", k)
		}
		outT = append(outT,
			ggufOutTensor{name: k + ".lora_a", dims: []uint64{uint64(p.a.shape[1]), uint64(p.a.shape[0])}, data: p.a.data},
			ggufOutTensor{name: k + ".lora_b", dims: []uint64{uint64(p.b.shape[1]), uint64(p.b.shape[0])}, data: p.b.data})
	}
	if len(outT) == 0 {
		return nil, 0, aiTrainFail("the adapter has no LoRA matrices")
	}
	kvs := []ggufOutKV{
		{"general.architecture", ggufTypeString, s.arch},
		{"general.type", ggufTypeString, "adapter"},
		{"general.name", ggufTypeString, "sky adapter for " + baseID},
		{"adapter.type", ggufTypeString, "lora"},
		{"adapter.lora.alpha", ggufTypeFloat32, float32(conf.Alpha)},
		{"general.alignment", ggufTypeUint32, uint32(ggufAlignment)},
		{"sky.adapter.base_id", ggufTypeString, baseID},
		{"sky.adapter.serving_sha256", ggufTypeString, servingSha},
	}
	return aiGGUFBytes(kvs, outT), len(outT), nil
}

// Ai_trainConvert : String -> String -> String -> Task Error Converted
//
// runDir, the serving model reference and the base id: write
// <run>/adapter.gguf from <run>/adapter/, read it back, and return its path
// and digest. Converting the same adapter again gives the same bytes.
func Ai_trainConvert(runArg, servingArg, baseArg any) any {
	runDir, serving, baseID := AsString(runArg), AsString(servingArg), AsString(baseArg)
	return func() any {
		v, err := aiTrainConvert(runDir, serving, baseID)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](v)
	}
}

func aiTrainConvert(runDir, serving, baseID string) (map[string]any, error) {
	ref, err := aiParseModelRef(serving)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), aiStartTimeout())
	defer cancel()
	s, _, err := aiTrainServingShape(ctx, serving)
	if err != nil {
		return nil, err
	}
	data, n, err := aiConvertPeft(filepath.Join(runDir, "adapter"), s, baseID, ref.sha256)
	if err != nil {
		return nil, err
	}
	out := filepath.Join(runDir, "adapter.gguf")
	if err := aiWriteFileAtomic(out, data); err != nil {
		return nil, aiTrainFail("cannot write %s: %v", out, err)
	}
	if err := aiCheckAdapterGGUF(out, s.arch, ref.sha256); err != nil {
		return nil, err
	}
	sum, err := aiFileSha256(out)
	if err != nil {
		return nil, aiTrainFail("cannot hash %s: %v", out, err)
	}
	abs, _ := filepath.Abs(out)
	return map[string]any{"path": abs, "sha256": sum, "tensors": n, "architecture": s.arch}, nil
}

// aiCheckAdapterGGUF reads an adapter back: a LoRA adapter for arch, bound to
// the serving model's digest.
func aiCheckAdapterGGUF(path, arch, servingSha string) error {
	kv, infos, err := readGGUFHeader(path)
	if err != nil {
		return aiTrainFail("the written adapter %s does not read back: %v", path, err)
	}
	gt, _ := kv["general.type"].(string)
	at, _ := kv["adapter.type"].(string)
	ga, _ := kv["general.architecture"].(string)
	ss, _ := kv["sky.adapter.serving_sha256"].(string)
	if gt != "adapter" || at != "lora" || ga != arch || ss != servingSha || len(infos) == 0 {
		return aiTrainFail("the written adapter %s does not read back as a %s LoRA adapter (type %q, adapter %q, architecture %q)", path, arch, gt, at, ga)
	}
	return nil
}

// aiAdapterServingSha reads the serving digest an adapter was converted for,
// "" when the adapter does not record one (a third-party adapter).
func aiAdapterServingSha(path string) string {
	kv, _, err := readGGUFHeader(path)
	if err != nil {
		return ""
	}
	s, _ := kv["sky.adapter.serving_sha256"].(string)
	return s
}

// ─── merged export ──────────────────────────────────────────────────────

// aiExportLoraBinary finds llama-export-lora next to the engine's
// llama-server (the pinned build, or the operator's).
func aiExportLoraBinary(ctx context.Context) (string, error) {
	eng, err := aiDiscoverEngine(ctx)
	if err != nil {
		return "", err
	}
	bin := filepath.Join(filepath.Dir(eng.path), aiExe("llama-export-lora"))
	if !fileExists(bin) {
		return "", aiFailf(aiNotProvisioned, "%s has no llama-export-lora; the merged export needs the llama.cpp tools next to llama-server", filepath.Dir(eng.path)).
			with("what", "llama-export-lora").with("command", "sky ai provision")
	}
	return bin, nil
}

// Ai_trainMerge : String -> String -> Task Error Converted
//
// Fold <run>/adapter.gguf into the serving model and write <run>/merged.gguf,
// a GGUF model an engine serves with no adapter.
func Ai_trainMerge(runArg, servingArg any) any {
	runDir, serving := AsString(runArg), AsString(servingArg)
	return func() any {
		v, err := aiTrainMerge(runDir, serving)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](v)
	}
}

func aiTrainMerge(runDir, serving string) (map[string]any, error) {
	adapter := filepath.Join(runDir, "adapter.gguf")
	if !fileExists(adapter) {
		return nil, aiTrainFail("%s does not exist; the merged export needs the GGUF adapter (Train.withGgufExport)", adapter)
	}
	out := filepath.Join(runDir, "merged.gguf")
	abs, _ := filepath.Abs(out)
	if testModeActive() {
		// No engine and no model in test mode: a model-shaped GGUF stands in.
		data := aiGGUFBytes([]ggufOutKV{
			{"general.architecture", ggufTypeString, "qwen2"},
			{"general.type", ggufTypeString, "model"},
			{"qwen2.block_count", ggufTypeUint32, uint32(1)},
		}, nil)
		if err := aiWriteFileAtomic(out, data); err != nil {
			return nil, aiTrainFail("cannot write %s: %v", out, err)
		}
		sum, _ := aiFileSha256(out)
		return map[string]any{"path": abs, "sha256": sum, "tensors": 0, "architecture": "qwen2"}, nil
	}
	ref, err := aiParseModelRef(serving)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	model, err := aiEnsureModel(ctx, ref)
	if err != nil {
		return nil, err
	}
	bin, err := aiExportLoraBinary(ctx)
	if err != nil {
		return nil, err
	}
	rnd, err := aiRandomHex(4)
	if err != nil {
		return nil, err
	}
	tmp := out + ".tmp-" + rnd
	defer os.Remove(tmp)
	cmd := exec.CommandContext(ctx, bin, "-m", model, "--lora", adapter, "-o", tmp)
	cmd.Env = aiEngineEnv()
	logs, err := cmd.CombinedOutput()
	if err != nil {
		return nil, aiFailf(aiToolchainFailed, "llama-export-lora failed: %v", err).with("tool", "llama-export-lora").with("logTail", aiTail(string(logs), 1200))
	}
	kv, infos, err := readGGUFHeader(tmp)
	if err != nil {
		return nil, aiTrainFail("the merged model does not read back: %v", err)
	}
	if gt, _ := kv["general.type"].(string); gt == "adapter" || len(infos) == 0 {
		return nil, aiTrainFail("the merged file is not a model")
	}
	if err := os.Rename(tmp, out); err != nil {
		return nil, aiTrainFail("cannot write %s: %v", out, err)
	}
	sum, err := aiFileSha256(out)
	if err != nil {
		return nil, aiTrainFail("cannot hash %s: %v", out, err)
	}
	arch, _ := kv["general.architecture"].(string)
	return map[string]any{"path": abs, "sha256": sum, "tensors": len(infos), "architecture": arch}, nil
}
