//go:build !js

package rt

// A remote slot with AUTH=google (Endpoint.withGoogleAuth): every request
// carries a Google ID token for the URL's origin in
// X-Serverless-Authorization, which Cloud Run checks and strips, while
// Authorization keeps the slot's key (the tier's service token). The token is
// minted by the Sky source (Std.Ai.Cloud.Google.idToken, a Go function of the
// same shape here) and reused until five minutes before it expires.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJWT is an unsigned JWT with an audience and an expiry.
func fakeJWT(aud string, exp time.Time) string {
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	body, _ := json.Marshal(map[string]any{"aud": aud, "exp": exp.Unix()})
	return head + "." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
}

type fakeIDTokens struct {
	mu        sync.Mutex
	calls     int32
	audiences []string
	exp       time.Duration
	fail      any
}

func (f *fakeIDTokens) fn() any {
	return func(audA any) any {
		return func() any {
			n := atomic.AddInt32(&f.calls, 1)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.audiences = append(f.audiences, audA.(string))
			if f.fail != nil {
				return Err[any, any](f.fail)
			}
			return Ok[any, any](Secret{v: fakeJWT(audA.(string), time.Now().Add(f.exp)) + "-" + string(rune('a'+n))})
		}
	}
}

// googleServer records the two auth headers of each request.
func googleServer(t *testing.T) (*httptest.Server, *[][2]string) {
	t.Helper()
	var mu sync.Mutex
	seen := [][2]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, [2]string{r.Header.Get("Authorization"), r.Header.Get("X-Serverless-Authorization")})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func googleBuilder(url string, tokens *fakeIDTokens) aiEndpointBuilder {
	b := endpointBuilder("tier")
	b.url = url + "/v1"
	b.key, b.hasKey = "service-token", true
	b.auth = "google"
	b.idToken = tokens.fn()
	return b
}

func TestAiGoogleAuthSendsBothHeadersAndReusesTheToken(t *testing.T) {
	aiTestEnv(t, nil)
	srv, seen := googleServer(t)
	tokens := &fakeIDTokens{exp: time.Hour}
	s, err := aiSlotFor(googleBuilder(srv.URL, tokens))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.post("/chat/completions", chatBody("hi")); err != nil {
			t.Fatal(err)
		}
	}
	if tokens.calls != 1 {
		t.Fatalf("the ID token is minted once and reused, minted %d times", tokens.calls)
	}
	if tokens.audiences[0] != srv.URL {
		t.Fatalf("the audience is the URL's origin %q, got %q", srv.URL, tokens.audiences[0])
	}
	for _, h := range *seen {
		if h[0] != "Bearer service-token" {
			t.Fatalf("Authorization keeps the slot's key, got %q", h[0])
		}
		if !strings.HasPrefix(h[1], "Bearer ") || strings.Count(h[1], ".") < 2 {
			t.Fatalf("X-Serverless-Authorization carries the ID token, got %q", h[1])
		}
	}
}

func TestAiGoogleAuthRefreshesATokenNearItsExpiry(t *testing.T) {
	aiTestEnv(t, nil)
	srv, seen := googleServer(t)
	// A token that expires in four minutes is inside the five-minute margin:
	// every call mints a new one.
	tokens := &fakeIDTokens{exp: 4 * time.Minute}
	s, err := aiSlotFor(googleBuilder(srv.URL, tokens))
	if err != nil {
		t.Fatal(err)
	}
	s.post("/chat/completions", chatBody("one"))
	s.post("/chat/completions", chatBody("two"))
	if tokens.calls != 2 {
		t.Fatalf("a token near its expiry is refreshed, minted %d times", tokens.calls)
	}
	if (*seen)[0][1] == (*seen)[1][1] {
		t.Fatalf("the refreshed token is sent")
	}
}

func TestAiGoogleAuthFailureIsTheSourcesTypedFailure(t *testing.T) {
	aiTestEnv(t, nil)
	srv, seen := googleServer(t)
	notConfigured := aiErrorValue(aiFailf(aiNotConfigured, "no credentials").with("setting", "GOOGLE_APPLICATION_CREDENTIALS"))
	tokens := &fakeIDTokens{exp: time.Hour, fail: notConfigured}
	s, err := aiSlotFor(googleBuilder(srv.URL, tokens))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.post("/chat/completions", chatBody("hi"))
	se, ok := err.(*aiSkyError)
	if !ok {
		t.Fatalf("the source's own failure comes back, got %v", err)
	}
	if df, ok := DomainOf(se.v); !ok || df.Code != string(aiNotConfigured) {
		t.Fatalf("NotConfigured, got %+v", df)
	}
	if len(*seen) != 0 {
		t.Fatalf("no request is sent without a token")
	}
}

func TestAiAuthBearerSendsNoIDToken(t *testing.T) {
	aiTestEnv(t, nil)
	srv, seen := googleServer(t)
	tokens := &fakeIDTokens{exp: time.Hour}
	b := googleBuilder(srv.URL, tokens)
	b.auth = ""
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	s.post("/chat/completions", chatBody("hi"))
	if tokens.calls != 0 || (*seen)[0][1] != "" {
		t.Fatalf("a bearer slot mints no ID token: calls %d header %q", tokens.calls, (*seen)[0][1])
	}
}

func TestAiAuthIsReadFromTheSlotsVariable(t *testing.T) {
	aiTestEnv(t, nil)
	srv, seen := googleServer(t)
	t.Setenv("SKY_AI_TIER_AUTH", "google")
	tokens := &fakeIDTokens{exp: time.Hour}
	b := googleBuilder(srv.URL, tokens)
	b.auth = ""
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	s.post("/chat/completions", chatBody("hi"))
	if tokens.calls != 1 || (*seen)[0][1] == "" {
		t.Fatalf("SKY_AI_TIER_AUTH=google turns the ID token on")
	}
	t.Setenv("SKY_AI_TIER_AUTH", "kerberos")
	aiResetSlots()
	if _, err := aiSlotFor(b); failCode(err) != aiInvalidSetting {
		t.Fatalf("an unknown AUTH is InvalidSetting, got %v", err)
	}
}
