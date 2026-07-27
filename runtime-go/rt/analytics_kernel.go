// Package rt — Std.Analytics runtime kernels.
//
// P1 (capture core): render a tracked event to a structured `[analytics]`
// line on stderr — the default local/debug sink — with PII redacted. Later
// phases route events through the consent gate, context enrichment, and the
// pluggable sinks (console SQLite store + external providers). See
// docs/rfcs/std-analytics.md.
package rt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"time"
)

// Analytics_track implements:
//
//	Std.Analytics.track : Event -> Task Error ()
//
// The event is `{ name : String, props : List Prop }`, each `Prop` a
// `{ key : String, value : PropValue }`. PII props are redacted to their
// kind (`<pii:email>`) — never their value — so redaction is load-bearing
// even before the full consent pipeline lands.
func Analytics_track(eventArg any) any {
	return func() any {
		name := fmt.Sprintf("%v", recordField(eventArg, "Name", "name"))
		props := analyticsReadProps(recordField(eventArg, "Props", "props"))
		payload := map[string]any{
			"event": name,
			"props": props,
			"ts":    time.Now().UnixMilli(),
		}
		// Don't HTML-escape (`<`/`>`/`&`) — these are human-read log lines,
		// and the PII redaction marker `<pii:email>` should read cleanly.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err == nil {
			fmt.Fprint(os.Stderr, "[analytics] "+buf.String())
		}
		return Ok[any, any](struct{}{})
	}
}

// analyticsReadProps turns the Sky `List Prop` into a JSON-ready map,
// rendering each typed PropValue and redacting PII.
func analyticsReadProps(v any) map[string]any {
	out := map[string]any{}
	rv := reflect.ValueOf(derefPointer(unwrapAny(v)))
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		return out
	}
	for i := 0; i < rv.Len(); i++ {
		p := rv.Index(i).Interface()
		key := fmt.Sprintf("%v", recordField(p, "Key", "key"))
		out[key] = analyticsRenderValue(recordField(p, "Value", "value"))
	}
	return out
}

// analyticsRenderValue renders one PropValue (a Sky ADT) to a JSON value.
// PII is redacted to its kind, never its value.
func analyticsRenderValue(v any) any {
	tag, fields := reflectExtractCtor(v)
	if len(fields) == 0 {
		return nil
	}
	switch tag {
	case "VString":
		return fmt.Sprintf("%v", fields[0])
	case "VInt":
		return AsInt(fields[0])
	case "VFloat":
		return AsFloat(fields[0])
	case "VBool":
		return AsBool(fields[0])
	case "VMoney":
		// Lossless "ISO_CODE AMOUNT" (e.g. "USD 19.99") — same rendering
		// Std.Db uses for SqlMoney, so revenue round-trips exactly.
		return sqlMoneyToString(fields[0])
	case "VPii":
		pTag, _ := reflectExtractCtor(fields[0])
		return "<pii:" + analyticsPiiKind(pTag) + ">"
	default:
		return fmt.Sprintf("%v", fields[0])
	}
}

func analyticsPiiKind(tag string) string {
	switch tag {
	case "PiiEmail":
		return "email"
	case "PiiRaw":
		return "raw"
	default:
		return "pii"
	}
}
