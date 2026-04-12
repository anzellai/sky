package rt

import (
	"fmt"
	"html"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/rivo/uniseg"
)

// ═══════════════════════════════════════════════════════════
// Sky.Core.String — validation helpers
// ═══════════════════════════════════════════════════════════

// String.isEmail : String -> Bool
// RFC 5322 syntactic check via net/mail. Does not verify the mailbox exists.
func String_isEmail(s any) any {
	addr, err := mail.ParseAddress(fmt.Sprintf("%v", s))
	if err != nil {
		return false
	}
	// Reject inputs that parsed but included a name component or extras.
	// e.g. "foo <bar@baz.com>" parses to address bar@baz.com — we only want
	// plain "user@host" forms.
	return addr.Address == fmt.Sprintf("%v", s) && strings.Contains(addr.Address, "@")
}

// String.isUrl : String -> Bool
// Requires a parseable absolute URL with scheme http/https/ws/wss.
// Reject relative paths and javascript: / data: URLs to prevent common XSS
// footguns when displaying user-submitted links.
func String_isUrl(s any) any {
	u, err := url.Parse(fmt.Sprintf("%v", s))
	if err != nil {
		return false
	}
	if !u.IsAbs() || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ws", "wss":
		return true
	default:
		return false
	}
}

// String.slugify : String -> String
// Converts arbitrary text to a URL-safe slug:
//   - lowercase
//   - Unicode letters/digits pass through
//   - whitespace → single '-'
//   - punctuation + control chars dropped
//   - trimmed, deduplicated dashes
// Use for blog-post slugs, file names, etc. Unicode-aware: "Café con leche" →
// "café-con-leche"  (retains "é" — SEO-friendly and URL-legal per RFC 3987).
func String_slugify(s any) any {
	in := fmt.Sprintf("%v", s)
	var b strings.Builder
	b.Grow(len(in))
	lastDash := true // so leading punctuation doesn't emit a dash
	for _, r := range in {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			lastDash = false
		case unicode.IsSpace(r) || r == '-' || r == '_':
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// String.htmlEscape : String -> String
// Escapes &, <, >, ", ' so the string is safe to insert into HTML content
// or a double-quoted HTML attribute. Use before concatenating untrusted text
// into raw HTML strings. Sky.Live's VNode renderer already escapes — this
// helper is for code that builds HTML manually (Sky.Http.Server handlers
// returning Server.html, templating into an email body, etc.).
func String_htmlEscape(s any) any {
	return html.EscapeString(fmt.Sprintf("%v", s))
}

// String.truncate : Int -> String -> String
// Cuts s to at most n Unicode grapheme clusters — never breaks in the middle
// of a multi-byte character or emoji ZWJ sequence. Preserves visual width for
// UI display purposes. If s is already ≤ n graphemes, returns it unchanged.
func String_truncate(n any, s any) any {
	str := fmt.Sprintf("%v", s)
	limit := AsInt(n)
	if limit <= 0 {
		return ""
	}
	// Walk graphemes to find cut-off byte offset.
	var b strings.Builder
	gr := uniseg.NewGraphemes(str)
	count := 0
	for gr.Next() {
		if count >= limit {
			break
		}
		b.WriteString(gr.Str())
		count++
	}
	return b.String()
}

// String.ellipsize : Int -> String -> String
// Like truncate but appends "…" when truncation occurs. For UI text.
func String_ellipsize(n any, s any) any {
	str := fmt.Sprintf("%v", s)
	limit := AsInt(n)
	if limit <= 0 {
		return ""
	}
	if uniseg.GraphemeClusterCount(str) <= limit {
		return str
	}
	truncated := String_truncate(limit, str).(string)
	return truncated + "…"
}

// ═══════════════════════════════════════════════════════════
// Sky.Core.Uuid
// ═══════════════════════════════════════════════════════════

// Uuid.v4 : Task String String
// Random UUIDv4 (RFC 4122). Returns 36-char canonical hyphenated form.
// Uses crypto/rand via github.com/google/uuid.
func Uuid_v4() any {
	return func() any {
		u, err := uuid.NewRandom()
		if err != nil {
			return Err[any, any]("uuid.v4: " + err.Error())
		}
		return Ok[any, any](u.String())
	}
}

// Uuid.v7 : Task String String
// UUIDv7 (time-ordered, draft but widely deployed). Sortable by creation
// time — better for database primary keys than v4.
func Uuid_v7() any {
	return func() any {
		u, err := uuid.NewV7()
		if err != nil {
			return Err[any, any]("uuid.v7: " + err.Error())
		}
		return Ok[any, any](u.String())
	}
}

// Uuid.parse : String -> Result String String
// Parses and canonicalises a UUID string, returning its 36-char form.
// Useful for rejecting malformed UUIDs at the API boundary.
func Uuid_parse(s any) any {
	u, err := uuid.Parse(fmt.Sprintf("%v", s))
	if err != nil {
		return Err[any, any]("uuid.parse: " + err.Error())
	}
	return Ok[any, any](u.String())
}

// ═══════════════════════════════════════════════════════════
// Sky.Http.Server — rate limiting
// ═══════════════════════════════════════════════════════════

// A simple token-bucket rate limiter keyed by arbitrary string (e.g. IP).
// Not a general-purpose implementation — good enough for defending a Sky
// server's public endpoints. For heavier workloads use an external service.

type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
	// capacity + refill rate are captured in the keying struct
}

type rateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*tokenBucket
	capacity   float64
	refillRate float64 // tokens per second
}

var rateLimiterRegistry = struct {
	sync.Mutex
	items map[string]*rateLimiter
}{items: map[string]*rateLimiter{}}

// RateLimit.allow : String -> String -> Int -> Int -> Bool
// (name, key, capacity, refillPerSec) → True if the request is allowed.
// Registers a named limiter the first time it's called; subsequent calls with
// the same name reuse the same configuration and bucket store.
func RateLimit_allow(name any, key any, capacity any, refillPerSec any) any {
	n := fmt.Sprintf("%v", name)
	k := fmt.Sprintf("%v", key)
	cap := float64(AsInt(capacity))
	rate := float64(AsInt(refillPerSec))
	if cap <= 0 || rate <= 0 {
		return false
	}

	rateLimiterRegistry.Lock()
	rl, ok := rateLimiterRegistry.items[n]
	if !ok {
		rl = &rateLimiter{
			buckets:    map[string]*tokenBucket{},
			capacity:   cap,
			refillRate: rate,
		}
		rateLimiterRegistry.items[n] = rl
	}
	rateLimiterRegistry.Unlock()

	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[k]
	now := time.Now()
	if !ok {
		b = &tokenBucket{tokens: rl.capacity, lastRefill: now}
		rl.buckets[k] = b
	}
	// Refill based on elapsed time.
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * rl.refillRate
	if b.tokens > rl.capacity {
		b.tokens = rl.capacity
	}
	b.lastRefill = now
	if b.tokens < 1.0 {
		return false
	}
	b.tokens--
	return true
}
