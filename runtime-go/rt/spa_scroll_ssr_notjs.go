//go:build !js

package rt

import (
	"encoding/json"
	"html"
	"strings"
)

// Remove only scroll leaves, without executing any command. Iterative batch
// frames avoid introducing a new recursion limit; non-scroll leaves are exact
// original values. The collected order is the original settle visitation order.
func spaStripScrolls(value any) (any, []spaBrowserCommand, bool) {
	type frame struct {
		value   any
		cmd     cmdT
		entered bool
		next    int
		out     []any
		changed bool
	}
	stack := []frame{{value: value}}
	var result any
	out := []spaBrowserCommand{}
	invalid := false
	finish := func(v any, changed bool) {
		stack = stack[:len(stack)-1]
		if len(stack) == 0 {
			result = v
			return
		}
		f := &stack[len(stack)-1]
		f.out = append(f.out, v)
		f.changed = f.changed || changed
	}
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		if !f.entered {
			c, ok := f.value.(cmdT)
			if !ok {
				finish(f.value, false)
				continue
			}
			f.cmd = c
			f.entered = true
			switch c.kind {
			case "scroll", "nav":
				s, ok := spaBrowserLeaf(c)
				if !ok {
					invalid = true
				} else {
					out = append(out, s)
				}
				finish(Cmd_none(), true)
				continue
			case "batch":
			default:
				finish(f.value, false)
				continue
			}
		}
		if f.next < len(f.cmd.batch) {
			v := f.cmd.batch[f.next]
			f.next++
			stack = append(stack, frame{value: v})
			continue
		}
		if !f.changed {
			finish(f.value, false)
			continue
		}
		c := f.cmd
		c.batch = f.out
		finish(c, true)
	}
	return result, out, invalid
}
func spaScrollsJSON(commands []spaBrowserCommand, invalid bool) string {
	if invalid {
		return `{"version":0,"commands":[]}`
	}
	if commands == nil {
		commands = []spaBrowserCommand{}
	}
	b, err := json.Marshal(spaScrollWire{spaScrollWireVersion, commands})
	if err != nil {
		return `{"version":0,"commands":[]}`
	}
	return string(b)
}

// Spa_ssrSettleFullScrolls invokes the ORIGINAL SSR settler exactly once. The
// wrapper update invokes the ORIGINAL update once per original continuation.
// Other unfinished commands still make full=false. Publish boot scrolls only
// for a fully settled group; otherwise the original client group still owns
// them, so publishing here as well would duplicate an effect.
func Spa_ssrSettleFullScrolls(model, cmd, update any) any {
	stripped, commands, invalid := spaStripScrolls(cmd)
	wrapped := func(msg, model any) any {
		pair := SkyCall(update, msg, model)
		m, okM := tupleFirstField(pair)
		c, okC := tupleSecondField(pair)
		if !okM || !okC {
			return pair
		}
		clean, more, bad := spaStripScrolls(c)
		commands = append(commands, more...)
		invalid = invalid || bad
		return T2[any, any]{V0: m, V1: clean}
	}
	pair := Spa_ssrSettleFull(model, stripped, wrapped)
	m, okM := tupleFirstField(pair)
	done, okDone := tupleSecondField(pair)
	if !okM || !okDone {
		panic("Codec.auto: cannot encode SSR scroll settlement")
	}
	full := AsBool(done)
	wire := ""
	if full {
		wire = spaScrollsJSON(commands, invalid)
	}
	return T3[any, bool, string]{V0: m, V1: full, V2: wire}
}

// Combine independent FULLY settled init/request/nav groups in actual order.
func Spa_joinScrolls(wires any) string {
	commands := []spaBrowserCommand{}
	for _, v := range spaAsListPortable(wires) {
		text := AsString(v)
		if text == "" {
			continue
		}
		cmd, err := spaDecodeScrolls(text)
		if err != nil {
			return `{"version":0,"commands":[]}`
		}
		for _, leaf := range cmd.batch {
			c, ok := leaf.(cmdT)
			if !ok {
				return `{"version":0,"commands":[]}`
			}
			s, ok := spaBrowserLeaf(c)
			if !ok {
				return `{"version":0,"commands":[]}`
			}
			commands = append(commands, s)
		}
	}
	return spaScrollsJSON(commands, false)
}
func spaAsListPortable(v any) []any { return AsList(v) }

// Only the framework #app receives the one-shot attribute. Build without app
// head markup first, so even a head/body value containing the marker cannot
// redirect the insertion. Reinsert head at its exact original framework slot.
func Spa_ssrPageSeededScrolls(head, body, wasmName, modelJSON, initDone, navDone, seedFields, scrollWire any) string {
	if AsString(scrollWire) == "" {
		return Spa_ssrPageSeeded(head, body, wasmName, modelJSON, initDone, navDone, seedFields)
	}
	page := Spa_ssrPageSeeded("", body, wasmName, modelJSON, initDone, navDone, seedFields)
	root := "</head>\n<body><div id=\"app\" "
	index := strings.Index(page, root)
	if index < 0 {
		panic("Codec.auto: cannot encode SSR scroll root")
	}
	at := index + len(root)
	page = page[:at] + `data-sky-scrolls="` + html.EscapeString(AsString(scrollWire)) + `" ` + page[at:]
	viewport := `<meta name="viewport" content="width=device-width, initial-scale=1">`
	if !strings.Contains(page, viewport) {
		panic("Codec.auto: cannot encode SSR scroll head")
	}
	return strings.Replace(page, viewport, viewport+AsString(head), 1)
}
