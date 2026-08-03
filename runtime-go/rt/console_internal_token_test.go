package rt

import (
	"net/http/httptest"
	"testing"
)

// F1: a loopback request with NO token must be DENIED in production (previously
// the loopback-IP bypass returned true → console APIs open behind a proxy).
func TestConsoleAccessNoLoopbackBypassInProd(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_TOKEN", "console-token-32-bytes-of-testdataxx")
	internal := ConsoleInternalTokenInit()

	// loopback peer, no bearer → denied
	req := httptest.NewRequest("GET", "/_sky/console/api/overview", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	w := httptest.NewRecorder()
	if consoleAccessAllowed(w, req) {
		t.Fatal("F1: loopback request with no token must be denied in production")
	}

	// same loopback peer WITH the internal token → allowed (the console sub-app)
	req2 := httptest.NewRequest("GET", "/_sky/console/api/overview", nil)
	req2.RemoteAddr = "127.0.0.1:5555"
	req2.Header.Set("Authorization", "Bearer "+internal)
	w2 := httptest.NewRecorder()
	if !consoleAccessAllowed(w2, req2) {
		t.Fatal("internal token must be accepted")
	}

	// admin token → allowed (external operator tool)
	t.Setenv("SKY_ADMIN_TOKEN", "admin-token-32-bytes-of-testdata-xxxx")
	req3 := httptest.NewRequest("GET", "/_sky/console/api/overview", nil)
	req3.RemoteAddr = "10.0.0.9:443" // not even loopback
	req3.Header.Set("Authorization", "Bearer admin-token-32-bytes-of-testdata-xxxx")
	w3 := httptest.NewRecorder()
	if !consoleAccessAllowed(w3, req3) {
		t.Fatal("admin token must be accepted from any address")
	}

	// wrong token → denied
	req4 := httptest.NewRequest("GET", "/_sky/console/api/overview", nil)
	req4.RemoteAddr = "127.0.0.1:5555"
	req4.Header.Set("Authorization", "Bearer wrong-token")
	w4 := httptest.NewRecorder()
	if consoleAccessAllowed(w4, req4) {
		t.Fatal("wrong token must be denied")
	}
}
