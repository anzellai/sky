// trim_imports.go — Remove unused imports from a Go file.
// Usage: trim-imports <file.go>
// Rewrites the file in-place, removing import lines whose package
// identifier doesn't appear in the function body code.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		return
	}
	filePath := os.Args[1]

	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	content := string(data)

	// Split into import block and body
	importStart := strings.Index(content, "import (")
	if importStart < 0 {
		return // no import block
	}
	importEnd := strings.Index(content[importStart:], ")")
	if importEnd < 0 {
		return
	}
	importEnd += importStart + 1

	before := content[:importStart]
	importBlock := content[importStart:importEnd]
	after := content[importEnd:]

	// Parse import lines
	lines := strings.Split(importBlock, "\n")
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "import (" || trimmed == ")" || trimmed == "" {
			kept = append(kept, line)
			continue
		}

		// Extract package identifier
		pkgIdent := extractPkgIdent(trimmed)
		if pkgIdent == "" || isUsedInBody(pkgIdent, after) {
			kept = append(kept, line)
		}
		// else: skip unused import
	}

	result := before + strings.Join(kept, "\n") + after
	os.WriteFile(filePath, []byte(result), 0644)
}

func extractPkgIdent(importLine string) string {
	// Remove quotes
	line := strings.ReplaceAll(importLine, "\"", "")
	line = strings.TrimSpace(line)

	parts := strings.Fields(line)
	if len(parts) >= 2 {
		// alias "path" — alias is the identifier
		return parts[0]
	}
	if len(parts) == 1 {
		// "path" — last path segment is the identifier
		segments := strings.Split(parts[0], "/")
		return segments[len(segments)-1]
	}
	return ""
}

func isUsedInBody(pkg string, body string) bool {
	// Check for pkg. (package-qualified access)
	if strings.Contains(body, pkg+".") {
		return true
	}
	// Check for pkg as standalone identifier (rare but possible)
	// Only for special cases like "fmt" in fmt.Println
	return false
}

func init() {
	// Suppress unused import of fmt if we remove the debug prints
	_ = fmt.Sprint
	_ = bufio.NewReader
}
