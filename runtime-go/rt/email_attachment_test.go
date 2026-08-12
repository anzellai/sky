package rt

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"
)

// `Std.Email` silently DROPPED attachments.
//
// `withAttachment` is honoured all the way through the pure Sky layer and
// `readEmailMessage` decodes every attachment into `emailMsg.Attachments` for
// every provider — and then THREE of the four transports built their payload
// without ever reading the field. `sendSmtp` wrote a single flat body,
// `sendSendGrid` had no `attachments` key at all, and `sendSes` used
// `Content.Simple`, which has nowhere to put one. Each returned
// `Ok "<provider>-<hex>"`: the mail arrived, correct in every other respect,
// with the attachment simply absent and nothing for the caller to branch on.
//
// (The in-tree note at `docs/ci-layer2-members.md` recorded this for SMTP alone
// and said the HTTP providers were fine. Only Resend was, and it dropped the
// attachment's MIME TYPE.)
//
// `sendSmtp` dropped a second thing for the same reason: a message carrying
// BOTH a text and an HTML body sent only the text. Same class — part of the
// message discarded with no signal.

// ─────────────────────── a fake SMTP server ───────────────────────

// smtpCapture is a minimal SMTP server that speaks just enough for
// `net/smtp.SendMail` and records the DATA payload verbatim. It deliberately
// does NOT advertise STARTTLS, so an unauthenticated send proceeds in plaintext.
type smtpCapture struct {
	addr string
	mu   sync.Mutex
	data []string
	ln   net.Listener
}

func newSMTPCapture(t *testing.T) *smtpCapture {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &smtpCapture{addr: ln.Addr().String(), ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *smtpCapture) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	w := func(line string) { _, _ = c.Write([]byte(line + "\r\n")) }
	w("220 fake ESMTP")
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			w("250-fake")
			w("250 SIZE 35882577")
		case strings.HasPrefix(cmd, "MAIL FROM"), strings.HasPrefix(cmd, "RCPT TO"):
			w("250 OK")
		case strings.HasPrefix(cmd, "DATA"):
			w("354 Send data")
			var sb strings.Builder
			for {
				dl, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				sb.WriteString(dl)
			}
			s.mu.Lock()
			s.data = append(s.data, sb.String())
			s.mu.Unlock()
			w("250 OK: queued")
		case strings.HasPrefix(cmd, "QUIT"):
			w("221 Bye")
			return
		case strings.HasPrefix(cmd, "RSET"), strings.HasPrefix(cmd, "NOOP"):
			w("250 OK")
		default:
			w("250 OK")
		}
	}
}

func (s *smtpCapture) last(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data) == 0 {
		t.Fatal("the SMTP server received no message")
	}
	return s.data[len(s.data)-1]
}

func (s *smtpCapture) hostPort() (string, int) {
	h, p, _ := net.SplitHostPort(s.addr)
	var n int
	for _, c := range p {
		n = n*10 + int(c-'0')
	}
	return h, n
}

// ─────────────────────── shared fixtures ───────────────────────

const (
	attachName = "invoice.pdf"
	attachMime = "application/pdf"
)

// attachBody deliberately contains bytes that a naive builder would corrupt:
// a CRLF, a lone dot on its own line (SMTP dot-stuffing), and a byte outside
// ASCII. If it survives the round trip, the encoding is real.
var attachBody = "%PDF-1.4\r\n.\r\nstream\x00\xffend"

func msgWithAttachment() map[string]any {
	return map[string]any{
		"from":     "a@example.com",
		"to":       []any{"b@example.com"},
		"cc":       []any{},
		"bcc":      []any{},
		"subject":  "Your invoice",
		"textBody": "See attached.",
		"htmlBody": "",
		"replyTo":  "",
		"attachments": []any{
			map[string]any{
				"filename": attachName,
				"mimeType": attachMime,
				"content":  attachBody,
			},
		},
	}
}

func msgNoAttachment() map[string]any {
	m := msgWithAttachment()
	m["attachments"] = []any{}
	return m
}

func adt(name string, fields ...any) SkyADT {
	return SkyADT{SkyName: name, Fields: fields}
}

func sendEmail(t *testing.T, provider any, msg any) any {
	t.Helper()
	t.Setenv("SKY_EMAIL_DRY_RUN", "")
	fn, ok := Email_send(provider, msg).(func() any)
	if !ok {
		t.Fatalf("Email_send did not return a Task thunk")
	}
	res, ok := fn().(SkyResult[any, any])
	if !ok {
		t.Fatalf("Email_send did not return a Result")
	}
	if res.Tag != 0 {
		t.Fatalf("Email_send returned Err: %#v", res.ErrValue)
	}
	return res.OkValue
}

// findAttachmentPart walks a MIME message (recursing through nested multiparts)
// and returns the DECODED bytes of the part whose filename matches.
func findAttachmentPart(t *testing.T, header mail.Header, body io.Reader, filename string) (string, string, bool) {
	t.Helper()
	ct := header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return "", "", false
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return "", "", false
	}
	return walkMultipart(t, multipart.NewReader(body, params["boundary"]), filename)
}

func walkMultipart(t *testing.T, mr *multipart.Reader, filename string) (string, string, bool) {
	t.Helper()
	for {
		p, err := mr.NextPart()
		if err != nil {
			return "", "", false
		}
		mediaType, params, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		if strings.HasPrefix(mediaType, "multipart/") {
			if c, ty, ok := walkMultipart(t, multipart.NewReader(p, params["boundary"]), filename); ok {
				return c, ty, true
			}
			continue
		}
		if p.FileName() != filename {
			continue
		}
		raw, err := io.ReadAll(p)
		if err != nil {
			t.Fatalf("reading part: %v", err)
		}
		// `multipart.Part` does not decode base64 for us.
		if strings.EqualFold(p.Header.Get("Content-Transfer-Encoding"), "base64") {
			dec, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(raw)), ""))
			if err != nil {
				t.Fatalf("attachment %q is not valid base64: %v", filename, err)
			}
			raw = dec
		}
		return string(raw), mediaType, true
	}
}

// collectBodies returns the decoded text/plain and text/html parts.
func collectBodies(t *testing.T, header mail.Header, body io.Reader) map[string]string {
	t.Helper()
	out := map[string]string{}
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		b, _ := io.ReadAll(body)
		out["text/plain"] = string(b)
		return out
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		b, _ := io.ReadAll(body)
		out[mediaType] = string(b)
		return out
	}
	collectParts(t, multipart.NewReader(body, params["boundary"]), out)
	return out
}

func collectParts(t *testing.T, mr *multipart.Reader, out map[string]string) {
	t.Helper()
	for {
		p, err := mr.NextPart()
		if err != nil {
			return
		}
		mediaType, params, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		if strings.HasPrefix(mediaType, "multipart/") {
			collectParts(t, multipart.NewReader(p, params["boundary"]), out)
			continue
		}
		if p.FileName() != "" {
			continue
		}
		b, _ := io.ReadAll(p)
		out[mediaType] = string(b)
	}
}

// ─────────────────────── SMTP ───────────────────────

func TestSmtpDeliversAttachments(t *testing.T) {
	srv := newSMTPCapture(t)
	host, port := srv.hostPort()
	provider := adt("Smtp", map[string]any{
		"host": host, "port": port, "user": "", "pass": "",
	})
	sendEmail(t, provider, msgWithAttachment())

	wire := srv.last(t)
	parsed, err := mail.ReadMessage(strings.NewReader(wire))
	if err != nil {
		t.Fatalf("the SMTP wire message does not parse as RFC 5322: %v\n%s", err, wire)
	}
	if parsed.Header.Get("MIME-Version") != "1.0" {
		t.Fatalf("no MIME-Version header — a message with an attachment must be MIME:\n%s", wire)
	}
	got, gotType, ok := findAttachmentPart(t, parsed.Header, parsed.Body, attachName)
	if !ok {
		t.Fatalf("the attachment %q is NOT in the SMTP message — it was silently dropped:\n%s", attachName, wire)
	}
	if got != attachBody {
		t.Fatalf("attachment bytes did not survive the wire:\n got %q\nwant %q", got, attachBody)
	}
	if gotType != attachMime {
		t.Fatalf("attachment content type: got %q, want %q", gotType, attachMime)
	}
}

func TestSmtpKeepsTheBodyAlongsideTheAttachment(t *testing.T) {
	srv := newSMTPCapture(t)
	host, port := srv.hostPort()
	sendEmail(t, adt("Smtp", map[string]any{"host": host, "port": port, "user": "", "pass": ""}),
		msgWithAttachment())
	parsed, err := mail.ReadMessage(strings.NewReader(srv.last(t)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bodies := collectBodies(t, parsed.Header, parsed.Body)
	if strings.TrimSpace(bodies["text/plain"]) != "See attached." {
		t.Fatalf("the text body was lost when the attachment was added: %#v", bodies)
	}
}

// A message with BOTH bodies sent only the text one: `bodyText` was set from
// `TextBody` and the `HtmlBody` branch only ran when the text was EMPTY.
func TestSmtpSendsBothTextAndHtmlBodies(t *testing.T) {
	srv := newSMTPCapture(t)
	host, port := srv.hostPort()
	msg := msgNoAttachment()
	msg["textBody"] = "plain version"
	msg["htmlBody"] = "<p>rich version</p>"
	sendEmail(t, adt("Smtp", map[string]any{"host": host, "port": port, "user": "", "pass": ""}), msg)

	parsed, err := mail.ReadMessage(strings.NewReader(srv.last(t)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bodies := collectBodies(t, parsed.Header, parsed.Body)
	if strings.TrimSpace(bodies["text/plain"]) != "plain version" {
		t.Fatalf("text body missing: %#v", bodies)
	}
	if strings.TrimSpace(bodies["text/html"]) != "<p>rich version</p>" {
		t.Fatalf("html body was silently dropped when a text body was also set: %#v", bodies)
	}
}

// The no-attachment, single-body path must keep working exactly as before.
func TestSmtpPlainMessageStillWorks(t *testing.T) {
	srv := newSMTPCapture(t)
	host, port := srv.hostPort()
	sendEmail(t, adt("Smtp", map[string]any{"host": host, "port": port, "user": "", "pass": ""}),
		msgNoAttachment())
	parsed, err := mail.ReadMessage(strings.NewReader(srv.last(t)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Header.Get("Subject") != "Your invoice" {
		t.Fatalf("Subject header: %q", parsed.Header.Get("Subject"))
	}
	if parsed.Header.Get("To") != "b@example.com" {
		t.Fatalf("To header: %q", parsed.Header.Get("To"))
	}
	b, _ := io.ReadAll(parsed.Body)
	if strings.TrimSpace(string(b)) != "See attached." {
		t.Fatalf("body: %q", string(b))
	}
}

// ─────────────────────── HTTP providers ───────────────────────

// captureJSON stands in for a provider's REST endpoint and records the request
// body. `emailEndpoint` reads SKY_EMAIL_ENDPOINT_<PROVIDER>, which is what the
// dev/CI fixtures already use.
func captureJSON(t *testing.T, provider string, reply string) *map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SKY_EMAIL_ENDPOINT_"+strings.ToUpper(provider), srv.URL)
	return &got
}

func TestSendGridSendsAttachments(t *testing.T) {
	body := captureJSON(t, "sendgrid", `{}`)
	sendEmail(t, adt("SendGrid", "sg-key"), msgWithAttachment())

	atts, ok := (*body)["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("SendGrid request has no `attachments` — the attachment was silently dropped: %#v", *body)
	}
	a := atts[0].(map[string]any)
	if a["filename"] != attachName {
		t.Fatalf("filename: %#v", a["filename"])
	}
	if a["type"] != attachMime {
		t.Fatalf("type: %#v (SendGrid needs the MIME type)", a["type"])
	}
	if a["disposition"] != "attachment" {
		t.Fatalf("disposition: %#v", a["disposition"])
	}
	dec, err := base64.StdEncoding.DecodeString(a["content"].(string))
	if err != nil {
		t.Fatalf("SendGrid `content` must be base64: %v", err)
	}
	if string(dec) != attachBody {
		t.Fatalf("attachment bytes: got %q want %q", string(dec), attachBody)
	}
}

func TestSendGridWithoutAttachmentsOmitsTheKey(t *testing.T) {
	body := captureJSON(t, "sendgrid", `{}`)
	sendEmail(t, adt("SendGrid", "sg-key"), msgNoAttachment())
	if _, present := (*body)["attachments"]; present {
		t.Fatalf("an empty attachment list must not add the key: %#v", *body)
	}
}

func TestResendSendsTheAttachmentContentType(t *testing.T) {
	body := captureJSON(t, "resend", `{"id":"re_1"}`)
	sendEmail(t, adt("Resend", "re-key"), msgWithAttachment())

	atts, ok := (*body)["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("Resend request has no `attachments`: %#v", *body)
	}
	a := atts[0].(map[string]any)
	if a["content_type"] != attachMime {
		t.Fatalf("Resend omitted `content_type` (%#v) — the provider then has to sniff the bytes", a["content_type"])
	}
	// `content` is a []byte in Go, which json.Marshal renders as base64.
	dec, err := base64.StdEncoding.DecodeString(a["content"].(string))
	if err != nil {
		t.Fatalf("Resend `content` must be base64: %v", err)
	}
	if string(dec) != attachBody {
		t.Fatalf("attachment bytes: got %q want %q", string(dec), attachBody)
	}
}

// SES v2's `Content.Simple` has nowhere to put an attachment. With one present
// the call must switch to `Content.Raw.Data`, a base64 MIME message.
func TestSesUsesRawContentWhenAttachmentsArePresent(t *testing.T) {
	body := captureJSON(t, "ses", `{"MessageId":"m1"}`)
	sendEmail(t, adt("Ses", map[string]any{
		"region": "eu-west-1", "key": "AKIA", "secret": "s3cret",
	}), msgWithAttachment())

	content, ok := (*body)["Content"].(map[string]any)
	if !ok {
		t.Fatalf("no Content: %#v", *body)
	}
	raw, ok := content["Raw"].(map[string]any)
	if !ok {
		t.Fatalf("SES still used Content.Simple, which cannot carry an attachment — it was silently dropped: %#v", content)
	}
	dec, err := base64.StdEncoding.DecodeString(raw["Data"].(string))
	if err != nil {
		t.Fatalf("SES Raw.Data must be base64: %v", err)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(string(dec)))
	if err != nil {
		t.Fatalf("SES Raw.Data is not a parseable MIME message: %v\n%s", err, string(dec))
	}
	got, _, found := findAttachmentPart(t, parsed.Header, parsed.Body, attachName)
	if !found {
		t.Fatalf("the attachment is not in the SES raw message:\n%s", string(dec))
	}
	if got != attachBody {
		t.Fatalf("attachment bytes: got %q want %q", got, attachBody)
	}
}

func TestSesKeepsSimpleContentWithoutAttachments(t *testing.T) {
	body := captureJSON(t, "ses", `{"MessageId":"m1"}`)
	sendEmail(t, adt("Ses", map[string]any{
		"region": "eu-west-1", "key": "AKIA", "secret": "s3cret",
	}), msgNoAttachment())
	content := (*body)["Content"].(map[string]any)
	if _, ok := content["Simple"]; !ok {
		t.Fatalf("a message with no attachment must keep using Content.Simple: %#v", content)
	}
}

// A filename or subject outside ASCII must not corrupt the message or smuggle
// a header: both are encoded, not interpolated raw.
func TestSmtpEncodesNonAsciiAndRejectsHeaderInjection(t *testing.T) {
	srv := newSMTPCapture(t)
	host, port := srv.hostPort()
	msg := msgWithAttachment()
	msg["subject"] = "Rechnung – Übersicht\r\nBcc: attacker@example.com"
	msg["attachments"] = []any{
		map[string]any{"filename": "Übersicht.pdf", "mimeType": attachMime, "content": attachBody},
	}
	sendEmail(t, adt("Smtp", map[string]any{"host": host, "port": port, "user": "", "pass": ""}), msg)

	wire := srv.last(t)
	parsed, err := mail.ReadMessage(strings.NewReader(wire))
	if err != nil {
		t.Fatalf("non-ASCII subject broke the message: %v\n%s", err, wire)
	}
	if parsed.Header.Get("Bcc") != "" {
		t.Fatalf("a CRLF in the subject injected a header:\n%s", wire)
	}
	dec, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("subject is not a decodable RFC 2047 header: %v", err)
	}
	if strings.Contains(dec, "\r") || strings.Contains(dec, "\n") {
		t.Fatalf("subject still carries a newline: %q", dec)
	}
	if !strings.Contains(dec, "Rechnung") {
		t.Fatalf("subject lost its text: %q", dec)
	}
	if _, _, ok := findAttachmentPart(t, parsed.Header, parsed.Body, "Übersicht.pdf"); !ok {
		t.Fatalf("a non-ASCII attachment filename was not round-tripped:\n%s", wire)
	}
}
