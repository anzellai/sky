//go:build !js

package rt

// ai_tokenizer.go — the native trainer's data side (Std.Ai.Train, the
// `tokenize` durable step). Go does all the parsing; the C trainer
// (scripts/ai/trainer/sky_trainer.c) reads only fixed binary files:
//
//   - data/train.tok and data/valid.tok: each row's token ids and the length
//     of its prompt. The loss counts the tokens after the prompt: position t
//     predicts token t+1, so a row of length n with prompt p trains on
//     t in [max(0, p-1), n-1).
//   - data/order.bin: the whole schedule, the row indices of every optimiser
//     step. A run resumed at step s reads the batches the first run would
//     have read, because the schedule is written once from (seed, rows,
//     rows per step) and never recomputed.
//
// The tokenizer is Qwen2's byte-level BPE, read from the base snapshot's
// tokenizer.json: added tokens split first, NFC, the Qwen2 pre-tokenizer
// (written by hand: Go's regexp has no `(?!\S)`), the GPT-2 byte mapping,
// then the ranked merges. The chat format is ChatML, rendered here the way
// the Qwen2.5 template renders a conversation without tools, including its
// default system prompt when the conversation has none. A template this
// renderer does not reproduce is an InvalidSetting, never a silent guess.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// ─── the tokenizer ──────────────────────────────────────────────────────

type aiTokenizer struct {
	vocab   map[string]int
	ranks   map[string]int // "left right" -> merge rank
	added   []aiAddedToken // longest content first
	byteRun [256]rune
	nfc     bool
	maxID   int
	cache   map[string][]int
}

type aiAddedToken struct {
	content string
	id      int
}

// aiTokenizerJSON is the part of a Hugging Face tokenizer.json the native
// tokenizer reads.
type aiTokenizerJSON struct {
	Normalizer *struct {
		Type string `json:"type"`
	} `json:"normalizer"`
	PreTokenizer *struct {
		Type          string            `json:"type"`
		Pretokenizers []json.RawMessage `json:"pretokenizers"`
	} `json:"pre_tokenizer"`
	AddedTokens []struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
	} `json:"added_tokens"`
	Model struct {
		Type   string            `json:"type"`
		Vocab  map[string]int    `json:"vocab"`
		Merges []json.RawMessage `json:"merges"`
	} `json:"model"`
}

func aiTokenizerInvalid(dir, reason string) *aiFailure {
	return aiFailf(aiInvalidSetting, "the base %s cannot be tokenized natively: %s", dir, reason).
		with("setting", "base").with("reason", reason)
}

// aiLoadTokenizer reads <dir>/tokenizer.json.
func aiLoadTokenizer(dir string) (*aiTokenizer, error) {
	b, err := os.ReadFile(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		return nil, aiTokenizerInvalid(dir, "it has no readable tokenizer.json")
	}
	var tj aiTokenizerJSON
	if err := json.Unmarshal(b, &tj); err != nil {
		return nil, aiTokenizerInvalid(dir, "its tokenizer.json does not parse: "+err.Error())
	}
	if tj.Model.Type != "BPE" {
		return nil, aiTokenizerInvalid(dir, fmt.Sprintf("its tokenizer model is %q, and only byte-level BPE is supported", tj.Model.Type))
	}
	if tj.PreTokenizer == nil || !strings.Contains(aiCompactJSON(b, "pre_tokenizer"), "ByteLevel") {
		return nil, aiTokenizerInvalid(dir, "its pre-tokenizer is not byte-level")
	}
	t := &aiTokenizer{
		vocab:   tj.Model.Vocab,
		ranks:   make(map[string]int, len(tj.Model.Merges)),
		byteRun: tinyByteRunes(),
		cache:   map[string][]int{},
	}
	if tj.Normalizer != nil {
		switch tj.Normalizer.Type {
		case "NFC":
			t.nfc = true
		default:
			return nil, aiTokenizerInvalid(dir, fmt.Sprintf("its normalizer is %q, and only NFC or none is supported", tj.Normalizer.Type))
		}
	}
	for i, m := range tj.Model.Merges {
		var pair string
		var s string
		var arr []string
		if json.Unmarshal(m, &s) == nil {
			pair = s
		} else if json.Unmarshal(m, &arr) == nil && len(arr) == 2 {
			pair = arr[0] + " " + arr[1]
		} else {
			return nil, aiTokenizerInvalid(dir, fmt.Sprintf("merge %d is neither \"a b\" nor [a, b]", i))
		}
		if _, dup := t.ranks[pair]; !dup {
			t.ranks[pair] = i
		}
	}
	for _, id := range t.vocab {
		if id > t.maxID {
			t.maxID = id
		}
	}
	for _, a := range tj.AddedTokens {
		if a.Content == "" {
			continue
		}
		t.added = append(t.added, aiAddedToken{a.Content, a.ID})
		if a.ID > t.maxID {
			t.maxID = a.ID
		}
	}
	sort.SliceStable(t.added, func(i, j int) bool { return len(t.added[i].content) > len(t.added[j].content) })
	for b := 0; b < 256; b++ {
		if _, ok := t.vocab[string(t.byteRun[b])]; !ok {
			return nil, aiTokenizerInvalid(dir, fmt.Sprintf("its vocabulary has no token for byte %d", b))
		}
	}
	return t, nil
}

// aiCompactJSON returns the raw JSON of one top-level key, for a substring
// test that does not depend on the key's nesting.
func aiCompactJSON(doc []byte, key string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(doc, &m) != nil {
		return ""
	}
	return string(m[key])
}

// VocabSize is the largest token id plus one.
func (t *aiTokenizer) VocabSize() int { return t.maxID + 1 }

// ID is the id of an added (special) token.
func (t *aiTokenizer) ID(content string) (int, bool) {
	for _, a := range t.added {
		if a.content == content {
			return a.id, true
		}
	}
	return 0, false
}

// Encode tokenizes text: added tokens are matched first, longest first,
// and the text between them goes through NFC, the pre-tokenizer and BPE.
func (t *aiTokenizer) Encode(text string) ([]int, error) {
	var out []int
	start := 0
	for i := 0; i < len(text); {
		matched := false
		if len(t.added) > 0 {
			for _, a := range t.added {
				if strings.HasPrefix(text[i:], a.content) {
					seg, err := t.encodeText(text[start:i])
					if err != nil {
						return nil, err
					}
					out = append(out, seg...)
					out = append(out, a.id)
					i += len(a.content)
					start = i
					matched = true
					break
				}
			}
		}
		if !matched {
			_, w := utf8.DecodeRuneInString(text[i:])
			i += w
		}
	}
	seg, err := t.encodeText(text[start:])
	if err != nil {
		return nil, err
	}
	return append(out, seg...), nil
}

func (t *aiTokenizer) encodeText(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	if t.nfc {
		s = norm.NFC.String(s)
	}
	var out []int
	for _, chunk := range aiQwenPretokenize(s) {
		ids, err := t.bpe(chunk)
		if err != nil {
			return nil, err
		}
		out = append(out, ids...)
	}
	return out, nil
}

// bpe encodes one pre-tokenized chunk: its bytes as GPT-2 byte runes, then
// the lowest-ranked adjacent pair merged until no pair has a rank.
func (t *aiTokenizer) bpe(chunk string) ([]int, error) {
	if ids, ok := t.cache[chunk]; ok {
		return ids, nil
	}
	syms := make([]string, 0, len(chunk))
	for i := 0; i < len(chunk); i++ {
		syms = append(syms, string(t.byteRun[chunk[i]]))
	}
	for len(syms) > 1 {
		best, at := -1, -1
		for i := 0; i+1 < len(syms); i++ {
			if r, ok := t.ranks[syms[i]+" "+syms[i+1]]; ok && (best < 0 || r < best) {
				best, at = r, i
			}
		}
		if at < 0 {
			break
		}
		left, right := syms[at], syms[at+1]
		merged := syms[:0:0]
		for i := 0; i < len(syms); i++ {
			if i+1 < len(syms) && syms[i] == left && syms[i+1] == right {
				merged = append(merged, left+right)
				i++
			} else {
				merged = append(merged, syms[i])
			}
		}
		syms = merged
	}
	ids := make([]int, len(syms))
	for i, s := range syms {
		id, ok := t.vocab[s]
		if !ok {
			return nil, aiFailf(aiInvalidSetting, "the tokenizer has no token for the merged symbol %q", s).
				with("setting", "base").with("reason", "a merge produced a symbol outside the vocabulary")
		}
		ids[i] = id
	}
	if len(t.cache) < 1<<16 {
		t.cache[chunk] = ids
	}
	return ids, nil
}

// aiQwenPretokenize splits text the way Qwen2's pre-tokenizer regex does:
//
//	(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}
//	| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+
//
// The alternatives are tried in order at each position, as the regex does.
func aiQwenPretokenize(s string) []string {
	rs := []rune(s)
	n := len(rs)
	isL := unicode.IsLetter
	isN := unicode.IsNumber
	isS := func(r rune) bool { return unicode.Is(unicode.White_Space, r) }
	isNL := func(r rune) bool { return r == '\r' || r == '\n' }
	isP := func(r rune) bool { return !isS(r) && !isL(r) && !isN(r) }
	var out []string
	for i := 0; i < n; {
		end := -1
		// 1. contractions, case-insensitive
		if rs[i] == '\'' && i+1 < n {
			a := unicode.ToLower(rs[i+1])
			switch {
			case a == 's' || a == 't' || a == 'm' || a == 'd':
				end = i + 2
			case i+2 < n:
				b := unicode.ToLower(rs[i+2])
				if (a == 'r' && b == 'e') || (a == 'v' && b == 'e') || (a == 'l' && b == 'l') {
					end = i + 3
				}
			}
		}
		// 2. an optional non-letter, non-digit, non-newline, then letters
		if end < 0 {
			j := i
			if !isNL(rs[j]) && !isL(rs[j]) && !isN(rs[j]) && j+1 < n && isL(rs[j+1]) {
				j++
			}
			if isL(rs[j]) {
				for j < n && isL(rs[j]) {
					j++
				}
				end = j
			}
		}
		// 3. one number
		if end < 0 && isN(rs[i]) {
			end = i + 1
		}
		// 4. an optional space, punctuation, then newlines
		if end < 0 {
			j := i
			if rs[j] == ' ' && j+1 < n && isP(rs[j+1]) {
				j++
			}
			if isP(rs[j]) {
				for j < n && isP(rs[j]) {
					j++
				}
				for j < n && isNL(rs[j]) {
					j++
				}
				end = j
			}
		}
		if end < 0 {
			// whitespace: rs[i] is White_Space here
			e := i
			for e < n && isS(rs[e]) {
				e++
			}
			// 5. \s*[\r\n]+ : up to the last newline of the run
			last := -1
			for k := i; k < e; k++ {
				if isNL(rs[k]) {
					last = k
				}
			}
			switch {
			case last >= 0:
				end = last + 1
			case e == n:
				// 6. \s+(?!\S) at the end of the text takes the whole run
				end = e
			case e-1 > i:
				// 6. otherwise it leaves the last space to the next word
				end = e - 1
			default:
				// 7. \s+
				end = e
			}
		}
		out = append(out, string(rs[i:end]))
		i = end
	}
	return out
}

// ─── the chat format ────────────────────────────────────────────────────

// aiQwenDefaultSystem is the system prompt the Qwen2.5 template inserts when
// a conversation has none.
const aiQwenDefaultSystem = "You are Qwen, created by Alibaba Cloud. You are a helpful assistant."

type aiChatFormat struct {
	defaultSystem string
}

type aiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// aiLoadChatFormat reads the chat template (tokenizer_config.json's
// chat_template, a string or the "default" entry of a list, else
// chat_template.jinja) and accepts only the ChatML templates this renderer
// reproduces.
func aiLoadChatFormat(dir string) (aiChatFormat, error) {
	b, err := os.ReadFile(filepath.Join(dir, "tokenizer_config.json"))
	if err != nil {
		return aiChatFormat{}, aiTokenizerInvalid(dir, "it has no readable tokenizer_config.json")
	}
	var cfg struct {
		ChatTemplate json.RawMessage `json:"chat_template"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return aiChatFormat{}, aiTokenizerInvalid(dir, "its tokenizer_config.json does not parse: "+err.Error())
	}
	var tmpl string
	if json.Unmarshal(cfg.ChatTemplate, &tmpl) != nil {
		var named []struct{ Name, Template string }
		if json.Unmarshal(cfg.ChatTemplate, &named) == nil {
			for _, n := range named {
				if n.Name == "default" {
					tmpl = n.Template
				}
			}
		}
	}
	if tmpl == "" {
		if j, err := os.ReadFile(filepath.Join(dir, "chat_template.jinja")); err == nil {
			tmpl = string(j)
		}
	}
	if tmpl == "" {
		return aiChatFormat{}, aiTokenizerInvalid(dir, "it has no chat template")
	}
	return aiChatFormatOf(dir, tmpl)
}

func aiChatFormatOf(dir, tmpl string) (aiChatFormat, error) {
	if !strings.Contains(tmpl, "<|im_start|>") || !strings.Contains(tmpl, "<|im_end|>") {
		return aiChatFormat{}, aiTokenizerInvalid(dir, "its chat template is not ChatML, and only ChatML (the Qwen2.5 template) is supported")
	}
	if strings.Contains(tmpl, "<think>") {
		return aiChatFormat{}, aiTokenizerInvalid(dir, "its chat template renders reasoning (<think>) turns, which the native renderer does not reproduce")
	}
	f := aiChatFormat{}
	if strings.Contains(tmpl, aiQwenDefaultSystem) {
		f.defaultSystem = aiQwenDefaultSystem
	}
	return f, nil
}

// render writes the conversation in ChatML; generation adds the opening of
// an assistant turn.
func (f aiChatFormat) render(msgs []aiChatMessage, generation bool) string {
	var b strings.Builder
	if f.defaultSystem != "" && (len(msgs) == 0 || msgs[0].Role != "system") {
		b.WriteString("<|im_start|>system\n" + f.defaultSystem + "<|im_end|>\n")
	}
	for _, m := range msgs {
		b.WriteString("<|im_start|>" + m.Role + "\n" + m.Content + "<|im_end|>\n")
	}
	if generation {
		b.WriteString("<|im_start|>assistant\n")
	}
	return b.String()
}

// aiRowMessages turns one staged row (chat or instruction) into messages.
func aiRowMessages(row []byte, format string) ([]aiChatMessage, error) {
	if format == "instruction" {
		var r struct {
			Prompt     *string `json:"prompt"`
			Completion *string `json:"completion"`
		}
		if err := json.Unmarshal(row, &r); err != nil || r.Prompt == nil || r.Completion == nil {
			return nil, fmt.Errorf("an instruction row needs a prompt and a completion")
		}
		return []aiChatMessage{{"user", *r.Prompt}, {"assistant", *r.Completion}}, nil
	}
	var r struct {
		Messages []aiChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(row, &r); err != nil || len(r.Messages) == 0 {
		return nil, fmt.Errorf("a chat row needs a non-empty messages list")
	}
	for _, m := range r.Messages {
		switch m.Role {
		case "system", "user", "assistant":
		default:
			return nil, fmt.Errorf("the role %q is not one of system, user and assistant", m.Role)
		}
	}
	return r.Messages, nil
}

// aiEncodeRow is one row's token ids, cut to seqLen, and the length of its
// prompt: everything before the last assistant turn's content. A row whose
// last message is not the assistant's has no prompt, so every token counts.
func aiEncodeRow(t *aiTokenizer, f aiChatFormat, msgs []aiChatMessage, seqLen int) ([]int, int, error) {
	full, err := t.Encode(f.render(msgs, false))
	if err != nil {
		return nil, 0, err
	}
	prompt := 0
	if msgs[len(msgs)-1].Role == "assistant" {
		p, err := t.Encode(f.render(msgs[:len(msgs)-1], true))
		if err != nil {
			return nil, 0, err
		}
		prompt = len(p)
	}
	if seqLen > 0 && len(full) > seqLen {
		full = full[:seqLen]
	}
	if hi := len(full) - 1; prompt > hi {
		prompt = max(0, hi)
	}
	return full, prompt, nil
}

// ─── the binary files ───────────────────────────────────────────────────

const (
	aiTokMagic   = "SKYTOK01"
	aiOrderMagic = "SKYORD01"
)

// aiTokRows is a tokenized split.
type aiTokRows struct {
	ids     [][]int
	prompts []int
	vocab   int
}

// encode writes the .tok layout, little-endian:
//
//	"SKYTOK01" u32 rows  u32 vocab  u64 tokens
//	rows x (u32 length, u32 prompt)
//	tokens x u32 id
func (r aiTokRows) encode() []byte {
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	total := 0
	for _, ids := range r.ids {
		total += len(ids)
	}
	b.WriteString(aiTokMagic)
	le(uint32(len(r.ids)))
	le(uint32(r.vocab))
	le(uint64(total))
	for i, ids := range r.ids {
		le(uint32(len(ids)))
		le(uint32(r.prompts[i]))
	}
	for _, ids := range r.ids {
		for _, id := range ids {
			le(uint32(id))
		}
	}
	return b.Bytes()
}

func aiDecodeTokRows(data []byte) (aiTokRows, error) {
	rd := bytes.NewReader(data)
	magic := make([]byte, 8)
	if _, err := rd.Read(magic); err != nil || string(magic) != aiTokMagic {
		return aiTokRows{}, fmt.Errorf("not a %s file", aiTokMagic)
	}
	var h struct {
		Rows, Vocab uint32
		Tokens      uint64
	}
	if err := binary.Read(rd, binary.LittleEndian, &h); err != nil {
		return aiTokRows{}, err
	}
	lens := make([]uint32, 2*h.Rows)
	if err := binary.Read(rd, binary.LittleEndian, lens); err != nil {
		return aiTokRows{}, err
	}
	toks := make([]uint32, h.Tokens)
	if err := binary.Read(rd, binary.LittleEndian, toks); err != nil {
		return aiTokRows{}, err
	}
	out := aiTokRows{vocab: int(h.Vocab)}
	at := 0
	for i := 0; i < int(h.Rows); i++ {
		n := int(lens[2*i])
		ids := make([]int, n)
		for k := 0; k < n; k++ {
			ids[k] = int(toks[at+k])
		}
		at += n
		out.ids = append(out.ids, ids)
		out.prompts = append(out.prompts, int(lens[2*i+1]))
	}
	return out, nil
}

// aiSplitMix64 is the schedule's random source: fixed here, so the schedule
// never depends on a library's generator.
type aiSplitMix64 uint64

func (s *aiSplitMix64) next() uint64 {
	*s += 0x9E3779B97F4A7C15
	z := uint64(*s)
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// aiEpochOrder is the permutation of n rows for one epoch, from the seed
// and the epoch alone (Fisher-Yates over splitmix64).
func aiEpochOrder(n int, seed int64, epoch int) []int {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	src := aiSplitMix64(uint64(seed)*1000003 + uint64(epoch))
	for i := n - 1; i > 0; i-- {
		j, _ := bits.Mul64(src.next(), uint64(i+1))
		order[i], order[j] = order[j], order[i]
	}
	return order
}

// aiTrainTotalSteps is the job's optimiser steps: maxSteps when set, else
// whole epochs of rowsPerStep rows.
func aiTrainTotalSteps(maxSteps, epochs, rows, rowsPerStep int) int {
	if maxSteps > 0 {
		return maxSteps
	}
	rowsPerStep = max(1, rowsPerStep)
	return max(1, (max(1, epochs)*rows+rowsPerStep-1)/rowsPerStep)
}

// aiOrderSchedule is the row indices of every step, step after step.
func aiOrderSchedule(rows, rowsPerStep, steps int, seed int64) []int {
	if rows <= 0 {
		return nil
	}
	out := make([]int, 0, rowsPerStep*steps)
	epoch := -1
	var order []int
	for q := 0; q < rowsPerStep*steps; q++ {
		e := q / rows
		if e != epoch {
			epoch, order = e, aiEpochOrder(rows, seed, e)
		}
		out = append(out, order[q%rows])
	}
	return out
}

// aiEncodeOrder writes order.bin, little-endian:
//
//	"SKYORD01" u32 rows  u32 rowsPerStep  u32 steps  u32 0  i64 seed
//	steps x rowsPerStep x u32 row
func aiEncodeOrder(rows, rowsPerStep, steps int, seed int64) []byte {
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString(aiOrderMagic)
	le(uint32(rows))
	le(uint32(rowsPerStep))
	le(uint32(steps))
	le(uint32(0))
	le(seed)
	for _, r := range aiOrderSchedule(rows, rowsPerStep, steps, seed) {
		le(uint32(r))
	}
	return b.Bytes()
}

// ─── the step ───────────────────────────────────────────────────────────

const aiTrainTokenizeFile = "tokenize.json"

// Ai_trainTokenize : String -> String -> Task Error Tokenized
//
// runDir (already staged) and the base snapshot's directory. Tokenizes
// data/train.jsonl and data/valid.jsonl with the base's own tokenizer and
// chat format, and writes data/train.tok, data/valid.tok and data/order.bin
// from the job's sequence length, batch, epochs, maxSteps and seed.
// Tokenizing the same inputs again writes nothing.
func Ai_trainTokenize(runArg, baseArg any) any {
	runDir, base := AsString(runArg), AsString(baseArg)
	return func() any {
		out, err := aiTrainTokenize(runDir, base)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](out)
	}
}

func aiTrainTokenize(runDir, base string) (map[string]any, error) {
	job, err := os.ReadFile(filepath.Join(runDir, aiTrainJobFile))
	if err != nil {
		return nil, aiTrainFail("the run %s is not staged: %v", runDir, err)
	}
	cfg, err := aiTrainJSON(string(job))
	if err != nil {
		return nil, aiTrainFail("the run's %s does not parse: %v", aiTrainJobFile, err)
	}
	dataDir := filepath.Join(runDir, "data")
	trainPath, validPath := filepath.Join(dataDir, "train.jsonl"), filepath.Join(dataDir, "valid.jsonl")
	// The stamp: the job and every input file, so a change to any is a new
	// tokenization.
	key := aiSha256Hex(job)
	for _, p := range []string{trainPath, validPath, filepath.Join(base, "tokenizer.json"), filepath.Join(base, "tokenizer_config.json"), filepath.Join(base, "chat_template.jinja")} {
		d, err := aiFileSha256(p)
		if err != nil {
			d = "absent"
		}
		key += d
	}
	key = aiSha256Hex([]byte(key))
	stamp := filepath.Join(runDir, aiTrainTokenizeFile)
	if b, err := os.ReadFile(stamp); err == nil {
		var st struct {
			Key                string
			Train, Eval, Steps int
		}
		if json.Unmarshal(b, &st) == nil && st.Key == key {
			return map[string]any{"train": st.Train, "eval": st.Eval, "steps": st.Steps}, nil
		}
	}
	tok, err := aiLoadTokenizer(base)
	if err != nil {
		return nil, err
	}
	chat, err := aiLoadChatFormat(base)
	if err != nil {
		return nil, err
	}
	seqLen := aiJNum(cfg, "sequenceLength", 0)
	format := aiJStr(cfg, "format")
	split := func(path string) (aiTokRows, error) {
		rows := aiTokRows{vocab: tok.VocabSize()}
		f, err := os.Open(path)
		if err != nil {
			return rows, aiTrainFail("cannot read %s: %v", path, err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		line := 0
		for sc.Scan() {
			line++
			text := bytes.TrimSpace(sc.Bytes())
			if len(text) == 0 {
				continue
			}
			msgs, err := aiRowMessages(text, format)
			if err != nil {
				return rows, aiFailf(aiDatasetInvalid, "%s line %d: %v", path, line, err).
					with("dataset", path).with("reasons", fmt.Sprintf("line %d: %v", line, err))
			}
			ids, prompt, err := aiEncodeRow(tok, chat, msgs, seqLen)
			if err != nil {
				return rows, err
			}
			rows.ids = append(rows.ids, ids)
			rows.prompts = append(rows.prompts, prompt)
		}
		if err := sc.Err(); err != nil {
			return rows, aiTrainFail("cannot read %s: %v", path, err)
		}
		return rows, nil
	}
	train, err := split(trainPath)
	if err != nil {
		return nil, err
	}
	if len(train.ids) == 0 {
		return nil, aiFailf(aiDatasetInvalid, "the dataset has no train rows").
			with("dataset", trainPath).with("reasons", "no train rows")
	}
	valid, err := split(validPath)
	if err != nil {
		return nil, err
	}
	batch := aiJMap(cfg, "batch")
	rowsPerStep := max(1, aiJNum(batch, "perDevice", 1)*aiJNum(batch, "accumulate", 1))
	steps := aiTrainTotalSteps(aiJNum(cfg, "maxSteps", 0), aiJNum(cfg, "epochs", 1), len(train.ids), rowsPerStep)
	seed := int64(aiJNum(cfg, "seed", 0))
	for name, data := range map[string][]byte{
		"train.tok": train.encode(),
		"valid.tok": valid.encode(),
		"order.bin": aiEncodeOrder(len(train.ids), rowsPerStep, steps, seed),
	} {
		if err := aiWriteFileAtomic(filepath.Join(dataDir, name), data); err != nil {
			return nil, aiTrainFail("cannot write %s: %v", name, err)
		}
	}
	st, _ := json.Marshal(map[string]any{"key": key, "train": len(train.ids), "eval": len(valid.ids), "steps": steps})
	if err := aiWriteFileAtomic(stamp, st); err != nil {
		return nil, aiTrainFail("cannot write %s: %v", aiTrainTokenizeFile, err)
	}
	return map[string]any{"train": len(train.ids), "eval": len(valid.ids), "steps": steps}, nil
}
