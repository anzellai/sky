//go:build !js

package rt

// ai_draft.go — a local engine's draft model (speculative decoding) and its
// named LoRA adapters.
//
// Draft. Local.withDraft gives the engine a second, smaller model of the same
// family. llama-server drafts tokens with it and the base model checks them,
// so the answer is the base model's answer, produced faster when the draft
// guesses well. The draft must share the base model's vocabulary. The pinned
// llama-server does NOT refuse a draft whose vocabulary differs: it logs
// "the target and draft vocabs are not compatible" and serves without
// speculation, a silent slow path nobody asked for. So the vocabularies are
// compared here before launch (the same rules llama.cpp applies), and the
// engine's log is checked after it is healthy; either finding is
// InvalidSetting naming the draft.
//
// Adapters. Local.withAdapters loads several LoRA adapters on one engine
// (`--lora a,b,…`). Every request then carries llama-server's per-request
// `lora` list with an explicit scale for EVERY loaded adapter: the named one
// at its scale, the others at 0, or all at 0 for the base model. The pinned
// build applies a loaded adapter at its launch scale to a request that names
// none, and ignores an id it does not know, so leaving the list out, or
// sending only the chosen adapter, would answer with the wrong weights.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// aiVocab is the part of a GGUF tokenizer the draft check compares.
type aiVocab struct {
	model          string // tokenizer.ggml.model
	tokens         []string
	bos, eos       int64 // -1 when the file names none
	addBos, addEos int64 // -1 when the file does not say
}

// aiSpecVocabMaxDiff and aiSpecVocabCheckFrom are llama.cpp's
// SPEC_VOCAB_MAX_SIZE_DIFFERENCE and SPEC_VOCAB_CHECK_START_TOKEN_ID.
const (
	aiSpecVocabMaxDiff   = 128
	aiSpecVocabCheckFrom = 5
)

// aiReadVocab reads the tokenizer of the GGUF file at path. It reads the
// header only, bounded like readGGUFMeta.
func aiReadVocab(path string) (aiVocab, error) {
	f, err := os.Open(path)
	if err != nil {
		return aiVocab{}, err
	}
	defer f.Close()
	return aiParseVocab(bufio.NewReaderSize(f, 1<<16))
}

func aiParseVocab(r io.Reader) (aiVocab, error) {
	v := aiVocab{bos: -1, eos: -1, addBos: -1, addEos: -1}
	var hdr struct {
		Magic   [4]byte
		Version uint32
	}
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil || string(hdr.Magic[:]) != "GGUF" {
		return v, fmt.Errorf("not a GGUF file")
	}
	if hdr.Version < 2 || hdr.Version > 3 {
		return v, fmt.Errorf("GGUF version %d is not supported (2 or 3)", hdr.Version)
	}
	var counts struct{ Tensors, KV uint64 }
	if err := binary.Read(r, binary.LittleEndian, &counts); err != nil {
		return v, fmt.Errorf("truncated GGUF header: %v", err)
	}
	if counts.KV > ggufMaxKV {
		return v, fmt.Errorf("GGUF declares %d metadata entries (limit %d)", counts.KV, ggufMaxKV)
	}
	num := func(x any) int64 {
		switch n := x.(type) {
		case uint64:
			return int64(n)
		case int64:
			return n
		}
		return -1
	}
	for i := uint64(0); i < counts.KV; i++ {
		key, err := ggufString(r)
		if err != nil {
			return v, fmt.Errorf("GGUF metadata entry %d: %v", i, err)
		}
		var typ uint32
		if err := binary.Read(r, binary.LittleEndian, &typ); err != nil {
			return v, fmt.Errorf("GGUF metadata %q: %v", key, err)
		}
		if key == "tokenizer.ggml.tokens" && typ == 9 {
			var et uint32
			var n uint64
			if err := binary.Read(r, binary.LittleEndian, &et); err != nil {
				return v, err
			}
			if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
				return v, err
			}
			if et != 8 || n > ggufMaxArray {
				return v, fmt.Errorf("GGUF tokens are not a list of strings within the limit")
			}
			v.tokens = make([]string, 0, n)
			for j := uint64(0); j < n; j++ {
				s, err := ggufString(r)
				if err != nil {
					return v, fmt.Errorf("GGUF token %d: %v", j, err)
				}
				v.tokens = append(v.tokens, s)
			}
			continue
		}
		val, err := ggufValue(r, typ, 0)
		if err != nil {
			return v, fmt.Errorf("GGUF metadata %q: %v", key, err)
		}
		switch key {
		case "tokenizer.ggml.model":
			v.model, _ = val.(string)
		case "tokenizer.ggml.bos_token_id":
			v.bos = num(val)
		case "tokenizer.ggml.eos_token_id":
			v.eos = num(val)
		case "tokenizer.ggml.add_bos_token":
			v.addBos = num(val)
		case "tokenizer.ggml.add_eos_token":
			v.addEos = num(val)
		}
	}
	return v, nil
}

// aiVocabMismatch says why a draft's vocabulary cannot serve a base model,
// or "" when it can. The rules are llama.cpp's
// (common_speculative_are_compatible): the same tokenizer type, the same
// BOS/EOS handling, sizes within 128 tokens, and the same token text from id
// 5 on.
func aiVocabMismatch(base, draft aiVocab) string {
	switch {
	case base.model != draft.model:
		return fmt.Sprintf("the tokenizer types differ (%q and %q)", base.model, draft.model)
	case base.addBos != draft.addBos || base.addEos != draft.addEos || base.bos != draft.bos || base.eos != draft.eos:
		return "the beginning and end of text tokens differ"
	}
	diff := len(base.tokens) - len(draft.tokens)
	if diff < 0 {
		diff = -diff
	}
	if diff > aiSpecVocabMaxDiff {
		return fmt.Sprintf("the vocabularies differ in size by %d tokens (%d and %d; at most %d)",
			diff, len(base.tokens), len(draft.tokens), aiSpecVocabMaxDiff)
	}
	n := min(len(base.tokens), len(draft.tokens))
	for i := aiSpecVocabCheckFrom; i < n; i++ {
		if base.tokens[i] != draft.tokens[i] {
			return fmt.Sprintf("token %d differs", i)
		}
	}
	return ""
}

// aiDraftSetting names where a draft came from, for a failure.
func aiDraftSetting(slot string) string {
	if slot == "" {
		return "Local.withDraft"
	}
	return skyEnvName(aiSlotSuffix(slot, "DRAFT")) + " (or Local.withDraft in code)"
}

// aiDraftVocabFailure is the failure for a draft that cannot serve the base.
func aiDraftVocabFailure(slot string, base, draft aiModelRef, why string) *aiFailure {
	where := aiDraftSetting(slot)
	return aiFailf(aiInvalidSetting,
		"the draft model %s cannot serve the base model %s: %s. A draft must be a smaller model of the same family, with the same vocabulary; set %s to one",
		draft.String(), base.String(), why, where).
		with("setting", where).with("reason", "the draft model's vocabulary does not match the base model's: "+why)
}

// aiCheckDraftVocab compares the base and draft files before launch.
func aiCheckDraftVocab(slot string, base aiModelRef, basePath string, draft aiModelRef, draftPath string) error {
	bv, err := aiReadVocab(basePath)
	if err != nil {
		return aiFailf(aiInvalidSetting, "model %s: %v", base.String(), err).with("setting", "model").with("reason", err.Error())
	}
	dv, err := aiReadVocab(draftPath)
	if err != nil {
		where := aiDraftSetting(slot)
		return aiFailf(aiInvalidSetting, "the draft model %s: %v", draft.String(), err).with("setting", where).with("reason", err.Error())
	}
	if why := aiVocabMismatch(bv, dv); why != "" {
		return aiDraftVocabFailure(slot, base, draft, why)
	}
	return nil
}

// aiDraftLogMarks are the lines the pinned llama-server logs when it loaded
// a draft and could not use it (it then serves on without speculation).
var aiDraftLogMarks = []string{
	"failed to initialize speculative decoding context",
	"the target and draft vocabs are not compatible",
	"draft model vocab type must match target model",
}

// aiDraftRefusedInLog reports the draft failure line in an engine's log, or
// "" when there is none.
func aiDraftRefusedInLog(log string) string {
	for _, line := range strings.Split(log, "\n") {
		for _, m := range aiDraftLogMarks {
			if strings.Contains(line, m) {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}

// aiLoraEntry is one entry of llama-server's per-request `lora` list.
type aiLoraEntry struct {
	ID    int     `json:"id"`
	Scale float64 `json:"scale"`
}

// aiAdapterNames lists the loaded adapters' names, for a failure.
func aiAdapterNames(as []aiNamedAdapter) string {
	names := make([]string, len(as))
	for i, a := range as {
		names[i] = a.name
	}
	return strings.Join(names, ", ")
}

// aiRouteAdapter rewrites a request for an engine with named adapters: the
// request's `model` names the adapter (Provider.Options.adapter), and the
// body gets llama-server's `lora` list with a scale for every loaded adapter.
// A request whose model is "", "local" or the base model's reference runs the
// base model (every scale 0). A name that is not loaded is NotRegistered
// listing the loaded names: it never answers from the base model. A request
// that already carries its own `lora` list is refused (InvalidSetting): the
// adapter is chosen by name. An engine without named adapters gets the body
// unchanged.
func aiRouteAdapter(spec aiLocalSpec, body string) (string, error) {
	if len(spec.adapters) == 0 {
		return body, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return "", aiFailf(aiInvalidSetting, "the request to the local engine is not a JSON object: %v", err).
			with("setting", "request").with("reason", "the request is not a JSON object")
	}
	if _, has := m["lora"]; has {
		return "", aiFailf(aiInvalidSetting,
			"the request sets its own lora list; on an engine with Local.withAdapters a call chooses its adapter by name (Provider.Options adapter)").
			with("setting", "lora").with("reason", "choose the adapter by name")
	}
	name := ""
	if raw, ok := m["model"]; ok {
		_ = json.Unmarshal(raw, &name)
	}
	name = strings.TrimSpace(name)
	chosen := -1
	switch {
	case name == "" || strings.EqualFold(name, "local") || name == spec.model.String():
	default:
		for i, a := range spec.adapters {
			if a.name == name {
				chosen = i
			}
		}
		if chosen < 0 {
			return "", aiFailf(aiNotRegistered,
				"the local engine has no adapter %q; it loaded %s (Local.withAdapters). Name one of those, or none for the base model",
				name, aiAdapterNames(spec.adapters)).
				with("ref", name)
		}
	}
	lora := make([]aiLoraEntry, len(spec.adapters))
	for i, a := range spec.adapters {
		lora[i] = aiLoraEntry{ID: i}
		if i == chosen {
			lora[i].Scale = a.scale
		}
	}
	lb, _ := json.Marshal(lora)
	m["lora"] = lb
	out, err := json.Marshal(m)
	if err != nil {
		return "", aiFailf(aiEngineFailed, "cannot encode the engine request: %v", err).with("reason", err.Error())
	}
	return string(out), nil
}

// aiDraftAndAdapterArgs are the llama-server launch arguments for a draft
// model and named adapters. Flag names are the pinned build's (b11200):
// `--spec-type draft-simple` turns speculative decoding on (a draft given
// with `-md` alone is loaded and never used), `--spec-draft-n-max` /
// `--spec-draft-n-min` bound the drafted tokens, and `--lora` takes the
// adapters as one comma-separated list (a repeated `--lora` keeps only the
// last).
func aiDraftAndAdapterArgs(spec aiLocalSpec, draftPath string, adapterPaths []string) []string {
	var args []string
	if draftPath != "" {
		args = append(args, "-md", draftPath, "--spec-type", "draft-simple")
		if spec.draftMax > 0 {
			args = append(args, "--spec-draft-n-max", fmt.Sprint(spec.draftMax))
		}
		if spec.draftMin > 0 {
			args = append(args, "--spec-draft-n-min", fmt.Sprint(spec.draftMin))
		}
	}
	if len(adapterPaths) > 0 {
		args = append(args, "--lora", strings.Join(adapterPaths, ","), "--lora-init-without-apply")
	}
	return args
}
