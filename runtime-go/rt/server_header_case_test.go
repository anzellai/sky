package rt

import "testing"

// HTTP header names are case-insensitive (RFC 9110 §5.1). The request carries
// them under Go's canonical spelling (`Content-Type`), so `Server.header`
// must find a header whatever case the app writes the name in.
func TestServerHeaderIsCaseInsensitive(t *testing.T) {
	req := SkyRequest{Headers: map[string]any{
		"Content-Type":    "application/json",
		"X-Request-Id":    "abc",
		"x-custom-lower":  "kept", // a key that is not canonical (set by hand)
		"Accept-Language": "en-GB",
	}}
	cases := []struct {
		name string
		want string
	}{
		{"Content-Type", "application/json"},
		{"content-type", "application/json"},
		{"CONTENT-TYPE", "application/json"},
		{"x-request-id", "abc"},
		{"X-REQUEST-ID", "abc"},
		{"X-Custom-Lower", "kept"},
		{"accept-language", "en-GB"},
	}
	for _, c := range cases {
		got := Server_header(c.name, req)
		m, ok := got.(SkyMaybe[any])
		if !ok || m.Tag != 0 {
			t.Fatalf("Server.header %q: want Just %q, got %#v", c.name, c.want, got)
		}
		if m.JustValue != c.want {
			t.Fatalf("Server.header %q: want %q, got %q", c.name, c.want, m.JustValue)
		}
	}
	if m, _ := Server_header("x-absent", req).(SkyMaybe[any]); m.Tag == 0 {
		t.Fatalf("Server.header of an absent header must be Nothing")
	}
}
