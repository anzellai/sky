package rt

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestColonToMuxPattern(t *testing.T) {
	cases := []struct {
		in    string
		out   string
		names []string
	}{
		{"/", "/", nil},
		{"/users", "/users", nil},
		{"/users/:id", "/users/{id}", []string{"id"}},
		{"/users/:id/posts/:pid", "/users/{id}/posts/{pid}", []string{"id", "pid"}},
		{"/a/:x/b", "/a/{x}/b", []string{"x"}},
		{"/lit/:", "/lit/:", nil}, // bare colon stays literal (no mux panic)
	}
	for _, c := range cases {
		gotOut, gotNames := colonToMuxPattern(c.in)
		if gotOut != c.out {
			t.Errorf("colonToMuxPattern(%q) pattern = %q, want %q", c.in, gotOut, c.out)
		}
		if !reflect.DeepEqual(gotNames, c.names) {
			t.Errorf("colonToMuxPattern(%q) names = %v, want %v", c.in, gotNames, c.names)
		}
	}
}

// End-to-end: a `:id` route must match a concrete URL AND make the captured
// value visible through Server_param — the two halves of the fix.
func TestServerParamRouteEndToEnd(t *testing.T) {
	mux := http.NewServeMux()
	translated, names := colonToMuxPattern("/users/:id")
	mux.HandleFunc(translated, func(w http.ResponseWriter, req *http.Request) {
		skyReq := SkyRequest{Params: make(map[string]any)}
		for _, pn := range names {
			skyReq.Params[pn] = req.PathValue(pn)
		}
		got := Server_param("id", skyReq)
		just, ok := got.(SkyMaybe[any])
		if !ok || just.Tag != 0 { // Just = Tag 0
			t.Fatalf("Server_param returned %#v, want Just", got)
		}
		w.Write([]byte(just.JustValue.(string)))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/users/42")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /users/42 → %d, want 200 (route did not match)", resp.StatusCode)
	}
	buf := make([]byte, 8)
	n, _ := resp.Body.Read(buf)
	if string(buf[:n]) != "42" {
		t.Fatalf("Server.param id = %q, want 42", string(buf[:n]))
	}
}
