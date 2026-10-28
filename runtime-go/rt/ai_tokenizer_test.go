//go:build !js

package rt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// tinyTokenizerDir writes a byte-level BPE tokenizer.json (one token per
// byte, the ids equal to the byte, then <|endoftext|> 256, <|im_start|> 257
// and <|im_end|> 258, then one token per merge from 259) and a
// tokenizer_config.json with the given chat template.
func tinyTokenizerDir(t *testing.T, merges [][2]string, template string) string {
	t.Helper()
	dir := t.TempDir()
	runes := tinyByteRunes()
	vocab := map[string]int{}
	for b := 0; b < 256; b++ {
		vocab[string(runes[b])] = b
	}
	var ms []string
	for i, m := range merges {
		vocab[m[0]+m[1]] = 259 + i
		ms = append(ms, m[0]+" "+m[1])
	}
	tj := map[string]any{
		"normalizer":    map[string]any{"type": "NFC"},
		"pre_tokenizer": map[string]any{"type": "Sequence", "pretokenizers": []any{map[string]any{"type": "ByteLevel"}}},
		"added_tokens": []any{
			map[string]any{"id": 256, "content": "<|endoftext|>", "special": true},
			map[string]any{"id": 257, "content": "<|im_start|>", "special": true},
			map[string]any{"id": 258, "content": "<|im_end|>", "special": true},
		},
		"model": map[string]any{"type": "BPE", "vocab": vocab, "merges": ms},
	}
	b, _ := json.Marshal(tj)
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ := json.Marshal(map[string]any{"chat_template": template})
	if err := os.WriteFile(filepath.Join(dir, "tokenizer_config.json"), c, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const tinyChatML = "{% for message in messages %}{{'<|im_start|>' + message['role'] + '\n' + message['content'] + '<|im_end|>' + '\n'}}{% endfor %}{% if add_generation_prompt %}{{ '<|im_start|>assistant\n' }}{% endif %}"

func bytesIDs(s string) []int {
	out := make([]int, len(s))
	for i := 0; i < len(s); i++ {
		out[i] = int(s[i])
	}
	return out
}

func cat(parts ...[]int) []int {
	var out []int
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestQwenPretokenizerSplitsLikeTheRegex(t *testing.T) {
	cases := map[string][]string{
		"Hello world  !\n\n  x's 123": {"Hello", " world", " ", " !\n\n", " ", " x", "'s", " ", "1", "2", "3"},
		"a\n\n b":                     {"a", "\n\n", " b"},
		"x  ":                         {"x", "  "},
		"I'LL go":                     {"I", "'LL", " go"},
		"café 42é":                    {"café", " ", "4", "2", "é"},
		"(x)+y":                       {"(x", ")+", "y"},
	}
	for in, want := range cases {
		if got := aiQwenPretokenize(in); !reflect.DeepEqual(got, want) {
			t.Errorf("pretokenize %q = %q, want %q", in, got, want)
		}
	}
}

func TestBPEMergesTheLowestRankFirst(t *testing.T) {
	dir := tinyTokenizerDir(t, [][2]string{{"h", "e"}, {"he", "l"}, {"l", "o"}}, tinyChatML)
	tok, err := aiLoadTokenizer(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tok.Encode("hello")
	if err != nil {
		t.Fatal(err)
	}
	// h e l l o -> he l l o -> hel l o -> hel lo
	if want := []int{260, 261}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hello = %v, want %v", got, want)
	}
	if tok.VocabSize() != 262 {
		t.Fatalf("vocab size %d, want 262", tok.VocabSize())
	}
}

func TestTokenizeChatMasksTheLastAssistantTurn(t *testing.T) {
	dir := tinyTokenizerDir(t, nil, tinyChatML)
	tok, err := aiLoadTokenizer(dir)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := aiLoadChatFormat(dir)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []aiChatMessage{{"user", "ab"}, {"assistant", "c"}}
	ids, prompt, err := aiEncodeRow(tok, chat, msgs, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := cat([]int{257}, bytesIDs("user\nab"), []int{258}, bytesIDs("\n"),
		[]int{257}, bytesIDs("assistant\nc"), []int{258}, bytesIDs("\n"))
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v\nwant  %v", ids, want)
	}
	// The prompt runs to the end of "<|im_start|>assistant\n": 21 tokens, so
	// the loss counts positions 20..22, which predict "c", <|im_end|> and "\n".
	if prompt != 21 {
		t.Fatalf("prompt = %d, want 21", prompt)
	}
	// An instruction row is the same conversation.
	row, _ := json.Marshal(map[string]string{"prompt": "ab", "completion": "c"})
	im, err := aiRowMessages(row, "instruction")
	if err != nil {
		t.Fatal(err)
	}
	ids2, prompt2, _ := aiEncodeRow(tok, chat, im, 0)
	if !reflect.DeepEqual(ids2, ids) || prompt2 != prompt {
		t.Fatalf("instruction row = %v/%d, want %v/%d", ids2, prompt2, ids, prompt)
	}
	// Cut to 22 tokens, the prompt stays below the last position.
	cut, p3, _ := aiEncodeRow(tok, chat, msgs, 21)
	if len(cut) != 21 || p3 != 20 {
		t.Fatalf("cut row: %d tokens, prompt %d; want 21, 20", len(cut), p3)
	}
	// A conversation that does not end with the assistant trains on all of it.
	_, p4, _ := aiEncodeRow(tok, chat, []aiChatMessage{{"user", "ab"}}, 0)
	if p4 != 0 {
		t.Fatalf("prompt without an assistant turn = %d, want 0", p4)
	}
}

func TestQwenTemplateAddsItsDefaultSystemPrompt(t *testing.T) {
	tmpl := "{%- if messages[0]['role'] == 'system' %}{{- '<|im_start|>system\\n' + messages[0]['content'] + '<|im_end|>\\n' }}{%- else %}{{- '<|im_start|>system\\n" + aiQwenDefaultSystem + "<|im_end|>\\n' }}{%- endif %}"
	f, err := aiChatFormatOf("base", tmpl)
	if err != nil {
		t.Fatal(err)
	}
	got := f.render([]aiChatMessage{{"user", "hi"}}, true)
	want := "<|im_start|>system\n" + aiQwenDefaultSystem + "<|im_end|>\n<|im_start|>user\nhi<|im_end|>\n<|im_start|>assistant\n"
	if got != want {
		t.Fatalf("render = %q\nwant     %q", got, want)
	}
	own := f.render([]aiChatMessage{{"system", "s"}, {"user", "hi"}}, false)
	if strings.Contains(own, aiQwenDefaultSystem) {
		t.Fatalf("a conversation with its own system prompt got the default: %q", own)
	}
}

func TestAnUnknownChatTemplateIsInvalidSetting(t *testing.T) {
	for _, tmpl := range []string{
		"{% for m in messages %}[INST] {{ m['content'] }} [/INST]{% endfor %}",
		"<|im_start|>{{ m }}<|im_end|><think>",
	} {
		_, err := aiChatFormatOf("base", tmpl)
		if f, ok := err.(*aiFailure); !ok || f.code != aiInvalidSetting {
			t.Fatalf("template %q: err = %v, want InvalidSetting", tmpl, err)
		}
	}
	if _, err := aiRowMessages([]byte(`{"messages":[{"role":"tool","content":"x"}]}`), "chat"); err == nil {
		t.Fatal("a tool message was accepted")
	}
}

func TestOrderScheduleIsResumeStable(t *testing.T) {
	a := aiOrderSchedule(10, 3, 8, 7)
	b := aiOrderSchedule(10, 3, 8, 7)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("the same seed gave two schedules")
	}
	if reflect.DeepEqual(a, aiOrderSchedule(10, 3, 8, 8)) {
		t.Fatal("another seed gave the same schedule")
	}
	// Every epoch is a permutation of the rows.
	seen := aiOrderSchedule(10, 2, 5, 1)
	got := map[int]int{}
	for _, r := range seen {
		got[r]++
	}
	for r := 0; r < 10; r++ {
		if got[r] != 1 {
			t.Fatalf("epoch 0 holds row %d %d times: %v", r, got[r], seen)
		}
	}
	// A longer run starts with the shorter run's schedule: steps already
	// taken never move when maxSteps grows.
	short, long := aiOrderSchedule(7, 4, 3, 3), aiOrderSchedule(7, 4, 6, 3)
	if !reflect.DeepEqual(long[:len(short)], short) {
		t.Fatalf("schedule prefix moved: %v vs %v", short, long[:len(short)])
	}
	// The epochs differ from each other.
	if reflect.DeepEqual(aiEpochOrder(20, 5, 0), aiEpochOrder(20, 5, 1)) {
		t.Fatal("two epochs have the same order")
	}
	if aiTrainTotalSteps(5, 1, 100, 4) != 5 || aiTrainTotalSteps(0, 2, 20, 8) != 5 || aiTrainTotalSteps(0, 1, 0, 4) != 1 {
		t.Fatal("total steps")
	}
	// Pinned values: a change to the generator changes every resumed run's
	// batches, so it must be deliberate.
	if want := []int{4, 3, 9, 1, 0, 7, 2, 6, 5, 8}; !reflect.DeepEqual(aiEpochOrder(10, 42, 0), want) {
		t.Fatalf("epoch order for seed 42 = %v, want %v", aiEpochOrder(10, 42, 0), want)
	}
}

func TestTokenizeWritesTheTrainerFilesOnce(t *testing.T) {
	base := tinyTokenizerDir(t, [][2]string{{"a", "b"}}, tinyChatML)
	run := t.TempDir()
	data := filepath.Join(run, "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	row := func(u, a string) string {
		b, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": u}, {"role": "assistant", "content": a}}})
		return string(b) + "\n"
	}
	train := row("ab", "c") + row("hi", "there") + row("x", "y")
	if err := os.WriteFile(filepath.Join(data, "train.jsonl"), []byte(train), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "valid.jsonl"), []byte(row("q", "r")), 0o644); err != nil {
		t.Fatal(err)
	}
	job := `{"batch":{"accumulate":1,"perDevice":2},"epochs":2,"format":"chat","maxSteps":0,"seed":3,"sequenceLength":16}`
	if err := os.WriteFile(filepath.Join(run, aiTrainJobFile), []byte(job+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := aiTrainTokenize(run, base)
	if err != nil {
		t.Fatal(err)
	}
	if out["train"] != 3 || out["eval"] != 1 || out["steps"] != 3 {
		t.Fatalf("tokenized %v, want 3 train, 1 eval, 3 steps", out)
	}
	tb, _ := os.ReadFile(filepath.Join(data, "train.tok"))
	rows, err := aiDecodeTokRows(tb)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.ids) != 3 || rows.vocab != 260 {
		t.Fatalf("train.tok: %d rows, vocab %d", len(rows.ids), rows.vocab)
	}
	for i, ids := range rows.ids {
		if len(ids) > 16 || rows.prompts[i] >= len(ids) {
			t.Fatalf("row %d: %d ids, prompt %d", i, len(ids), rows.prompts[i])
		}
	}
	// "ab" is one merged token.
	if rows.ids[0][6] != 259 {
		t.Fatalf("row 0 = %v, want the merged ab at 6", rows.ids[0])
	}
	ob, _ := os.ReadFile(filepath.Join(data, "order.bin"))
	if string(ob[:8]) != aiOrderMagic || len(ob) != 32+4*2*3 {
		t.Fatalf("order.bin is %d bytes", len(ob))
	}
	// A second call reads the stamp and rewrites nothing.
	before, _ := os.Stat(filepath.Join(data, "train.tok"))
	if _, err := aiTrainTokenize(run, base); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(data, "train.tok"))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("tokenizing the same inputs again rewrote train.tok")
	}
	// A base with no tokenizer is InvalidSetting.
	if _, err := aiTrainTokenize(t.TempDir(), base); err == nil {
		t.Fatal("an unstaged run was tokenized")
	}
	if _, err := aiTrainTokenize(run, t.TempDir()); err == nil {
		t.Fatal("a base without a tokenizer was accepted")
	} else if f, ok := err.(*aiFailure); !ok || f.code != aiInvalidSetting {
		t.Fatalf("a base without a tokenizer: %v, want InvalidSetting", err)
	}
}
