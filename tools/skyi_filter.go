// skyi_filter.go — Filter .skyi binding files to keep only used declarations.
// Usage: skyi-filter <skyi_path> <alias>
// Reads all .sky source files from src/ to determine which declarations are used.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	if len(os.Args) < 3 {
		return
	}
	skyiPath := os.Args[1]
	alias := os.Args[2]
	srcRoot := "src"
	if len(os.Args) >= 4 {
		srcRoot = os.Args[3]
	}

	// Precompute set of used qualified names (Alias.funcName)
	usedNames := buildUsedNameSet(alias, srcRoot)

	f, err := os.Open(skyiPath)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	inHeader := true
	keep := true

	for sc.Scan() {
		line := sc.Text()

		if inHeader {
			if strings.HasPrefix(line, "module ") || strings.HasPrefix(line, "import ") ||
				strings.HasPrefix(line, "foreign ") || line == "" {
				fmt.Println(line)
				continue
			}
			inHeader = false
		}

		if strings.HasPrefix(line, "type ") {
			fmt.Println(line)
			continue
		}

		if strings.HasPrefix(line, " ") || line == "" {
			if keep {
				fmt.Println(line)
			}
			continue
		}

		// New top-level declaration — check if name is in used set
		name := extractName(line)
		keep = usedNames[name]
		if keep {
			fmt.Println(line)
		}
	}
}

func extractName(line string) string {
	for i, c := range line {
		if c == ' ' {
			return line[:i]
		}
	}
	return line
}

// Build a set of function names used as Alias.name in source files
func buildUsedNameSet(alias string, srcRoot string) map[string]bool {
	src := loadAllSources(srcRoot)
	prefix := alias + "."
	used := map[string]bool{}

	// Find all occurrences of Alias.identifier in source
	re := regexp.MustCompile(regexp.QuoteMeta(prefix) + `([a-zA-Z_][a-zA-Z0-9_]*)`)
	matches := re.FindAllStringSubmatch(src, -1)
	for _, m := range matches {
		used[m[1]] = true
	}
	return used
}

func loadAllSources(srcRoot string) string {
	var b strings.Builder
	patterns := []string{srcRoot + "/*.sky", srcRoot + "/**/*.sky", srcRoot + "/*/*.sky", srcRoot + "/*/*/*.sky"}
	seen := map[string]bool{}
	for _, pat := range patterns {
		files, _ := filepath.Glob(pat)
		for _, p := range files {
			if seen[p] {
				continue
			}
			seen[p] = true
			data, err := os.ReadFile(p)
			if err == nil {
				b.Write(data)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
