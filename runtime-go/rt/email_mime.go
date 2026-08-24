//go:build !js

// email_mime.go — the RFC 5322 / MIME message `Std.Email` puts on the wire.
//
// # Why this file exists
//
// `withAttachment` was honoured all the way through the pure Sky layer and
// `readEmailMessage` decoded every attachment for every provider — and then
// THREE of the four transports built their payload without ever reading the
// field. `sendSmtp` wrote one flat body, `sendSendGrid` had no `attachments`
// key, and `sendSes` used `Content.Simple`, which has nowhere to put one. Each
// returned `Ok "<provider>-<hex>"`, so the mail arrived, correct in every other
// respect, with the attachment silently absent and nothing for the caller to
// branch on. Silently discarding part of a message is the worst of the three
// possible outcomes (deliver / fail loudly / drop), so this delivers it.
//
// `sendSmtp` dropped a second thing the same way: a message carrying BOTH a
// text and an HTML body sent only the text, because the HTML branch ran only
// when the text was empty.
//
// And it was header-injectable. `"Subject: " + m.Subject` interpolated the
// subject raw, so a CRLF inside it appended arbitrary headers — a subject of
// "Invoice\r\nBcc: attacker@example.com" silently BCC'd the attacker. Every
// header value now goes through [emailHeaderValue].
//
// # Structure
//
//	no attachment, one body      → a flat message with that body's type
//	no attachment, both bodies   → multipart/alternative
//	attachments, one body        → multipart/mixed { body, attachment… }
//	attachments, both bodies     → multipart/mixed { alternative, attachment… }
//
// Attachments are base64 in 76-column lines, which is what makes arbitrary
// bytes survive: the payload in the tests contains a CRLF, a lone `.` on its
// own line (the SMTP dot-stuffing trap) and a byte outside ASCII.
//
// Non-ASCII header values are RFC 2047 encoded (`=?utf-8?q?…?=`); a non-ASCII
// filename additionally goes out as an RFC 2231 `filename*` parameter, which
// `mime.FormatMediaType` produces for us.
package rt

import (
	"encoding/base64"
	"mime"
	"strings"
)

// emailDefaultAttachmentType is what an attachment with no declared `mimeType`
// is sent as. `Std.Email.defaultAttachment` leaves it "", and a MIME part with
// no Content-Type is interpreted as text/plain — which would corrupt a PDF.
const emailDefaultAttachmentType = "application/octet-stream"

// emailHeaderValue makes a value safe to place after `Name: `.
//
// Two jobs, in order:
//  1. Strip CR and LF. A newline in a header value ENDS the header and starts
//     another one, which is how a subject smuggled a `Bcc:`. Folding is not
//     attempted — a header that needs folding is rare here, and a wrong fold is
//     another injection.
//  2. RFC 2047 encode anything outside ASCII, so `Übersicht` survives a
//     7-bit-clean relay instead of arriving as mojibake.
func emailHeaderValue(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(s)
	return mime.QEncoding.Encode("utf-8", s)
}

// emailAddressHeader joins addresses for a To/Cc/Reply-To header. An address is
// stripped of CR/LF but never RFC 2047 encoded — an encoded-word in an address
// header is not a valid address.
func emailAddressHeader(addrs []string) string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		a = strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(a)
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return strings.Join(out, ", ")
}

// emailBoundary returns a MIME boundary that cannot appear inside base64 or
// inside a typical text body.
func emailBoundary(tag string) string {
	return "--=_sky_" + tag + "_" + emailGenID()
}

// buildEmailMIME renders the whole message: headers, bodies, attachments.
//
// The SAME bytes go to SMTP (`smtp.SendMail`) and to SES's `Content.Raw.Data`,
// so the two transports cannot drift apart in what they carry.
func buildEmailMIME(m emailMsg) []byte {
	var b strings.Builder
	writeHeader := func(name, value string) {
		if value == "" {
			return
		}
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteString("\r\n")
	}
	writeHeader("From", emailAddressHeader([]string{m.From}))
	writeHeader("To", emailAddressHeader(m.To))
	writeHeader("Cc", emailAddressHeader(m.Cc))
	writeHeader("Reply-To", emailAddressHeader([]string{m.ReplyTo}))
	writeHeader("Subject", emailHeaderValue(m.Subject))
	b.WriteString("MIME-Version: 1.0\r\n")

	bodyParts := emailBodyParts(m)
	switch {
	case len(m.Attachments) > 0:
		mixed := emailBoundary("mixed")
		b.WriteString("Content-Type: multipart/mixed; boundary=\"" + mixed + "\"\r\n\r\n")
		b.WriteString("--" + mixed + "\r\n")
		writeEmailBody(&b, bodyParts)
		for _, a := range m.Attachments {
			b.WriteString("\r\n--" + mixed + "\r\n")
			writeEmailAttachment(&b, a)
		}
		b.WriteString("\r\n--" + mixed + "--\r\n")
	default:
		writeEmailBody(&b, bodyParts)
	}
	return []byte(b.String())
}

// emailBodyPart is one renderable body: a content type and its text.
type emailBodyPart struct {
	contentType string
	text        string
}

func emailBodyParts(m emailMsg) []emailBodyPart {
	var parts []emailBodyPart
	if m.TextBody != "" {
		parts = append(parts, emailBodyPart{"text/plain; charset=UTF-8", m.TextBody})
	}
	if m.HtmlBody != "" {
		parts = append(parts, emailBodyPart{"text/html; charset=UTF-8", m.HtmlBody})
	}
	if len(parts) == 0 {
		// A message with neither body is still a valid message; sending no
		// Content-Type at all would make the receiver guess.
		parts = append(parts, emailBodyPart{"text/plain; charset=UTF-8", ""})
	}
	return parts
}

// writeEmailBody writes the body section — headers included, so the caller can
// drop it straight into a multipart/mixed part or use it as the whole message.
func writeEmailBody(b *strings.Builder, parts []emailBodyPart) {
	if len(parts) == 1 {
		b.WriteString("Content-Type: " + parts[0].contentType + "\r\n\r\n")
		b.WriteString(parts[0].text)
		return
	}
	// Both a text and an HTML body: multipart/alternative, least-rich FIRST,
	// which is the order that tells a client the HTML is the preferred one.
	alt := emailBoundary("alt")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + alt + "\"\r\n\r\n")
	for _, p := range parts {
		b.WriteString("--" + alt + "\r\n")
		b.WriteString("Content-Type: " + p.contentType + "\r\n\r\n")
		b.WriteString(p.text)
		b.WriteString("\r\n")
	}
	b.WriteString("--" + alt + "--\r\n")
}

func writeEmailAttachment(b *strings.Builder, a emailAttachment) {
	ct := a.MimeType
	if ct == "" {
		ct = emailDefaultAttachmentType
	}
	name := strings.NewReplacer("\r", "", "\n", "", "\x00", "", `"`, "").Replace(a.Filename)
	// FormatMediaType quotes, and RFC 2231 encodes a non-ASCII name into a
	// `filename*=utf-8''…` parameter that `mime.ParseMediaType` reads back.
	typeHeader := mime.FormatMediaType(ct, map[string]string{"name": name})
	if typeHeader == "" {
		typeHeader = emailDefaultAttachmentType
	}
	dispHeader := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if dispHeader == "" {
		dispHeader = "attachment"
	}
	b.WriteString("Content-Type: " + typeHeader + "\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: " + dispHeader + "\r\n\r\n")
	b.WriteString(emailBase64Lines(a.Content))
}

// emailBase64Lines base64-encodes and wraps at 76 columns (RFC 2045 caps a line
// at 76 characters; some relays truncate or refuse longer ones).
func emailBase64Lines(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for i := 0; i < len(enc); i += 76 {
		end := i + 76
		if end > len(enc) {
			end = len(enc)
		}
		b.WriteString(enc[i:end])
		b.WriteString("\r\n")
	}
	return b.String()
}

// emailAttachmentsJSON renders the attachment list in the shape the HTTP
// providers expect. `contentKey` differs between them and so does whether the
// type parameter is wanted, so each caller passes its own field names — but the
// base64 body and the default MIME type are shared, which is the part that was
// getting each provider wrong in a different way.
func emailAttachmentsJSON(atts []emailAttachment, typeKey, dispositionValue string) []map[string]any {
	out := make([]map[string]any, 0, len(atts))
	for _, a := range atts {
		ct := a.MimeType
		if ct == "" {
			ct = emailDefaultAttachmentType
		}
		e := map[string]any{
			"filename": a.Filename,
			"content":  base64.StdEncoding.EncodeToString([]byte(a.Content)),
		}
		if typeKey != "" {
			e[typeKey] = ct
		}
		if dispositionValue != "" {
			e["disposition"] = dispositionValue
		}
		out = append(out, e)
	}
	return out
}
