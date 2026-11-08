package rt

// Scroll RPC sideband: data only. Collection never executes Tasks/toMsg or
// renders, and application only reconstructs the ordinary post-render Cmd.
import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const spaScrollWireVersion = 2
const spaScrollWireProblem = "SpaScrollWire: malformed returned scroll command"

type spaScrollWire struct {
	Version  int                 `json:"version"`
	Commands []spaBrowserCommand `json:"commands"`
}

// spaBrowserCommand is a closed, data-only wire union. Pointers preserve
// required false/empty values while rejecting fields belonging to another kind.
type spaBrowserCommand struct {
	Kind     string  `json:"kind"`
	Target   *string `json:"target,omitempty"`
	Top      *bool   `json:"top,omitempty"`
	IfNeeded *bool   `json:"ifNeeded,omitempty"`
	URL      *string `json:"url,omitempty"`
	Replace  *bool   `json:"replace,omitempty"`
}

func spaBrowserLeaf(c cmdT) (spaBrowserCommand, bool) {
	switch c.kind {
	case "scroll":
		v, ok := scrollCmdOf(c)
		if !ok || (v.Top && (v.Target != "" || v.IfNeeded)) {
			return spaBrowserCommand{}, false
		}
		return spaBrowserCommand{Kind: "scroll", Target: &v.Target, Top: &v.Top, IfNeeded: &v.IfNeeded}, true
	case "nav":
		v, ok := navCmdOf(c)
		if !ok || navTargetError(v.URL) != "" {
			return spaBrowserCommand{}, false
		}
		return spaBrowserCommand{Kind: "nav", URL: &v.URL, Replace: &v.Replace}, true
	}
	return spaBrowserCommand{}, false
}
func (v spaBrowserCommand) command() (cmdT, bool) {
	switch v.Kind {
	case "scroll":
		if v.Target == nil || v.Top == nil || v.IfNeeded == nil || v.URL != nil || v.Replace != nil || (*v.Top && (*v.Target != "" || *v.IfNeeded)) {
			return Cmd_none(), false
		}
		return cmdT{kind: "scroll", payload: scrollCmd{Target: *v.Target, Top: *v.Top, IfNeeded: *v.IfNeeded}}, true
	case "nav":
		if v.URL == nil || v.Replace == nil || v.Target != nil || v.Top != nil || v.IfNeeded != nil || navTargetError(*v.URL) != "" {
			return Cmd_none(), false
		}
		return cmdT{kind: "nav", payload: navCmd{URL: *v.URL, Replace: *v.Replace}}, true
	}
	return Cmd_none(), false
}

// Spa_collectScrolls walks the ACTUAL returned command, including opaque helper
// results. Preserve depth-first batch order; never run a perform/task/toMsg.
func Spa_collectScrolls(cmd any) string {
	commands := make([]spaBrowserCommand, 0)
	stack := []any{cmd}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		ct, ok := c.(cmdT)
		if !ok {
			continue
		}
		switch ct.kind {
		case "batch":
			for i := len(ct.batch) - 1; i >= 0; i-- {
				stack = append(stack, ct.batch[i])
			}
		case "scroll", "nav":
			s, ok := spaBrowserLeaf(ct)
			if !ok {
				return `{"version":0,"commands":[]}`
			}
			commands = append(commands, s)
		}
	}
	b, err := json.Marshal(spaScrollWire{spaScrollWireVersion, commands})
	if err != nil {
		return `{"version":0,"commands":[]}`
	}
	return string(b)
}

// Reject duplicate object keys as well as unknown fields/trailing data. This
// parser consumes only JSON primitives and never invokes a user method.
func spaScrollWireKey(key string) bool {
	switch key {
	case "version", "commands", "kind", "url", "replace", "target", "top", "ifNeeded", "followUps", "scrolls":
		return true
	}
	return false
}
func spaScrollJSON(data string, target any) error {
	d := json.NewDecoder(bytes.NewBufferString(data))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 16 {
			return errors.New(spaScrollWireProblem)
		}
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || !spaScrollWireKey(s) || seen[s] {
					return errors.New(spaScrollWireProblem)
				}
				seen[s] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := walk(depth + 1); e != nil {
					return e
				}
			}
		default:
			return errors.New(spaScrollWireProblem)
		}
		_, e = d.Token()
		return e
	}
	if e := walk(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New(spaScrollWireProblem)
	}
	strict := json.NewDecoder(bytes.NewBufferString(data))
	strict.DisallowUnknownFields()
	if e := strict.Decode(target); e != nil {
		return e
	}
	return nil
}
func spaDecodeScrolls(wire string) (cmdT, error) {
	// Missing sideband from a pre-scroll backend is the unchanged no-command.
	if wire == "" {
		return Cmd_none(), nil
	}
	var value struct {
		Version  *int              `json:"version"`
		Commands []json.RawMessage `json:"commands"`
	}
	if spaScrollJSON(wire, &value) != nil || value.Version == nil || *value.Version != spaScrollWireVersion || value.Commands == nil {
		return Cmd_none(), errors.New(spaScrollWireProblem)
	}
	batch := make([]any, 0, len(value.Commands))
	for _, raw := range value.Commands {
		var x spaBrowserCommand
		var fields map[string]json.RawMessage
		if spaScrollJSON(string(raw), &x) != nil || json.Unmarshal(raw, &fields) != nil {
			return Cmd_none(), errors.New(spaScrollWireProblem)
		}
		expected := 4
		if x.Kind == "nav" {
			expected = 3
		}
		if len(fields) != expected {
			return Cmd_none(), errors.New(spaScrollWireProblem)
		}
		c, ok := x.command()
		if !ok {
			return Cmd_none(), errors.New(spaScrollWireProblem)
		}
		batch = append(batch, c)
	}

	return cmdT{kind: "batch", batch: batch}, nil
}

// Spa_scrollCommands never applies DOM work itself. A malformed response is a
// loud framework error Cmd, so the already-applied model delta is preserved.
type spaReturnedScrolls struct{}

// Client-only postcommit authority; never serialized.
type spaReturnedBrowserOrigin struct{ authority uint64 }

func spaIsReturnedScrolls(command cmdT) bool {
	_, ok := command.payload.(spaReturnedScrolls)
	return command.kind == "batch" && ok
}

func Spa_scrollCommands(wire any) SkyCmd {
	c, err := spaDecodeScrolls(AsString(wire))
	if err != nil {
		return cmdT{kind: "spaError", payload: ErrDecode(spaScrollWireProblem)}
	}
	if c.kind == "batch" {
		c.payload = spaReturnedScrolls{}
	}
	return c
}

type spaInitScrollWire struct {
	Version   int    `json:"version"`
	FollowUps string `json:"followUps"`
	Scrolls   string `json:"scrolls"`
}

// The outer init RPC remains String, but its inner version is included in the
// generated Shared wire hash. Old clients therefore reload via the409 gate.
func Spa_initScrollResponse(cmd, follows any) string {
	b, err := json.Marshal(spaInitScrollWire{spaScrollWireVersion, AsString(follows), Spa_collectScrolls(cmd)})
	if err != nil {
		return `{"version":0}`
	}
	return string(b)
}
func Spa_readInitScrolls(wire any) any {
	raw := AsString(wire)
	// A new client can still read the old backend's plain follow-up JSON. It is
	// validated by the existing typed follow-up decoder, not interpreted here.
	if b := bytes.TrimSpace([]byte(raw)); len(b) > 0 && b[0] == '[' && json.Valid(b) {
		return Ok[any, any](T2[string, string]{V0: raw, V1: ""})
	}
	var value struct {
		Version   *int    `json:"version"`
		FollowUps *string `json:"followUps"`
		Scrolls   *string `json:"scrolls"`
	}
	if spaScrollJSON(raw, &value) != nil || value.Version == nil || *value.Version != spaScrollWireVersion || value.FollowUps == nil || value.Scrolls == nil {
		return Err[any, any](ErrDecode(spaScrollWireProblem))
	}
	if _, err := spaDecodeScrolls(*value.Scrolls); err != nil {
		return Err[any, any](ErrDecode(spaScrollWireProblem))
	}
	return Ok[any, any](T2[string, string]{V0: *value.FollowUps, V1: *value.Scrolls})
}

// spaScrollRpcHold is a private scheduler marker, never part of RPC JSON or
// model data. Marking preserves the original request and toMsg closures.
type spaScrollRpcHold struct{}

func Spa_scrollRpcHold(command any) SkyCmd {
	c, ok := command.(cmdT)
	if !ok || c.kind != "rpc" {
		return cmdT{kind: "spaError", payload: ErrDecode("SpaScrollWire: invalid RPC barrier")}
	}
	c.payload = spaScrollRpcHold{}
	return c
}

func spaRpcHoldFlags(payload any) (hold, dom bool) {
	if _, ok := payload.(spaScrollRpcHold); ok {
		return true, true
	}
	hold, _ = payload.(bool)
	return hold, false
}

// Spa_scrollRpcScope is data only: the client interpreter records origin at
// execution, before the original ordered residual/RPC batch runs.
func Spa_scrollRpcScope(command any) SkyCmd {
	c, ok := command.(cmdT)
	if !ok {
		return cmdT{kind: "spaError", payload: ErrDecode("SpaScrollWire: invalid RPC scope")}
	}
	return cmdT{kind: "scrollRpcScope", payload: c}
}

// spaReturnedLeaves flattens a returned command batch to its leaves in
// depth-first batch order (iterative: no new recursion limit).
func spaReturnedLeaves(cmd cmdT) []cmdT {
	out := []cmdT{}
	stack := []any{cmd}
	for len(stack) > 0 {
		c, ok := stack[len(stack)-1].(cmdT)
		stack = stack[:len(stack)-1]
		if !ok {
			continue
		}
		if c.kind == "batch" {
			for i := len(c.batch) - 1; i >= 0; i-- {
				stack = append(stack, c.batch[i])
			}
			continue
		}
		if c.kind != "" && c.kind != "none" {
			out = append(out, c)
		}
	}
	return out
}

func spaReturnedHasNav(leaves []cmdT) bool {
	for _, c := range leaves {
		if c.kind == "nav" {
			return true
		}
	}
	return false
}

// spaRunReturnedInOrder runs a returned batch's leaves one after another, in
// batch order, while the batch still owns the page. `expected` is the scroll
// authority the batch was accepted under; any change to it before a leaf runs
// (a newer navigation, a newer render) cancels the rest. A leaf's OWN
// navigation advances the authority by exactly one step, which the batch then
// owns; a larger change means something else intervened, so the rest stop.
func spaRunReturnedInOrder(leaves []cmdT, expected uint64, current func() uint64, run func(cmdT)) {
	for _, c := range leaves {
		if current() != expected {
			return
		}
		before := current()
		run(c)
		after := current()
		switch {
		case after == before:
		case c.kind == "nav" && after == before+1:
			expected = after
		default:
			return
		}
	}
}
