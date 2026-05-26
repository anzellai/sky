package rt

import (
	"testing"
	"time"
)

// TestParseTTL — locks the env > sky.toml > default precedence AND
// the dual-shape acceptance (Go-duration string OR bare-int seconds)
// at each layer.
//
// Pre-fix bug: TTL was only read from `SKY_LIVE_TTL` env via
// strconv.Atoi, so `SKY_LIVE_TTL=24h` and any sky.toml `ttl = "24h"`
// silently fell back to the 30-minute default. The sky.toml `ttl`
// key was completely unread. CLAUDE.md documents `30m` as the
// default form, so duration strings MUST work at both layers.
func TestParseTTL(t *testing.T) {
	def := 30 * time.Minute
	cases := []struct {
		name     string
		env      string
		toml     string
		expected time.Duration
	}{
		// ── env wins over toml ─────────────────────────────────
		{"env-duration-wins-over-toml", "24h", "5m", 24 * time.Hour},
		{"env-int-seconds-wins-over-toml", "60", "5m", 60 * time.Second},

		// ── env duration string accepted ───────────────────────
		{"env-30m", "30m", "", 30 * time.Minute},
		{"env-24h", "24h", "", 24 * time.Hour},
		{"env-1h30m", "1h30m", "", 90 * time.Minute},
		{"env-45s", "45s", "", 45 * time.Second},

		// ── env bare-int seconds accepted (backward compat) ────
		{"env-1800-seconds", "1800", "", 30 * time.Minute},
		{"env-86400-seconds", "86400", "", 24 * time.Hour},

		// ── toml fallback when env unset ───────────────────────
		{"toml-duration-30m", "", "30m", 30 * time.Minute},
		{"toml-duration-24h", "", "24h", 24 * time.Hour},
		{"toml-int-1800", "", "1800", 30 * time.Minute},

		// ── empty + whitespace → default ───────────────────────
		{"both-empty", "", "", def},
		{"both-whitespace", "  ", " \t ", def},

		// ── unparseable at one layer falls through to next ─────
		{"env-garbage-toml-good", "abc", "10m", 10 * time.Minute},
		{"env-good-toml-garbage", "10m", "abc", 10 * time.Minute},
		{"both-garbage", "abc", "xyz", def},

		// ── zero / negative rejected at each layer, fall through ─
		{"env-zero-toml-good", "0", "5m", 5 * time.Minute},
		{"env-negative-toml-good", "-1", "5m", 5 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseTTL(c.env, c.toml, def)
			if got != c.expected {
				t.Errorf("parseTTL(%q, %q, %v) = %v; want %v",
					c.env, c.toml, def, got, c.expected)
			}
		})
	}
}
