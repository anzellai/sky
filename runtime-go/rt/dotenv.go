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
// sky.toml `port` value. It only seeds SKY_LIVE_PORT when unset — shell
// env and .env still win.
func SetPortDefault(port string) {
	SetEnvDefault("SKY_LIVE_PORT", port)
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

// Process_loadEnv: explicit loader. Returns Ok(()) on success, Err(msg) on I/O
// failure. `override = false` by default (matches godotenv semantics).
func Process_loadEnv(path any) any {
	p := ""
	if path != nil {
		p = fmt.Sprintf("%v", path)
	}
	if p == "" {
		p = ".env"
	}
	if err := loadDotEnvFile(p, false); err != nil {
		return Err[any, any](err.Error())
	}
	return Ok[any, any](nil)
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
		val := strings.TrimSpace(line[eq+1:])
		val = stripMatchingQuotes(val)
		if _, set := os.LookupEnv(key); set && !override {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return sc.Err()
}

func stripMatchingQuotes(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
