package rt

// Scroll RPC sideband: data only. Collection never executes Tasks/toMsg or
// renders, and application only reconstructs the ordinary post-render Cmd.
import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const spaScrollWireVersion = 1
const spaScrollWireProblem = "SpaScrollWire: malformed returned scroll command"

type spaScrollWire struct {
	Version  int         `json:"version"`
	Commands []scrollCmd `json:"commands"`
}

// Spa_collectScrolls walks the ACTUAL returned command, including opaque helper
// results. Preserve depth-first batch order; never run a perform/task/toMsg.
func Spa_collectScrolls(cmd any) string {
	commands := make([]scrollCmd, 0)
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
		case "scroll":
			s, ok := scrollCmdOf(ct)
			if !ok || (s.Top && (s.Target != "" || s.IfNeeded)) {
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
	case "version", "commands", "target", "top", "ifNeeded", "followUps", "scrolls":
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
	type item struct {
		Target   *string `json:"target"`
		Top      *bool   `json:"top"`
		IfNeeded *bool   `json:"ifNeeded"`
	}
	var value struct {
		Version  *int   `json:"version"`
		Commands []item `json:"commands"`
	}
	if spaScrollJSON(wire, &value) != nil || value.Version == nil || *value.Version != spaScrollWireVersion || value.Commands == nil {
		return Cmd_none(), errors.New(spaScrollWireProblem)
	}
	batch := make([]any, 0, len(value.Commands))
	for _, x := range value.Commands {
		if x.Target == nil || x.Top == nil || x.IfNeeded == nil || (*x.Top && (*x.Target != "" || *x.IfNeeded)) {
			return Cmd_none(), errors.New(spaScrollWireProblem)
		}
		batch = append(batch, cmdT{kind: "scroll", payload: scrollCmd{Target: *x.Target, Top: *x.Top, IfNeeded: *x.IfNeeded}})
	}
	return cmdT{kind: "batch", batch: batch}, nil
}

// Spa_scrollCommands never applies DOM work itself. A malformed response is a
// loud framework error Cmd, so the already-applied model delta is preserved.
type spaReturnedScrolls struct{}

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
