// dotenv.go — auto-load `.env` on program start.
//
// Every Sky binary imports `rt`, so this init() runs before main(). The loader
// is conservative:
//   * only reads `.env` in the current working directory (no recursive search)
//   * never overrides an already-set env var (precedence: shell > .env)
//   * silently no-ops if `.env` doesn't exist
//   * tolerant parser (KEY=VALUE, strips matching quote pairs, ignores blank
//     lines and `#` comments)
//
// Surface: Process_loadEnv(path) — explicit API for reloading a specific file.
package rt

import (
	"bufio"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

// debugStack returns a stack trace for panic logging elsewhere in rt.
func debugStack() string { return string(debug.Stack()) }

// SetPortDefault is called by generated main.go at init time with the
// sky.toml `port` value. It only seeds <PREFIX>_LIVE_PORT when unset
// — shell env and .env still win. The prefix defaults to "SKY"; see
// env_prefix.go for the namespacing rules.
func SetPortDefault(port string) {
	SetSkyDefault("LIVE_PORT", port)
}

// SetEnvDefault: set an environment variable only when it isn't already
// set. Generated init() functions call this for each sky.toml-derived
// default (session store, TTL, static dir, etc.), so shell + .env always
// take precedence.
func SetEnvDefault(name, value string) {
	if _, ok := os.LookupEnv(name); ok {
		return
	}
	_ = os.Setenv(name, value)
}

func init() {
	// Best-effort load of .env; failures are silent.
	_ = loadDotEnvFile(".env", false)
}

// Process_loadEnv: explicit loader. Task-shaped per the
// Task-everywhere doctrine — file I/O thunked so it defers to
// Cmd.perform / Task.run. Returns Ok(()) on success, Err on I/O
// failure. `override = false` by default (matches godotenv semantics).
func Process_loadEnv(path any) any {
	captured := path
	return func() any {
		// Audit P3-4: path must be a String. Non-string input is a
		// caller bug, not a display value — return typed Err rather
		// than %v-stringifying a Maybe/Dict/Int into a filename.
		p := ""
		if captured != nil {
			s, ok := captured.(string)
			if !ok {
				return Err[any, any](ErrInvalidInput(
					fmt.Sprintf("loadEnv: path must be a String, got %T", captured)))
			}
			p = s
		}
		if p == "" {
			p = ".env"
		}
		if err := loadDotEnvFile(p, false); err != nil {
			return Err[any, any](ErrFfi(err.Error()))
		}
		return Ok[any, any](nil)
	}
}

func loadDotEnvFile(path string, override bool) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := stripDotEnvValue(line[eq+1:])
		if _, set := os.LookupEnv(key); set && !override {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return sc.Err()
}

// stripDotEnvValue normalises a raw `KEY=…` RHS into its actual value,
// matching godotenv / python-dotenv / Foreman semantics:
//   * unquoted `value` — trim surrounding whitespace
//   * unquoted `value  # comment` — strip the trailing comment when
//     `#` is preceded by whitespace (so `tag#1` stays intact and only
//     `tag #1` becomes `tag`)
//   * quoted `"value"` / `'value'` — strip the matching outer quotes and
//     preserve the inner content verbatim (a `#` inside quotes is part
//     of the value)
//
// This is the canonical .env contract every other ecosystem honours; Sky
// previously kept the trailing `# comment` as part of the value, which
// silently broke any deploy whose `.env` had a trailing comment on a
// load-bearing setting (real-world hit on sky-lang.org's OAuth callback,
// 2026-06-02).
func stripDotEnvValue(raw string) string {
	// Drop only leading whitespace first so the quote check sees the
	// real opening character. Trailing whitespace handled per-branch.
	s := strings.TrimLeft(raw, " \t")
	if s == "" {
		return ""
	}
	if first := s[0]; first == '"' || first == '\'' {
		// Quoted value — content runs to the matching closing quote;
		// anything after it is treated as comment / ignored.
		if end := strings.IndexByte(s[1:], first); end >= 0 {
			return s[1 : 1+end]
		}
		// Unterminated quote — fall through and treat the whole thing
		// as an unquoted value (the closing quote, if any, sits in
		// trailing space and would be lost regardless).
	}
	// Leading `#` on an unquoted value means the whole RHS is a
	// comment and the value is empty (matches godotenv / python-dotenv:
	// `KEY=# stuff` → "" and `KEY= # stuff` → "" after the TrimLeft above).
	if s[0] == '#' {
		return ""
	}
	// Unquoted: strip a trailing ` # …` (or `\t#…`) comment. A `#`
	// without preceding whitespace is part of the value (hash tags,
	// fragment identifiers, comment markers inside URLs).
	if i := indexInlineCommentStart(s); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t")
}

// indexInlineCommentStart returns the byte index of the inline comment
// `#` (preceded by ASCII whitespace) in an unquoted value, or -1 if
// the value carries no inline comment.
func indexInlineCommentStart(s string) int {
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			return i
		}
	}
	return -1
}
