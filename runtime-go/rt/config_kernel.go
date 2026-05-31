// Package rt — Std.Config runtime kernels.
//
// v0.15.47 stdlib batch (#380): typed TOML/YAML/JSON decoding.
//
// Architecture: every entry point (decodeToml/Yaml/Json) parses
// the input into a generic `map[string]any` (or `[]any` /
// primitive scalar). A `Decoder a` is a Go closure
// `func(any) any` returning either Ok[a] / Err[Error]. The
// combinators (`field`, `at`, `list`, `nullable`, `map`,
// `andThen`, `succeed`, `fail`) are all standard tree-walkers.
package rt

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// configDecoder is the runtime representation of `Std.Config.Decoder a`.
// A decoder takes a generic node and returns an Ok/Err Result.
type configDecoder func(node any) any // any = SkyResult[any, any]

// Config_string: Decoder String.
func Config_string() any {
	return configDecoder(func(node any) any {
		if s, ok := node.(string); ok {
			return Ok[any, any](s)
		}
		return Err[any, any](ErrDecode(fmt.Sprintf("config.string: expected string, got %T", node)))
	})
}

// Config_int: Decoder Int.
func Config_int() any {
	return configDecoder(func(node any) any {
		switch n := node.(type) {
		case int:
			return Ok[any, any](n)
		case int64:
			return Ok[any, any](int(n))
		case float64:
			// JSON numbers come back as float64; accept whole-number values.
			if float64(int(n)) == n {
				return Ok[any, any](int(n))
			}
			return Err[any, any](ErrDecode("config.int: non-integer float"))
		default:
			return Err[any, any](ErrDecode(fmt.Sprintf("config.int: expected int, got %T", node)))
		}
	})
}

// Config_float: Decoder Float.
func Config_float() any {
	return configDecoder(func(node any) any {
		switch n := node.(type) {
		case float64:
			return Ok[any, any](n)
		case int:
			return Ok[any, any](float64(n))
		case int64:
			return Ok[any, any](float64(n))
		default:
			return Err[any, any](ErrDecode(fmt.Sprintf("config.float: expected float, got %T", node)))
		}
	})
}

// Config_bool: Decoder Bool.
func Config_bool() any {
	return configDecoder(func(node any) any {
		if b, ok := node.(bool); ok {
			return Ok[any, any](b)
		}
		return Err[any, any](ErrDecode(fmt.Sprintf("config.bool: expected bool, got %T", node)))
	})
}

// Config_nullable: Decoder a -> Decoder (Maybe a).
func Config_nullable(inner any) any {
	d := castConfigDecoder(inner)
	return configDecoder(func(node any) any {
		if node == nil {
			return Ok[any, any](makeMaybeNothing())
		}
		r := d(node)
		if cfgIsErr(r) {
			// Treat decode failure as Nothing for nullable.
			return Ok[any, any](makeMaybeNothing())
		}
		return Ok[any, any](makeMaybeJust(cfgUnwrapOk(r)))
	})
}

// Config_field: String -> Decoder a -> Decoder a.
func Config_field(nameArg, inner any) any {
	name := fmt.Sprintf("%v", nameArg)
	d := castConfigDecoder(inner)
	return configDecoder(func(node any) any {
		m, ok := nodeAsMap(node)
		if !ok {
			return Err[any, any](ErrDecode("config.field: expected object at " + name))
		}
		child, ok := m[name]
		if !ok {
			return Err[any, any](ErrDecode("config.field: missing key " + name))
		}
		return d(child)
	})
}

// Config_at: List String -> Decoder a -> Decoder a.
func Config_at(pathArg, inner any) any {
	pathItems := AsList(pathArg)
	pathStrs := make([]string, len(pathItems))
	for i, it := range pathItems {
		pathStrs[i] = fmt.Sprintf("%v", it)
	}
	d := castConfigDecoder(inner)
	return configDecoder(func(node any) any {
		cur := node
		for _, k := range pathStrs {
			m, ok := nodeAsMap(cur)
			if !ok {
				return Err[any, any](ErrDecode("config.at: expected object at " + strings.Join(pathStrs, ".")))
			}
			child, ok := m[k]
			if !ok {
				return Err[any, any](ErrDecode("config.at: missing key " + k))
			}
			cur = child
		}
		return d(cur)
	})
}

// Config_list: Decoder a -> Decoder (List a).
func Config_list(inner any) any {
	d := castConfigDecoder(inner)
	return configDecoder(func(node any) any {
		items, ok := nodeAsSlice(node)
		if !ok {
			return Err[any, any](ErrDecode("config.list: expected array"))
		}
		out := make([]any, 0, len(items))
		for i, it := range items {
			r := d(it)
			if cfgIsErr(r) {
				return Err[any, any](ErrDecode(fmt.Sprintf("config.list[%d]: %v", i, errMsg(r))))
			}
			out = append(out, cfgUnwrapOk(r))
		}
		return Ok[any, any](out)
	})
}

// Config_succeed: a -> Decoder a.
func Config_succeed(v any) any {
	return configDecoder(func(_ any) any {
		return Ok[any, any](v)
	})
}

// Config_fail: String -> Decoder a.
func Config_fail(msgArg any) any {
	msg := fmt.Sprintf("%v", msgArg)
	return configDecoder(func(_ any) any {
		return Err[any, any](ErrDecode(msg))
	})
}

// Config_map: (a -> b) -> Decoder a -> Decoder b.
func Config_map(fn, inner any) any {
	d := castConfigDecoder(inner)
	return configDecoder(func(node any) any {
		r := d(node)
		if cfgIsErr(r) {
			return r
		}
		v := cfgUnwrapOk(r)
		mapped := SkyCall(fn, v)
		return Ok[any, any](mapped)
	})
}

// Config_andThen: (a -> Decoder b) -> Decoder a -> Decoder b.
func Config_andThen(fn, inner any) any {
	d := castConfigDecoder(inner)
	return configDecoder(func(node any) any {
		r := d(node)
		if cfgIsErr(r) {
			return r
		}
		v := cfgUnwrapOk(r)
		next := SkyCall(fn, v)
		nd := castConfigDecoder(next)
		return nd(node)
	})
}

// ──────────────────── Entry points ────────────────────

// Config_decodeToml: String -> Decoder a -> Result Error a.
func Config_decodeToml(srcArg, decArg any) any {
	src := fmt.Sprintf("%v", srcArg)
	var node any
	if _, err := toml.Decode(src, &node); err != nil {
		return Err[any, any](ErrDecode("config.decodeToml: " + err.Error()))
	}
	node = normaliseConfigNode(node)
	d := castConfigDecoder(decArg)
	return d(node)
}

// Config_decodeYaml: String -> Decoder a -> Result Error a.
func Config_decodeYaml(srcArg, decArg any) any {
	src := fmt.Sprintf("%v", srcArg)
	var node any
	if err := yaml.Unmarshal([]byte(src), &node); err != nil {
		return Err[any, any](ErrDecode("config.decodeYaml: " + err.Error()))
	}
	node = normaliseConfigNode(node)
	d := castConfigDecoder(decArg)
	return d(node)
}

// Config_decodeJson: String -> Decoder a -> Result Error a.
func Config_decodeJson(srcArg, decArg any) any {
	src := fmt.Sprintf("%v", srcArg)
	var node any
	if err := json.Unmarshal([]byte(src), &node); err != nil {
		return Err[any, any](ErrDecode("config.decodeJson: " + err.Error()))
	}
	node = normaliseConfigNode(node)
	d := castConfigDecoder(decArg)
	return d(node)
}

// Config_loadFromFile: String -> Decoder a -> Task Error a.
//
// Dispatches by extension: .toml / .yaml / .yml / .json.
func Config_loadFromFile(pathArg, decArg any) any {
	path := fmt.Sprintf("%v", pathArg)
	return func() any {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Err[any, any](ErrInvalidInput("config.loadFromFile: " + err.Error()))
		}
		lower := strings.ToLower(path)
		switch {
		case strings.HasSuffix(lower, ".toml"):
			return Config_decodeToml(string(raw), decArg)
		case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
			return Config_decodeYaml(string(raw), decArg)
		case strings.HasSuffix(lower, ".json"):
			return Config_decodeJson(string(raw), decArg)
		default:
			return Err[any, any](ErrInvalidInput("config.loadFromFile: unknown extension " + path))
		}
	}
}

// ──────────────────── helpers ────────────────────

func castConfigDecoder(v any) configDecoder {
	if d, ok := v.(configDecoder); ok {
		return d
	}
	// User got the decoder via Ffi.kernel and it might have travelled
	// through generic any-pipelines. Re-wrap a function-shaped value.
	if fn, ok := v.(func(any) any); ok {
		return configDecoder(fn)
	}
	return configDecoder(func(_ any) any {
		return Err[any, any](ErrDecode(fmt.Sprintf("config: not a decoder (%T)", v)))
	})
}

func nodeAsMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, vv := range m {
			out[fmt.Sprintf("%v", k)] = vv
		}
		return out, true
	}
	return nil, false
}

func nodeAsSlice(v any) ([]any, bool) {
	if xs, ok := v.([]any); ok {
		return xs, true
	}
	return nil, false
}

// normaliseConfigNode walks the decoded tree once, converting
// map[any]any → map[string]any (yaml.v3 returns the former for
// unstructured docs) so the decoder's `nodeAsMap` fast-path hits.
func normaliseConfigNode(v any) any {
	switch n := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(n))
		for k, vv := range n {
			out[fmt.Sprintf("%v", k)] = normaliseConfigNode(vv)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(n))
		for k, vv := range n {
			out[k] = normaliseConfigNode(vv)
		}
		return out
	case []any:
		out := make([]any, len(n))
		for i, it := range n {
			out[i] = normaliseConfigNode(it)
		}
		return out
	}
	return v
}

// cfgIsErr / unwrapOk / errMsg work on SkyResult[any, any].
func cfgIsErr(r any) bool {
	if rr, ok := r.(SkyResult[any, any]); ok {
		return rr.Tag == 1
	}
	return false
}

func cfgUnwrapOk(r any) any {
	if rr, ok := r.(SkyResult[any, any]); ok {
		return rr.OkValue
	}
	return r
}

func errMsg(r any) string {
	if rr, ok := r.(SkyResult[any, any]); ok {
		return fmt.Sprintf("%v", rr.ErrValue)
	}
	return ""
}
