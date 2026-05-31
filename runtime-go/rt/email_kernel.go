// Package rt — Std.Email runtime kernels.
//
// v0.15.47 stdlib batch (#380): provider-abstract email send for
// Resend, AWS SES, SendGrid, and raw SMTP.
//
// Production-mode networking is real: Resend, SendGrid, SES (REST
// API v2) over HTTPS; SMTP via Go's net/smtp.
//
// Test mode (SKY_EMAIL_DRY_RUN=1) short-circuits every provider
// and returns a synthetic ID — used in CI and the Sky.Test
// fixtures so tests don't depend on third-party services.
package rt

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"os"
	"reflect"
	"strings"
	"time"
)

// Email_send implements:
//
//	Std.Email.send : EmailProvider -> EmailMessage -> Task Error String
func Email_send(providerArg, msgArg any) any {
	return func() any {
		if os.Getenv("SKY_EMAIL_DRY_RUN") == "1" {
			return Ok[any, any]("dry-run-" + emailGenID())
		}
		msg := readEmailMessage(msgArg)
		if msg.From == "" {
			return Err[any, any](ErrInvalidInput("email.send: from required"))
		}
		if len(msg.To) == 0 {
			return Err[any, any](ErrInvalidInput("email.send: at least one recipient required"))
		}

		// Provider is a Sky ADT — Resend/Ses/SendGrid/Smtp.
		tag, args := readEmailProvider(providerArg)
		switch tag {
		case "Resend":
			return sendResend(strOf(args, 0), msg)
		case "SendGrid":
			return sendSendGrid(strOf(args, 0), msg)
		case "Ses":
			return sendSes(args[0], msg)
		case "Smtp":
			return sendSmtp(args[0], msg)
		default:
			return Err[any, any](ErrInvalidInput("email.send: unknown provider tag " + tag))
		}
	}
}

// emailMsg mirrors the Sky-side EmailMessage record.
type emailMsg struct {
	From, Subject, TextBody, HtmlBody, ReplyTo string
	To, Cc, Bcc                                []string
	Attachments                                []emailAttachment
}

type emailAttachment struct {
	Filename, MimeType, Content string
}

func readEmailMessage(arg any) emailMsg {
	return emailMsg{
		From:        fmt.Sprintf("%v", recordField(arg, "From", "from")),
		Subject:     fmt.Sprintf("%v", recordField(arg, "Subject", "subject")),
		TextBody:    fmt.Sprintf("%v", recordField(arg, "TextBody", "textBody")),
		HtmlBody:    fmt.Sprintf("%v", recordField(arg, "HtmlBody", "htmlBody")),
		ReplyTo:     fmt.Sprintf("%v", recordField(arg, "ReplyTo", "replyTo")),
		To:          asStringList(recordField(arg, "To", "to")),
		Cc:          asStringList(recordField(arg, "Cc", "cc")),
		Bcc:         asStringList(recordField(arg, "Bcc", "bcc")),
		Attachments: readAttachments(recordField(arg, "Attachments", "attachments")),
	}
}

func asStringList(v any) []string {
	items := AsList(v)
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = fmt.Sprintf("%v", it)
	}
	return out
}

func readAttachments(v any) []emailAttachment {
	items := AsList(v)
	out := make([]emailAttachment, len(items))
	for i, it := range items {
		out[i] = emailAttachment{
			Filename: fmt.Sprintf("%v", recordField(it, "Filename", "filename")),
			MimeType: fmt.Sprintf("%v", recordField(it, "MimeType", "mimeType")),
			Content:  asBytesString(recordField(it, "Content", "content")),
		}
	}
	return out
}

// readEmailProvider extracts the ADT tag + args.
func readEmailProvider(v any) (string, []any) {
	// SkyADT with Tag + Fields layout
	if a, ok := v.(SkyADT); ok {
		// Prefer SkyName when populated (codegen sets it); fall back
		// to a numeric tag mapping for runtime-constructed values.
		if a.SkyName != "" {
			return a.SkyName, a.Fields
		}
		return emailProviderTagName(a.Tag), a.Fields
	}
	// Reflect fallback: any struct with .Tag/.SkyName/.Fields.
	return reflectExtractCtor(v)
}

// reflectExtractCtor uses reflect to pull (SkyName, Fields) off any
// ADT-shaped struct value. Used for foreign-typed values we don't
// statically know about.
func reflectExtractCtor(v any) (string, []any) {
	if v == nil {
		return "", nil
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "", nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() || rv.Kind() != reflect.Struct {
		return "", nil
	}
	skyName := ""
	if f := rv.FieldByName("SkyName"); f.IsValid() && f.Kind() == reflect.String {
		skyName = f.String()
	}
	var fields []any
	if f := rv.FieldByName("Fields"); f.IsValid() && f.Kind() == reflect.Slice {
		fields = make([]any, f.Len())
		for i := 0; i < f.Len(); i++ {
			fields[i] = f.Index(i).Interface()
		}
	}
	if skyName == "" {
		if f := rv.FieldByName("Tag"); f.IsValid() && f.CanInt() {
			skyName = emailProviderTagName(int(f.Int()))
		}
	}
	return skyName, fields
}

// emailProviderTagName maps the integer Tag from a SkyADT back to
// the constructor name. The ordering follows the Sky-side declaration
// order: Resend | Ses | SendGrid | Smtp.
func emailProviderTagName(tag int) string {
	switch tag {
	case 0:
		return "Resend"
	case 1:
		return "Ses"
	case 2:
		return "SendGrid"
	case 3:
		return "Smtp"
	default:
		return ""
	}
}

func strOf(args []any, i int) string {
	if i < 0 || i >= len(args) {
		return ""
	}
	return fmt.Sprintf("%v", args[i])
}

func emailGenID() string {
	b := [8]byte{}
	now := time.Now().UnixNano()
	for i := 0; i < 8; i++ {
		b[i] = byte(now >> (i * 8))
	}
	return hex.EncodeToString(b[:])
}

// ──────────────────── Resend ────────────────────

func sendResend(apiKey string, m emailMsg) any {
	if apiKey == "" {
		return Err[any, any](ErrInvalidInput("email.send/Resend: empty API key"))
	}
	body := map[string]any{
		"from":    m.From,
		"to":      m.To,
		"subject": m.Subject,
	}
	if len(m.Cc) > 0 {
		body["cc"] = m.Cc
	}
	if len(m.Bcc) > 0 {
		body["bcc"] = m.Bcc
	}
	if m.TextBody != "" {
		body["text"] = m.TextBody
	}
	if m.HtmlBody != "" {
		body["html"] = m.HtmlBody
	}
	if m.ReplyTo != "" {
		body["reply_to"] = m.ReplyTo
	}
	if len(m.Attachments) > 0 {
		atts := make([]map[string]any, 0, len(m.Attachments))
		for _, a := range m.Attachments {
			atts = append(atts, map[string]any{
				"filename": a.Filename,
				"content":  []byte(a.Content),
			})
		}
		body["attachments"] = atts
	}
	resp, err := emailDoJSON("POST", emailEndpoint("resend", "https://api.resend.com/emails"),
		map[string]string{
			"Authorization": "Bearer " + apiKey,
			"Content-Type":  "application/json",
		}, body)
	if err != nil {
		return Err[any, any](ErrNetwork("email.send/Resend: " + err.Error()))
	}
	id, _ := resp["id"].(string)
	if id == "" {
		id = "resend-" + emailGenID()
	}
	return Ok[any, any](id)
}

// ──────────────────── SendGrid ────────────────────

func sendSendGrid(apiKey string, m emailMsg) any {
	if apiKey == "" {
		return Err[any, any](ErrInvalidInput("email.send/SendGrid: empty API key"))
	}
	personalisations := []map[string]any{
		{
			"to": addrList(m.To),
		},
	}
	if len(m.Cc) > 0 {
		personalisations[0]["cc"] = addrList(m.Cc)
	}
	if len(m.Bcc) > 0 {
		personalisations[0]["bcc"] = addrList(m.Bcc)
	}
	content := []map[string]any{}
	if m.TextBody != "" {
		content = append(content, map[string]any{"type": "text/plain", "value": m.TextBody})
	}
	if m.HtmlBody != "" {
		content = append(content, map[string]any{"type": "text/html", "value": m.HtmlBody})
	}
	body := map[string]any{
		"personalizations": personalisations,
		"from":             map[string]string{"email": m.From},
		"subject":          m.Subject,
		"content":          content,
	}
	if m.ReplyTo != "" {
		body["reply_to"] = map[string]string{"email": m.ReplyTo}
	}
	_, err := emailDoJSON("POST", emailEndpoint("sendgrid", "https://api.sendgrid.com/v3/mail/send"),
		map[string]string{
			"Authorization": "Bearer " + apiKey,
			"Content-Type":  "application/json",
		}, body)
	if err != nil {
		return Err[any, any](ErrNetwork("email.send/SendGrid: " + err.Error()))
	}
	// SendGrid returns 202 with no body; synthesise an id.
	return Ok[any, any]("sendgrid-" + emailGenID())
}

func addrList(xs []string) []map[string]string {
	out := make([]map[string]string, len(xs))
	for i, x := range xs {
		out[i] = map[string]string{"email": x}
	}
	return out
}

// ──────────────────── SES (v2) ────────────────────

func sendSes(cfgArg any, m emailMsg) any {
	region := fmt.Sprintf("%v", recordField(cfgArg, "Region", "region"))
	key := fmt.Sprintf("%v", recordField(cfgArg, "Key", "key"))
	secret := fmt.Sprintf("%v", recordField(cfgArg, "Secret", "secret"))
	if region == "" || key == "" || secret == "" {
		return Err[any, any](ErrInvalidInput("email.send/Ses: region+key+secret required"))
	}
	body := map[string]any{
		"FromEmailAddress": m.From,
		"Destination": map[string]any{
			"ToAddresses": m.To,
		},
		"Content": map[string]any{
			"Simple": map[string]any{
				"Subject": map[string]any{"Data": m.Subject, "Charset": "UTF-8"},
				"Body": map[string]any{
					"Text": map[string]any{"Data": m.TextBody, "Charset": "UTF-8"},
				},
			},
		},
	}
	if m.HtmlBody != "" {
		simple := body["Content"].(map[string]any)["Simple"].(map[string]any)
		bodyMap := simple["Body"].(map[string]any)
		bodyMap["Html"] = map[string]any{"Data": m.HtmlBody, "Charset": "UTF-8"}
	}
	if len(m.Cc) > 0 {
		body["Destination"].(map[string]any)["CcAddresses"] = m.Cc
	}
	if len(m.Bcc) > 0 {
		body["Destination"].(map[string]any)["BccAddresses"] = m.Bcc
	}

	host := "email." + region + ".amazonaws.com"
	payload, _ := json.Marshal(body)
	headers := sesSignV4(host, region, key, secret, payload)
	endpoint := emailEndpoint("ses", "https://"+host+"/v2/email/outbound-emails")
	_, err := emailDoJSONRaw("POST", endpoint, headers, payload)
	if err != nil {
		return Err[any, any](ErrNetwork("email.send/Ses: " + err.Error()))
	}
	return Ok[any, any]("ses-" + emailGenID())
}

// sesSignV4 produces AWS Sig-v4 headers for the SES v2 sendEmail call.
// Minimal implementation — enough to authenticate against `ses` v2.
func sesSignV4(host, region, key, secret string, payload []byte) map[string]string {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	canonicalRequest := strings.Join([]string{
		"POST",
		"/v2/email/outbound-emails",
		"", // no query string
		"content-type:application/json",
		"host:" + host,
		"x-amz-date:" + amzDate,
		"", // canonical headers terminator
		"content-type;host;x-amz-date",
		hexSha256(payload),
	}, "\n")

	credentialScope := strings.Join([]string{dateStamp, region, "ses", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hexSha256([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacBytes([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacBytes(kDate, []byte(region))
	kService := hmacBytes(kRegion, []byte("ses"))
	kSigning := hmacBytes(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacBytes(kSigning, []byte(stringToSign)))

	authHeader := "AWS4-HMAC-SHA256 " +
		"Credential=" + key + "/" + credentialScope + ", " +
		"SignedHeaders=content-type;host;x-amz-date, " +
		"Signature=" + signature
	return map[string]string{
		"Content-Type":  "application/json",
		"Host":          host,
		"X-Amz-Date":    amzDate,
		"Authorization": authHeader,
	}
}

func hexSha256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacBytes(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

// ──────────────────── SMTP ────────────────────

func sendSmtp(cfgArg any, m emailMsg) any {
	host := fmt.Sprintf("%v", recordField(cfgArg, "Host", "host"))
	port := AsInt(recordField(cfgArg, "Port", "port"))
	user := fmt.Sprintf("%v", recordField(cfgArg, "User", "user"))
	pass := fmt.Sprintf("%v", recordField(cfgArg, "Pass", "pass"))
	if host == "" || port == 0 {
		return Err[any, any](ErrInvalidInput("email.send/Smtp: host+port required"))
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	var auth smtp.Auth
	if user != "" || pass != "" {
		auth = smtp.PlainAuth("", user, pass, host)
	}
	recipients := append([]string{}, m.To...)
	recipients = append(recipients, m.Cc...)
	recipients = append(recipients, m.Bcc...)

	headers := []string{
		"From: " + m.From,
		"To: " + strings.Join(m.To, ", "),
	}
	if len(m.Cc) > 0 {
		headers = append(headers, "Cc: "+strings.Join(m.Cc, ", "))
	}
	if m.ReplyTo != "" {
		headers = append(headers, "Reply-To: "+m.ReplyTo)
	}
	headers = append(headers, "Subject: "+m.Subject)
	bodyText := m.TextBody
	if bodyText == "" && m.HtmlBody != "" {
		headers = append(headers, "MIME-Version: 1.0")
		headers = append(headers, "Content-Type: text/html; charset=UTF-8")
		bodyText = m.HtmlBody
	}
	wire := strings.Join(headers, "\r\n") + "\r\n\r\n" + bodyText

	if err := smtp.SendMail(addr, auth, m.From, recipients, []byte(wire)); err != nil {
		return Err[any, any](ErrNetwork("email.send/Smtp: " + err.Error()))
	}
	return Ok[any, any]("smtp-" + emailGenID())
}

// ──────────────────── HTTP helpers ────────────────────

func emailDoJSON(method, url string, headers map[string]string, body any) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return emailDoJSONRaw(method, url, headers, payload)
}

func emailDoJSONRaw(method, url string, headers map[string]string, payload []byte) (map[string]any, error) {
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}
	out := map[string]any{}
	_ = json.Unmarshal(respBody, &out)
	return out, nil
}

// emailEndpoint allows tests to override per-provider URLs via env.
//
//	SKY_EMAIL_ENDPOINT_RESEND=http://127.0.0.1:9000/emails
func emailEndpoint(provider, def string) string {
	env := "SKY_EMAIL_ENDPOINT_" + strings.ToUpper(provider)
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}
