//go:build !js

package rt

// ai_gguf.go — read a GGUF file's metadata and plan whether it fits.
//
// "If it compiles, it works" cannot prove that a model fits in memory. So
// Std.Ai.Local predicts it before it loads anything: it reads the model's
// shape from the GGUF header (layer count, KV heads, head size, trained
// context) and the host's memory, chooses GPU layers and a context that fit,
// and refuses with ModelTooLarge rather than let the engine page to disk.
//
// The reader is bounded. A GGUF file is untrusted input, so every length it
// declares is capped before anything is allocated for it, and the tokenizer
// arrays (hundreds of thousands of strings) are skipped, not stored.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// ggufMeta is the part of a GGUF header the fit plan needs.
type ggufMeta struct {
	arch          string
	blockCount    int
	contextLength int
	embedding     int
	headCount     int
	headCountKV   int
	keyLength     int
	valueLength   int
	fileType      int
	pooling       bool // the model declares a pooling type (an embedding model)
}

const (
	ggufMaxString = 1 << 20 // one metadata string
	ggufMaxArray  = 1 << 25 // one metadata array's element count
	ggufMaxKV     = 1 << 16 // metadata entries
)

// readGGUFMeta reads the metadata of the GGUF file at path.
func readGGUFMeta(path string) (ggufMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return ggufMeta{}, err
	}
	defer f.Close()
	return parseGGUFMeta(bufio.NewReaderSize(f, 1<<16))
}

func parseGGUFMeta(r io.Reader) (ggufMeta, error) {
	var hdr struct {
		Magic   [4]byte
		Version uint32
	}
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil {
		return ggufMeta{}, fmt.Errorf("not a GGUF file: %v", err)
	}
	if string(hdr.Magic[:]) != "GGUF" {
		return ggufMeta{}, fmt.Errorf("not a GGUF file (magic %q)", hdr.Magic[:])
	}
	if hdr.Version < 2 || hdr.Version > 3 {
		return ggufMeta{}, fmt.Errorf("GGUF version %d is not supported (2 or 3)", hdr.Version)
	}
	var counts struct{ Tensors, KV uint64 }
	if err := binary.Read(r, binary.LittleEndian, &counts); err != nil {
		return ggufMeta{}, fmt.Errorf("truncated GGUF header: %v", err)
	}
	if counts.KV > ggufMaxKV {
		return ggufMeta{}, fmt.Errorf("GGUF declares %d metadata entries (limit %d)", counts.KV, ggufMaxKV)
	}
	kv := map[string]any{}
	for i := uint64(0); i < counts.KV; i++ {
		key, err := ggufString(r)
		if err != nil {
			return ggufMeta{}, fmt.Errorf("GGUF metadata entry %d: %v", i, err)
		}
		var typ uint32
		if err := binary.Read(r, binary.LittleEndian, &typ); err != nil {
			return ggufMeta{}, fmt.Errorf("GGUF metadata %q: %v", key, err)
		}
		v, err := ggufValue(r, typ, 0)
		if err != nil {
			return ggufMeta{}, fmt.Errorf("GGUF metadata %q: %v", key, err)
		}
		if v != nil {
			kv[key] = v
		}
	}
	m := ggufMeta{}
	m.arch, _ = kv["general.architecture"].(string)
	if m.arch == "" {
		return ggufMeta{}, fmt.Errorf("GGUF has no general.architecture")
	}
	num := func(k string) int {
		switch x := kv[m.arch+"."+k].(type) {
		case uint64:
			if x > math.MaxInt32 {
				return math.MaxInt32
			}
			return int(x)
		case int64:
			if x < 0 || x > math.MaxInt32 {
				return 0
			}
			return int(x)
		}
		return 0
	}
	m.blockCount = num("block_count")
	m.contextLength = num("context_length")
	m.embedding = num("embedding_length")
	m.headCount = num("attention.head_count")
	m.headCountKV = num("attention.head_count_kv")
	m.keyLength = num("attention.key_length")
	m.valueLength = num("attention.value_length")
	_, m.pooling = kv[m.arch+".pooling_type"]
	if ft, ok := kv["general.file_type"].(uint64); ok {
		m.fileType = int(ft)
	}
	if m.headCountKV == 0 {
		m.headCountKV = m.headCount
	}
	if m.blockCount == 0 {
		return ggufMeta{}, fmt.Errorf("GGUF has no %s.block_count", m.arch)
	}
	return m, nil
}

func ggufString(r io.Reader) (string, error) {
	var n uint64
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return "", err
	}
	if n > ggufMaxString {
		return "", fmt.Errorf("string of %d bytes (limit %d)", n, ggufMaxString)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

// ggufValue reads one value of type typ. Integers come back as uint64 or
// int64, strings as string; arrays are read and dropped (nil), because the
// plan needs none of them and the tokenizer arrays are large.
func ggufValue(r io.Reader, typ uint32, depth int) (any, error) {
	read := func(v any) error { return binary.Read(r, binary.LittleEndian, v) }
	switch typ {
	case 0, 7: // uint8, bool
		var v uint8
		err := read(&v)
		return uint64(v), err
	case 1: // int8
		var v int8
		err := read(&v)
		return int64(v), err
	case 2:
		var v uint16
		err := read(&v)
		return uint64(v), err
	case 3:
		var v int16
		err := read(&v)
		return int64(v), err
	case 4:
		var v uint32
		err := read(&v)
		return uint64(v), err
	case 5:
		var v int32
		err := read(&v)
		return int64(v), err
	case 6:
		var v float32
		err := read(&v)
		return nil, err
	case 8:
		return ggufString(r)
	case 9:
		if depth > 2 {
			return nil, fmt.Errorf("arrays nested too deeply")
		}
		var et uint32
		var n uint64
		if err := read(&et); err != nil {
			return nil, err
		}
		if err := read(&n); err != nil {
			return nil, err
		}
		if n > ggufMaxArray {
			return nil, fmt.Errorf("array of %d elements (limit %d)", n, ggufMaxArray)
		}
		if size := ggufFixedSize(et); size > 0 {
			_, err := io.CopyN(io.Discard, r, int64(n)*int64(size))
			return nil, err
		}
		for i := uint64(0); i < n; i++ {
			if _, err := ggufValue(r, et, depth+1); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case 10:
		var v uint64
		err := read(&v)
		return v, err
	case 11:
		var v int64
		err := read(&v)
		return v, err
	case 12:
		var v float64
		err := read(&v)
		return nil, err
	}
	return nil, fmt.Errorf("unknown value type %d", typ)
}

func ggufFixedSize(t uint32) int {
	switch t {
	case 0, 1, 7:
		return 1
	case 2, 3:
		return 2
	case 4, 5, 6:
		return 4
	case 10, 11, 12:
		return 8
	}
	return 0
}

// ─── the fit plan ───────────────────────────────────────────────────────

// aiFit is the plan for one engine: what it will hold and where.
type aiFit struct {
	accelerator    string
	weightsBytes   int64
	kvCacheBytes   int64
	availableBytes int64
	gpuLayers      int
	contextTokens  int
	expectedTps    int // 0 when there is no estimate
}

// aiDefaultContext is the context a local slot asks for when nothing sets one;
// the plan halves it until it fits, down to aiMinContext.
const (
	aiDefaultContext = 8192
	aiMinContext     = 1024
)

// aiComputeOverhead is the fixed allowance for the engine's compute buffers
// and runtime, on top of weights and KV cache.
const aiComputeOverhead = 256 << 20

// aiKVBytesPerToken is the f16 KV cache cost of one context token.
func aiKVBytesPerToken(m ggufMeta) int64 {
	kl, vl := m.keyLength, m.valueLength
	if kl == 0 && m.headCount > 0 {
		kl = m.embedding / m.headCount
	}
	if vl == 0 {
		vl = kl
	}
	return int64(m.blockCount) * int64(m.headCountKV) * int64(kl+vl) * 2
}

// aiPlanFit chooses a context and GPU layers that fit avail bytes, or refuses.
//
// ctx is the requested context (0: the default, reducible to fit; an explicit
// value is honoured or refused, never silently cut). gpu is "auto", "cpu",
// "all" or a layer count. accel is the accelerator the engine drives.
func aiPlanFit(m ggufMeta, weights int64, ctx int, gpu, accel string, avail int64) (aiFit, error) {
	fit := aiFit{accelerator: accel, weightsBytes: weights, availableBytes: avail}
	explicit := ctx > 0
	if !explicit {
		ctx = aiDefaultContext
	}
	if m.contextLength > 0 && ctx > m.contextLength {
		if explicit {
			return fit, aiFailf(aiInvalidSetting, "context %d exceeds the model's trained context %d", ctx, m.contextLength)
		}
		ctx = m.contextLength
	}
	switch gpu {
	case "", "auto":
		if accel == "cpu" {
			fit.gpuLayers = 0
		} else {
			fit.gpuLayers = m.blockCount + 1
		}
	case "cpu":
		fit.gpuLayers = 0
	case "all":
		if accel == "cpu" {
			return fit, aiFailf(aiNoAccelerator, "GPU layers were asked for (all), and this engine has no accelerator (it is a CPU build); use the CPU or provision a GPU build")
		}
		fit.gpuLayers = m.blockCount + 1
	default:
		n, err := strconv.Atoi(gpu)
		if err != nil || n < 0 {
			return fit, aiFailf(aiInvalidSetting, "GPU setting %q: use auto, cpu, all or a layer count", gpu)
		}
		if n > 0 && accel == "cpu" {
			return fit, aiFailf(aiNoAccelerator, "%d GPU layers were asked for, and this engine has no accelerator (it is a CPU build)", n)
		}
		fit.gpuLayers = n
	}
	perTok := aiKVBytesPerToken(m)
	need := func(c int) int64 { return weights + perTok*int64(c) + aiComputeOverhead + weights/20 }
	for need(ctx) > avail {
		if explicit || ctx/2 < aiMinContext {
			return fit, aiFailf(aiModelTooLarge,
				"the model needs %s (weights %s, KV cache %s for %d tokens, overhead %s) and %s is available; use a smaller model or quantisation, or a smaller context",
				memUnit(uint64(need(ctx))), memUnit(uint64(weights)), memUnit(uint64(perTok*int64(ctx))), ctx,
				memUnit(uint64(aiComputeOverhead+weights/20)), memUnit(uint64(avail)))
		}
		ctx /= 2
	}
	fit.contextTokens = ctx
	fit.kvCacheBytes = perTok * int64(ctx)
	// Decode speed is bounded by memory bandwidth over the bytes read per
	// token (the weights). The bandwidth figures are deliberately low
	// estimates for each class of machine, labelled as an estimate.
	bw := int64(20e9)
	if accel != "cpu" && fit.gpuLayers > 0 {
		bw = 100e9
	}
	if weights > 0 {
		fit.expectedTps = int(bw / weights)
	}
	return fit, nil
}

// aiAccelerator is the accelerator an engine build drives:
// `<PREFIX>_AI_ACCEL` when the operator forces one, else the build's variant.
func aiAccelerator(variant string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(skyGetenv("AI_ACCEL"))); v {
	case "":
	case "cpu", "metal", "cuda", "rocm", "vulkan":
		return v, nil
	default:
		return "", aiFailf(aiInvalidSetting, "%s=%q: use cpu, metal, cuda, rocm or vulkan", skyEnvName("AI_ACCEL"), v)
	}
	switch variant {
	case "metal", "cpu":
		return variant, nil
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return "metal", nil
	}
	return "cpu", nil
}

// aiHostAvailableBytes is the memory an engine may use on this host. A container's
// cgroup limit comes first (a container is not sized to its host). On Apple
// silicon the GPU shares memory with the system and Metal works within about
// three quarters of it. On Linux it is what the kernel reports available now.
func aiHostAvailableBytes() int64 {
	total := int64(detectRAMBytes())
	if limit := int64(cgroupMemoryLimitFrom(os.ReadFile)); limit > 0 {
		return limit * 9 / 10
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		if n := parseMemAvailable(string(b)); n > 0 {
			return n
		}
	}
	return total * 3 / 4
}

// parseMemAvailable reads `MemAvailable:  8123456 kB` out of /proc/meminfo.
func parseMemAvailable(meminfo string) int64 {
	for _, line := range strings.Split(meminfo, "\n") {
		rest, ok := strings.CutPrefix(line, "MemAvailable:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return n * 1024
	}
	return 0
}
