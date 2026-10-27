//go:build !js

package rt

import (
	"encoding/base64"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
)

// `Std.Email` custom headers (issue #300).
//
// `EmailMessage` had no field for a header, so no Sky app could send the
// RFC 2369 / RFC 8058 unsubscribe pair that Gmail and Yahoo require from bulk
// senders, with any provider. A message now carries `headers : List Header`,
// where `Header` is opaque and built only by `Email.header` (validated) or
// `Email.listUnsubscribe` (total). At run time a Header is a stdlib ADT:
//
//	HeaderGroup (keys : List String) (fields : List HeaderField)  -- HeaderField name value
//
// These tests build that shape directly, as the compiled Sky code hands it to
// `Email_send`, and assert what each provider puts on the wire.

// headerGroup is the runtime shape of one `Std.Email.Header` value.
func headerGroup(keys []string, fields ...[2]string) SkyADT {
	ks := make([]any, len(keys))
	for i, k := range keys {
		ks[i] = k
	}
	fs := make([]any, len(fields))
	for i, f := range fields {
		fs[i] = SkyADT{SkyName: "HeaderField", Fields: []any{f[0], f[1]}}
	}
	return SkyADT{SkyName: "HeaderGroup", Fields: []any{ks, fs}}
}

const (
	unsubURL    = "https://example.com/u/abc"
	unsubMailto = "mailto:unsub@example.com"
	unsubValue  = "<" + unsubURL + ">, <" + unsubMailto + ">"
	oneClick    = "List-Unsubscribe=One-Click"
)

// msgWithHeaders is a plain text message carrying the one-click unsubscribe
// pair (as `Email.listUnsubscribe { oneClick = True }` builds it) and one
// custom header with a non-ASCII value.
func msgWithHeaders() map[string]any {
	m := msgNoAttachment()
	m["headers"] = []any{
		headerGroup([]string{"list-unsubscribe", "list-unsubscribe-post"},
			[2]string{"List-Unsubscribe", unsubValue},
			[2]string{"List-Unsubscribe-Post", oneClick}),
		headerGroup([]string{"x-campaign"}, [2]string{"X-Campaign", "Übersicht"}),
	}
	return m
}

// captureRaw records the request body BYTES (not a decoded map), so the
// no-header goldens below compare exactly what went on the wire.
func captureRaw(t *testing.T, provider string, reply string) *[]byte {
	t.Helper()
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SKY_EMAIL_ENDPOINT_"+strings.ToUpper(provider), srv.URL)
	return &got
}

func sesProvider() SkyADT {
	return adt("Ses", map[string]any{"region": "eu-west-1", "key": "AKIA", "secret": "s3cret"})
}

// sendEmailErr runs Email_send and returns the Err message; it fails the test
// on Ok.
func sendEmailErr(t *testing.T, provider any, msg any) string {
	t.Helper()
	t.Setenv("SKY_EMAIL_DRY_RUN", "")
	res, ok := Email_send(provider, msg).(func() any)().(SkyResult[any, any])
	if !ok {
		t.Fatalf("Email_send did not return a Result")
	}
	if res.Tag == 0 {
		t.Fatalf("Email_send returned Ok %#v, want Err", res.OkValue)
	}
	return errorMessage(res.ErrValue)
}

// countHeader counts the header lines named `name` (case-insensitive) in the
// header block of a MIME message.
func countHeader(wire, name string) int {
	head := wire
	if i := strings.Index(wire, "\r\n\r\n"); i >= 0 {
		head = wire[:i]
	}
	n := 0
	for _, line := range strings.Split(head, "\r\n") {
		if i := strings.Index(line, ":"); i > 0 && strings.EqualFold(line[:i], name) {
			n++
		}
	}
	return n
}

func assertJSONHeaders(t *testing.T, provider string, hdrs any) {
	t.Helper()
	m, ok := hdrs.(map[string]any)
	if !ok {
		t.Fatalf("%s request has no `headers` object: %#v", provider, hdrs)
	}
	if len(m) != 3 {
		t.Fatalf("%s headers: want exactly 3 keys, got %#v", provider, m)
	}
	if m["List-Unsubscribe"] != unsubValue {
		t.Fatalf("%s List-Unsubscribe: %#v", provider, m["List-Unsubscribe"])
	}
	if m["List-Unsubscribe-Post"] != oneClick {
		t.Fatalf("%s List-Unsubscribe-Post: %#v", provider, m["List-Unsubscribe-Post"])
	}
	dec, err := new(mime.WordDecoder).DecodeHeader(m["X-Campaign"].(string))
	if err != nil || dec != "Übersicht" {
		t.Fatalf("%s X-Campaign must be RFC 2047 encoded Übersicht: %#v (%v)", provider, m["X-Campaign"], err)
	}
}

// ─────────────────────── each provider carries each header once ───────────────────────

func TestResendSendsCustomHeaders(t *testing.T) {
	body := captureJSON(t, "resend", `{"id":"r1"}`)
	sendEmail(t, adt("Resend", "re-key"), msgWithHeaders())
	assertJSONHeaders(t, "Resend", (*body)["headers"])
}

func TestSendGridSendsCustomHeaders(t *testing.T) {
	body := captureJSON(t, "sendgrid", `{}`)
	sendEmail(t, adt("SendGrid", "sg-key"), msgWithHeaders())
	assertJSONHeaders(t, "SendGrid", (*body)["headers"])
}

func assertMIMEHeaders(t *testing.T, transport, wire string) {
	t.Helper()
	for _, name := range []string{"List-Unsubscribe", "List-Unsubscribe-Post", "X-Campaign"} {
		if n := countHeader(wire, name); n != 1 {
			t.Fatalf("%s: header %s appears %d times, want exactly once:\n%s", transport, name, n, wire)
		}
	}
	parsed, err := mail.ReadMessage(strings.NewReader(wire))
	if err != nil {
		t.Fatalf("%s: not a parseable message: %v\n%s", transport, err, wire)
	}
	if got := parsed.Header.Get("List-Unsubscribe"); got != unsubValue {
		t.Fatalf("%s List-Unsubscribe: %q", transport, got)
	}
	if got := parsed.Header.Get("List-Unsubscribe-Post"); got != oneClick {
		t.Fatalf("%s List-Unsubscribe-Post: %q", transport, got)
	}
	dec, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("X-Campaign"))
	if err != nil || dec != "Übersicht" {
		t.Fatalf("%s X-Campaign: %q (%v)", transport, parsed.Header.Get("X-Campaign"), err)
	}
	b, _ := io.ReadAll(parsed.Body)
	if strings.TrimRight(string(b), "\r\n") != "See attached." {
		t.Fatalf("%s body changed: %q", transport, string(b))
	}
}

func TestSmtpWritesCustomHeadersIntoTheMimeBlock(t *testing.T) {
	srv := newSMTPCapture(t)
	host, port := srv.hostPort()
	sendEmail(t, adt("Smtp", map[string]any{"host": host, "port": port, "user": "", "pass": ""}), msgWithHeaders())
	assertMIMEHeaders(t, "SMTP", srv.last(t))
}

// SES v2's `Content.Simple` has no field for a custom header, so a message with
// headers takes the `Content.Raw` path, exactly as attachments do, and SES and
// SMTP send the same bytes.
func TestSesUsesRawContentWhenHeadersArePresent(t *testing.T) {
	body := captureJSON(t, "ses", `{"MessageId":"m1"}`)
	sendEmail(t, sesProvider(), msgWithHeaders())
	content := (*body)["Content"].(map[string]any)
	raw, ok := content["Raw"].(map[string]any)
	if !ok {
		t.Fatalf("SES used Content.Simple, which cannot carry a custom header: %#v", content)
	}
	dec, err := base64.StdEncoding.DecodeString(raw["Data"].(string))
	if err != nil {
		t.Fatalf("SES Raw.Data must be base64: %v", err)
	}
	assertMIMEHeaders(t, "SES", string(dec))
}

// A plain (not one-click) List-Unsubscribe group sends no Post header.
func TestListUnsubscribeWithoutOneClickSendsNoPostHeader(t *testing.T) {
	srv := newSMTPCapture(t)
	host, port := srv.hostPort()
	m := msgNoAttachment()
	m["headers"] = []any{headerGroup([]string{"list-unsubscribe", "list-unsubscribe-post"},
		[2]string{"List-Unsubscribe", "<" + unsubURL + ">"})}
	sendEmail(t, adt("Smtp", map[string]any{"host": host, "port": port, "user": "", "pass": ""}), m)
	wire := srv.last(t)
	if countHeader(wire, "List-Unsubscribe") != 1 || countHeader(wire, "List-Unsubscribe-Post") != 0 {
		t.Fatalf("want List-Unsubscribe once and no Post header:\n%s", wire)
	}
}

// ─────────────────────── the run-time guard ───────────────────────

// `Email.header` refuses these before a Header exists. The kernel checks again,
// so a Header that reached it some other way can never inject a header or
// override one Std.Email sets itself.
func TestEmailSendRefusesAnUnsafeHeader(t *testing.T) {
	cases := map[string][2]string{
		"injected value":    {"X-Note", "ok\r\nBcc: x@example.com"},
		"lone LF in value":  {"X-Note", "ok\nBcc: x@example.com"},
		"reserved Bcc":      {"bcc", "x@example.com"},
		"reserved Subject":  {"SUBJECT", "spoofed"},
		"colon in name":     {"X-A:B", "v"},
		"space in name":     {"X A", "v"},
		"empty name":        {"", "v"},
		"CRLF in name":      {"X-A\r\nBcc", "v"},
		"non-ASCII in name": {"X-Übersicht", "v"},
	}
	// Every provider endpoint points at a server that fails the test when it is
	// reached: a refused header must stop the send before any request.
	reached := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a message with an unsafe header reached the provider (%s)", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(reached.Close)
	for _, p := range []string{"RESEND", "SENDGRID", "SES"} {
		t.Setenv("SKY_EMAIL_ENDPOINT_"+p, reached.URL)
	}
	smtpSrv := newSMTPCapture(t)
	smtpHost, smtpPort := smtpSrv.hostPort()
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			m := msgNoAttachment()
			m["headers"] = []any{headerGroup([]string{strings.ToLower(c[0])}, [2]string{c[0], c[1]})}
			for _, p := range []any{adt("Resend", "k"), adt("SendGrid", "k"), sesProvider(), adt("Smtp", map[string]any{"host": smtpHost, "port": smtpPort, "user": "", "pass": ""})} {
				msg := sendEmailErr(t, p, m)
				if !strings.Contains(msg, "header") {
					t.Fatalf("the Err should name the header problem: %q", msg)
				}
			}
		})
	}
	smtpSrv.mu.Lock()
	defer smtpSrv.mu.Unlock()
	if len(smtpSrv.data) != 0 {
		t.Fatalf("a message with an unsafe header reached the SMTP server:\n%s", smtpSrv.data[0])
	}
}

// ─────────────────────── no headers: byte-identical to before ───────────────────────

// Captured from the tree BEFORE custom headers existed. A message with no
// headers (the field absent, or an empty list, which is what `defaultMessage`
// now builds) must send exactly these bytes.
const (
	goldenResendNoHeaders   = `{"from":"a@example.com","subject":"Your invoice","text":"See attached.","to":["b@example.com"]}`
	goldenSendGridNoHeaders = `{"content":[{"type":"text/plain","value":"See attached."}],"from":{"email":"a@example.com"},"personalizations":[{"to":[{"email":"b@example.com"}]}],"subject":"Your invoice"}`
	goldenSesNoHeaders      = `{"Content":{"Simple":{"Body":{"Text":{"Charset":"UTF-8","Data":"See attached."}},"Subject":{"Charset":"UTF-8","Data":"Your invoice"}}},"Destination":{"ToAddresses":["b@example.com"]},"FromEmailAddress":"a@example.com"}`
	goldenSmtpNoHeaders     = "From: a@example.com\r\nTo: b@example.com\r\nSubject: Your invoice\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nSee attached.\r\n"
)

func noHeaderVariants() map[string]map[string]any {
	absent := msgNoAttachment()
	delete(absent, "headers")
	empty := msgNoAttachment()
	empty["headers"] = []any{}
	return map[string]map[string]any{"field absent": absent, "empty list": empty}
}

func TestNoHeadersSendByteIdenticalBodies(t *testing.T) {
	for label, m := range noHeaderVariants() {
		t.Run(label, func(t *testing.T) {
			for _, tc := range []struct {
				name, golden, reply string
				provider            any
			}{
				{"resend", goldenResendNoHeaders, `{"id":"r1"}`, adt("Resend", "k")},
				{"sendgrid", goldenSendGridNoHeaders, `{}`, adt("SendGrid", "k")},
				{"ses", goldenSesNoHeaders, `{"MessageId":"m1"}`, sesProvider()},
			} {
				got := captureRaw(t, tc.name, tc.reply)
				sendEmail(t, tc.provider, m)
				if string(*got) != tc.golden {
					t.Fatalf("%s body changed for a message with no headers:\n got %s\nwant %s", tc.name, string(*got), tc.golden)
				}
			}
			srv := newSMTPCapture(t)
			host, port := srv.hostPort()
			sendEmail(t, adt("Smtp", map[string]any{"host": host, "port": port, "user": "", "pass": ""}), m)
			if wire := srv.last(t); wire != goldenSmtpNoHeaders {
				t.Fatalf("SMTP message changed for a message with no headers:\n got %q\nwant %q", wire, goldenSmtpNoHeaders)
			}
		})
	}
}
