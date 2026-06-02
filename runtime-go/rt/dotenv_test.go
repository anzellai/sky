package rt

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStripDotEnvValue covers the canonical .env semantics that
// dotenv.go's loader must honour. The regression that motivated this
// suite: sky-lang.org's .env had
//
//	SKYLANG_BASE_URL=https://sky-lang.org    # used for redirect_uri
//
// which Sky previously read as the full literal "https://sky-lang.org
// (whitespace) # used for redirect_uri", silently breaking the OAuth
// redirect_uri check at GitHub. Every other dotenv parser
// (godotenv / python-dotenv / Foreman / dotenv-cli) strips the trailing
// ` # comment` from an unquoted value.
func TestStripDotEnvValue(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Plain unquoted
		{"plain", "value", "value"},
		{"leading-ws", "  value", "value"},
		{"trailing-ws", "value  ", "value"},
		{"both-ws", "  value  ", "value"},
		{"empty", "", ""},
		{"only-ws", "   ", ""},

		// Inline comments — STRIPPED when preceded by whitespace
		{"comment-after-space", "value # comment", "value"},
		{"comment-after-tab", "value\t# comment", "value"},
		{"comment-many-spaces", "value     # boom", "value"},
		{"comment-with-hash-inside", "value # has #2 inside", "value"},
		{"comment-no-space-after-hash", "value #x", "value"},

		// `#` WITHOUT preceding whitespace stays in the value
		{"hash-tag", "tag#1", "tag#1"},
		{"hash-fragment", "https://x.io/page#section", "https://x.io/page#section"},
		{"hash-many", "a#b#c", "a#b#c"},

		// Quoted values — strip outer quotes, preserve inner content
		{"double-quoted", `"value"`, "value"},
		{"single-quoted", `'value'`, "value"},
		{"quoted-with-hash", `"value # not a comment"`, "value # not a comment"},
		{"quoted-with-spaces", `"  spaced  "`, "  spaced  "},
		{"single-quoted-hash", `'has#tag'`, "has#tag"},
		{"quoted-then-junk-dropped", `"value" # trailing comment`, "value"},

		// Edge cases
		{"unterminated-double-quote", `"unterminated`, `"unterminated`},
		{"unterminated-single-quote", `'unterminated`, `'unterminated`},
		// Leading `#` (post-trim) means the whole RHS is a comment per
		// godotenv / python-dotenv conventions.
		{"only-hash", "#", ""},
		{"hash-then-space", "# x", ""},
		{"space-then-hash", " # comment", ""},
		{"tab-then-hash", "\t# comment", ""},

		// The real-world hit
		{
			"sky-lang.org-base-url",
			"https://sky-lang.org    # used to construct OAuth redirect_uri",
			"https://sky-lang.org",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stripDotEnvValue(c.in)
			if got != c.want {
				t.Fatalf("stripDotEnvValue(%q) = %q; want %q", c.in, got, c.want)
			}
		})
	}
}

// TestLoadDotEnvFile_InlineComments end-to-end-loads a fixture .env
// file and asserts the resulting os.Getenv values are comment-stripped.
// This is the customer-visible contract — TestStripDotEnvValue covers
// the helper in isolation; this covers the integration through
// bufio.Scanner + os.Setenv.
func TestLoadDotEnvFile_InlineComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := `# leading comment line
PLAIN=value
WITH_COMMENT=hello # trailing comment
WITH_TAB_COMMENT=tab-val	# tab before hash
URL_WITH_FRAGMENT=https://x.io/page#section
QUOTED="value # inside quotes"
QUOTED_THEN_COMMENT="kept" # dropped
EMPTY=
COMMENT_ONLY_LINE=actual    # comment

# blank line above
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Isolate side effects on the process env.
	keys := []string{
		"PLAIN", "WITH_COMMENT", "WITH_TAB_COMMENT",
		"URL_WITH_FRAGMENT", "QUOTED", "QUOTED_THEN_COMMENT",
		"EMPTY", "COMMENT_ONLY_LINE",
	}
	for _, k := range keys {
		_ = os.Unsetenv(k)
		t.Cleanup(func() { _ = os.Unsetenv(k) })
	}

	if err := loadDotEnvFile(path, false); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"PLAIN":               "value",
		"WITH_COMMENT":        "hello",
		"WITH_TAB_COMMENT":    "tab-val",
		"URL_WITH_FRAGMENT":   "https://x.io/page#section",
		"QUOTED":              "value # inside quotes",
		"QUOTED_THEN_COMMENT": "kept",
		"EMPTY":               "",
		"COMMENT_ONLY_LINE":   "actual",
	}
	for k, w := range want {
		got := os.Getenv(k)
		if got != w {
			t.Errorf("%s: got %q; want %q", k, got, w)
		}
	}
}

// TestLoadDotEnvFile_NoOverride confirms the 12-factor precedence:
// values already set in the process env are NEVER overwritten by
// the .env file when override=false.
func TestLoadDotEnvFile_NoOverride(t *testing.T) {
	const k = "SKY_TEST_NO_OVERRIDE_PRECEDENCE"
	const shellValue = "from-shell"

	t.Setenv(k, shellValue)

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(k+"=from-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := loadDotEnvFile(path, false); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(k); got != shellValue {
		t.Fatalf("override leaked: got %q, want %q (process env should win)", got, shellValue)
	}
}
