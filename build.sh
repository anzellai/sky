#!/bin/sh
# Build the Sky compiler with native JSON optimization
set -e

echo "Building Sky compiler..."
sky build src/Main.sky

echo "Applying native JSON optimization..."
python3 -c "
import re
with open('sky-out/main.go') as f: c=f.read()
c=re.sub(r'(?<!func )Lsp_JsonRpc_FindInString\(', 'sky_nativeFindInString(', c)
c=re.sub(r'(?<!func )Lsp_JsonRpc_ExtractBracketed\(', 'sky_nativeExtractBracketed(', c)
c=re.sub(r'(?<!func )Lsp_JsonRpc_ExtractBraced\(', 'sky_nativeExtractBracketed(', c)
c=re.sub(r'(?<!func )Lsp_JsonRpc_SplitJsonElements\(', 'sky_nativeSplitJsonElements(', c)
c=re.sub(r'(?<!func )Lsp_JsonRpc_JsonSplitArray\(', 'sky_nativeJsonSplitArray(', c)
with open('sky-out/main.go','w') as f: f.write(c)
"

cat > sky-out/sky_json_native.go << 'GOEOF'
package main

import "strings"

var _ = strings.TrimSpace

func sky_nativeFindInString(needle any, haystack any, idx any) any {
	n := sky_asString(needle); h := sky_asString(haystack); i := sky_asInt(idx)
	if i > 0 && i < len(h) { h = h[i:] } else if i >= len(h) { return -1 }
	pos := strings.Index(h, n); if pos < 0 { return -1 }; return i + pos
}

func sky_nativeExtractBracketed(remaining any, _ any, _ any) any {
	str := sky_asString(remaining); depth := 0; inStr := false; esc := false
	for i := 0; i < len(str); i++ {
		c := str[i]; if esc { esc = false; continue }; if c == 92 && inStr { esc = true; continue }
		if c == 34 { inStr = !inStr; continue }; if inStr { continue }
		if c == 91 || c == 123 { depth++ } else if c == 93 || c == 125 { depth--; if depth == 0 { return str[:i+1] } }
	}
	return str
}

func sky_nativeSplitJsonElements(s any, _ any, _ any, _ any, _ any) any {
	str := strings.TrimSpace(sky_asString(s)); if len(str) == 0 { return []any{} }
	var result []any; depth := 0; start := 0; inStr := false; esc := false
	for i := 0; i < len(str); i++ {
		c := str[i]; if esc { esc = false; continue }; if c == 92 && inStr { esc = true; continue }
		if c == 34 { inStr = !inStr; continue }; if inStr { continue }
		if c == 123 || c == 91 { depth++ } else if c == 125 || c == 93 { depth-- } else if c == 44 && depth == 0 {
			elem := strings.TrimSpace(str[start:i]); if len(elem) > 0 { result = append(result, elem) }; start = i + 1
		}
	}
	last := strings.TrimSpace(str[start:]); if len(last) > 0 { result = append(result, last) }
	if result == nil { return []any{} }; return result
}

func sky_nativeJsonSplitArray(arrayStr any) any {
	str := strings.TrimSpace(sky_asString(arrayStr))
	if len(str) < 2 { return []any{} }
	inner := strings.TrimSpace(str[1:len(str)-1])
	if len(inner) == 0 { return []any{} }
	return sky_nativeSplitJsonElements(inner, nil, nil, nil, nil)
}
GOEOF

echo "Rebuilding Go binary..."
cd sky-out && go build -gcflags=all=-l -o app .
echo "Build complete: sky-out/app"
