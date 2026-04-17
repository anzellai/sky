package sky_sky_core_regex

import (
	"fmt"
	"regexp"
)

type SkyMaybe struct {
	Tag       int
	SkyName   string
	JustValue any
}

func just(v any) SkyMaybe  { return SkyMaybe{Tag: 0, SkyName: "Just", JustValue: v} }
func nothing() SkyMaybe    { return SkyMaybe{Tag: 1, SkyName: "Nothing"} }

func Match(pattern, input any) any {
	re, err := regexp.Compile(asString(pattern))
	if err != nil { return false }
	return re.MatchString(asString(input))
}

func Find(pattern, input any) any {
	re, err := regexp.Compile(asString(pattern))
	if err != nil { return nothing() }
	m := re.FindString(asString(input))
	if m == "" { return nothing() }
	return just(m)
}

func FindAll(pattern, input any) any {
	re, err := regexp.Compile(asString(pattern))
	if err != nil { return []any{} }
	matches := re.FindAllString(asString(input), -1)
	result := make([]any, len(matches))
	for i, m := range matches { result[i] = m }
	return result
}

func Replace(pattern, replacement, input any) any {
	re, err := regexp.Compile(asString(pattern))
	if err != nil { return input }
	return re.ReplaceAllString(asString(input), asString(replacement))
}

func Split(pattern, input any) any {
	re, err := regexp.Compile(asString(pattern))
	if err != nil { return []any{input} }
	parts := re.Split(asString(input), -1)
	result := make([]any, len(parts))
	for i, p := range parts { result[i] = p }
	return result
}

func asString(v any) string {
	if s, ok := v.(string); ok { return s }
	return fmt.Sprintf("%v", v)
}
