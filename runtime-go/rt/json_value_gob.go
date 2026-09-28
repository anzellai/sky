package rt

// json_value_gob.go — gob encoding for Json `Value` (rt.JsonValue) and the
// refusal to gob-encode a Secret.
//
// # Why a Value needs its own encoder
//
// A Sky.Live model is persisted by the DB-backed session stores (sqlite,
// postgres, redis) with encoding/gob (live_store.go encodeSession). JsonValue
// keeps its tree in an UNEXPORTED field, and gob refuses a struct with no
// exported fields — so a model holding a Value (for example one produced by
// `Json.Decode.value`) could not be stored at all: the store fell back to the
// in-process copy and the session was lost on restart and invisible to other
// replicas.
//
// The encoder below writes the tree as a tagged node tree that keeps each
// leaf's exact Go shape, because the runtime reads those shapes back:
// Db_sqlOfValue switches on string / int / float64 / bool, jsonObjFields
// needs a jsonOrderedObject (key order is part of the value: Json.Encode.object
// emits keys in the order given), and a decoded number stays a json.Number so
// its exact text survives (Json.Decode.value's contract). A leaf of any other
// type is stored as its JSON text (json.RawMessage), which encodes to the same
// JSON.
//
// # Why a Secret refuses
//
// Secret redacts itself in every print / log / JSON path (secret.go). gob
// would already refuse it (no exported fields), but only by accident of its
// layout; GobEncode makes the refusal explicit and gives it a message, so no
// future field change can let a secret be written to a session store in clear.

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// init registers the kernel value types a model can hold behind an interface
// (a Sky `Json.Value` field and the Fields of a Decimal / Money ADT are
// `any`). gob's name-to-type registry is per process and the codegen boot
// list (RegisterSkyGobTypes) names only Sky-minted types, so without this a
// RESTARTED process could not decode a session its predecessor wrote, even
// though the writer registered the types while encoding.
func init() {
	gob.Register(JsonValue{})
	gob.Register(decimal.Decimal{})
}

// jsonGobNode is the gob wire form of one node of a JsonValue tree.
type jsonGobNode struct {
	K    uint8
	S    string
	I    int64
	F    float64
	B    bool
	Keys []string
	Kids []jsonGobNode
}

const (
	jgNull uint8 = iota
	jgString
	jgInt
	jgInt64
	jgFloat
	jgBool
	jgNumber  // json.Number (exact source text in S)
	jgRaw     // json.RawMessage (JSON text in S)
	jgList    // []any
	jgMap     // map[string]any (Keys sorted)
	jgOrdered // jsonOrderedObject (Keys in insertion order)
	jgValue   // a nested JsonValue (Kids[0])
)

func jsonToGobNode(v any) (jsonGobNode, error) {
	switch x := v.(type) {
	case nil:
		return jsonGobNode{K: jgNull}, nil
	case string:
		return jsonGobNode{K: jgString, S: x}, nil
	case int:
		return jsonGobNode{K: jgInt, I: int64(x)}, nil
	case int64:
		return jsonGobNode{K: jgInt64, I: x}, nil
	case float64:
		return jsonGobNode{K: jgFloat, F: x}, nil
	case bool:
		return jsonGobNode{K: jgBool, B: x}, nil
	case json.Number:
		return jsonGobNode{K: jgNumber, S: string(x)}, nil
	case json.RawMessage:
		return jsonGobNode{K: jgRaw, S: string(x)}, nil
	case JsonValue:
		inner, err := jsonToGobNode(x.raw)
		if err != nil {
			return jsonGobNode{}, err
		}
		return jsonGobNode{K: jgValue, Kids: []jsonGobNode{inner}}, nil
	case []any:
		n := jsonGobNode{K: jgList, Kids: make([]jsonGobNode, 0, len(x))}
		for _, it := range x {
			kid, err := jsonToGobNode(it)
			if err != nil {
				return jsonGobNode{}, err
			}
			n.Kids = append(n.Kids, kid)
		}
		return n, nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		n := jsonGobNode{K: jgMap, Keys: keys, Kids: make([]jsonGobNode, 0, len(keys))}
		for _, k := range keys {
			kid, err := jsonToGobNode(x[k])
			if err != nil {
				return jsonGobNode{}, err
			}
			n.Kids = append(n.Kids, kid)
		}
		return n, nil
	case jsonOrderedObject:
		n := jsonGobNode{K: jgOrdered, Keys: append([]string(nil), x.keys...), Kids: make([]jsonGobNode, 0, len(x.vals))}
		for _, it := range x.vals {
			kid, err := jsonToGobNode(it)
			if err != nil {
				return jsonGobNode{}, err
			}
			n.Kids = append(n.Kids, kid)
		}
		return n, nil
	}
	// Any other leaf: keep its JSON meaning. A value JSON cannot represent
	// (a NaN Float) is an error rather than a silent substitute.
	b, err := json.Marshal(v)
	if err != nil {
		return jsonGobNode{}, fmt.Errorf("Json.Value leaf of type %T cannot be stored: %w", v, err)
	}
	return jsonGobNode{K: jgRaw, S: string(b)}, nil
}

func jsonFromGobNode(n jsonGobNode) (any, error) {
	switch n.K {
	case jgNull:
		return nil, nil
	case jgString:
		return n.S, nil
	case jgInt:
		return int(n.I), nil
	case jgInt64:
		return n.I, nil
	case jgFloat:
		return n.F, nil
	case jgBool:
		return n.B, nil
	case jgNumber:
		return json.Number(n.S), nil
	case jgRaw:
		return json.RawMessage(n.S), nil
	case jgValue:
		if len(n.Kids) != 1 {
			return nil, errors.New("Json.Value: malformed nested value")
		}
		inner, err := jsonFromGobNode(n.Kids[0])
		if err != nil {
			return nil, err
		}
		return JsonValue{raw: inner}, nil
	case jgList:
		out := make([]any, 0, len(n.Kids))
		for _, k := range n.Kids {
			v, err := jsonFromGobNode(k)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case jgMap, jgOrdered:
		if len(n.Keys) != len(n.Kids) {
			return nil, errors.New("Json.Value: malformed object")
		}
		vals := make([]any, 0, len(n.Kids))
		for _, k := range n.Kids {
			v, err := jsonFromGobNode(k)
			if err != nil {
				return nil, err
			}
			vals = append(vals, v)
		}
		if n.K == jgOrdered {
			return jsonOrderedObject{keys: append([]string(nil), n.Keys...), vals: vals}, nil
		}
		m := make(map[string]any, len(n.Keys))
		for i, k := range n.Keys {
			m[k] = vals[i]
		}
		return m, nil
	}
	return nil, fmt.Errorf("Json.Value: unknown node kind %d", n.K)
}

// GobEncode stores the Value's tree (see the file header).
func (v JsonValue) GobEncode() ([]byte, error) {
	n, err := jsonToGobNode(v.raw)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(n); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// GobDecode restores a Value written by GobEncode.
func (v *JsonValue) GobDecode(b []byte) error {
	var n jsonGobNode
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&n); err != nil {
		return err
	}
	raw, err := jsonFromGobNode(n)
	if err != nil {
		return err
	}
	v.raw = raw
	return nil
}

// errSecretNotStorable is returned by every attempt to gob-encode a Secret.
var errSecretNotStorable = errors.New("a Secret is never written to a session store or any gob stream " +
	"(it would be stored in clear); keep secrets out of the model and read them where they are used")

// GobEncode refuses: a Secret must not be serialised (see the file header).
func (s Secret) GobEncode() ([]byte, error) { return nil, errSecretNotStorable }

// GobDecode refuses too, so no stream can mint a Secret.
func (s *Secret) GobDecode([]byte) error { return errSecretNotStorable }
