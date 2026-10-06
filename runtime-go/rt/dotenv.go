// dotenv.go — auto-load `.env` on program start.
//
// Every Sky binary imports `rt`, so this init() runs before main(). The loader
// is conservative:
//   - only reads `.env` in the current working directory (no recursive search)
//   - never overrides an already-set env var (precedence: shell > .env)
//   - silently no-ops if `.env` doesn't exist
//   - tolerant parser (KEY=VALUE, strips matching quote pairs, ignores blank
//     lines and `#` comments)
//
// Surface: Process_loadEnv(path) — explicit API for reloading a specific file.
package rt

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"sky-app/rt/procenv"
)

// `debugStack` moved to panic_log.go, which owns the dev/production
// policy for what a stack trace is allowed to say. It lived here, next
// to the .env parser, with no policy attached — which is how eight sites
// came to dump full goroutine stacks into production logs.

// SetPortDefault is called by generated main.go at init time with the
// sky.toml `port` value. It only seeds <PREFIX>_LIVE_PORT when unset
// — shell env and .env still win. The prefix defaults to "SKY"; see
// env_prefix.go for the namespacing rules.
func SetPortDefault(port string) {
	SetSkyDefault("LIVE_PORT", port)
}

// The program's own defaults live in the procenv table, NEVER in the process
// environment (v0.27.7).
//
// An operator-set value and a seeded one used to share os.Environ(), and that
// ambiguity was a live defect twice over. Inside the process, generated init()
// always seeds <PREFIX>_LIVE_PORT from sky.toml, so a consumer that treated
// "env is set" as "the operator chose this" let a compiler-injected default
// beat an explicit `Live.withPort` (closed by recording the seeding). Across
// processes nothing could record it: every child inherited the seeded
// `SKY_LIVE_PORT=8000` and read it as the OPERATOR's choice, so a Sky program
// spawned by another Sky program ignored its own sky.toml and builder port.
//
// So a seed is written into procenv (rt/procenv), which children cannot see,
// and every in-process read goes through procenv.Lookup: the operator's value
// when the environment has one, else the program's own. The three-way
// precedence — operator env > explicit builder call > seeded default — is read
// off procenv.SourceOf instead of being guessed.

// SetEnvDefault: record a default for an environment variable name, only when
// neither the operator nor an earlier default set it. Generated init()
// functions call this for each sky.toml-derived default (session store, TTL,
// static dir, etc.), so shell + .env always take precedence. The value is the
// program's own: it is visible to in-process reads and to no child process.
func SetEnvDefault(name, value string) {
	procenv.SetDefault(name, value, procenv.Seeded)
}

// isSeededDefault reports whether name's current value is a default seeded by
// SetEnvDefault rather than set by the operator or a `withX` builder.
func isSeededDefault(name string) bool {
	src, ok := procenv.SourceOf(name)
	return ok && src == procenv.Seeded
}

// clearSeededDefault drops a recorded seeding. Used by tests.
func clearSeededDefault(name string) {
	procenv.Clear(name, procenv.Seeded)
}

// lookupEnvRaw is the in-process read of an env name: the operator's value,
// else the program's own (procenv). setEnvRaw / unsetEnvRaw write the PROCESS
// environment, which is the operator layer by definition, and drop any value
// the program set for itself under the same name.
func lookupEnvRaw(name string) (string, bool) { return procenv.Lookup(name) }

func setEnvRaw(name, value string) {
	procenv.Delete(name)
	_ = os.Setenv(name, value)
}

func unsetEnvRaw(name string) {
	procenv.Delete(name)
	_ = os.Unsetenv(name)
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
		// A `.env` value is operator-chosen, not a compiler-seeded default —
		// it sits above sky.toml in the documented precedence.
		setEnvRaw(key, val)
	}
	return sc.Err()
}

// stripDotEnvValue normalises a raw `KEY=…` RHS into its actual value,
// matching godotenv / python-dotenv / Foreman semantics:
//   - unquoted `value` — trim surrounding whitespace
//   - unquoted `value  # comment` — strip the trailing comment when
//     `#` is preceded by whitespace (so `tag#1` stays intact and only
//     `tag #1` becomes `tag`)
//   - quoted `"value"` / `'value'` — strip the matching outer quotes and
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
