// sky_dce.go — Fast dead code elimination for Sky compiler output.
// Usage: sky-dce <outDir>
// Performs wrapper DCE and main.go DCE in a single pass using native Go strings.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sky-dce <outDir>")
		os.Exit(1)
	}
	outDir := os.Args[1]

	// Phase 1: Wrapper DCE — remove unused functions from wrapper files
	wrapperElim := wrapperDCE(outDir)

	// Phase 2: Main.go DCE — remove unreachable functions from main.go
	mainElim := mainGoDCE(outDir)

	if wrapperElim > 0 {
		fmt.Printf("   [DCE] wrapper: %d functions eliminated\n", wrapperElim)
	}
	if mainElim > 0 {
		fmt.Printf("   [DCE] main.go: %d functions eliminated\n", mainElim)
	}
}

// wrapperDCE reads main.go, extracts all identifiers, then trims wrapper files
// to keep only functions whose names appear in main.go.
func wrapperDCE(outDir string) int {
	mainGoPath := filepath.Join(outDir, "main.go")
	mainGoContent, err := os.ReadFile(mainGoPath)
	if err != nil {
		return 0
	}
	mainGo := string(mainGoContent)

	// Find all wrapper files
	wrapperFiles := findWrapperFiles(outDir)
	if len(wrapperFiles) == 0 {
		return 0
	}

	totalEliminated := 0
	for _, wf := range wrapperFiles {
		elim := trimWrapperFile(wf, mainGo)
		totalEliminated += elim
	}

	// Run trim-imports on remaining files
	for _, wf := range wrapperFiles {
		if _, err := os.Stat(wf); err == nil {
			runTrimImports(wf)
		}
	}

	return totalEliminated
}

func findWrapperFiles(outDir string) []string {
	var files []string
	patterns := []string{
		filepath.Join(outDir, "sky_ffi_*.go"),
		filepath.Join(outDir, "sky_*.go"),
	}
	seen := map[string]bool{}
	for _, pat := range patterns {
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				files = append(files, m)
			}
		}
	}
	sort.Strings(files)
	return files
}

func trimWrapperFile(filePath string, mainGo string) int {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return 0
	}

	header, funcBlocks := splitSections(string(content))
	eliminated := 0
	var kept []string

	for _, block := range funcBlocks {
		name := extractFuncName(block)
		if name == "" || isHelperFunc(name) || strings.Contains(mainGo, name) {
			kept = append(kept, block)
		} else {
			eliminated++
		}
	}

	if len(kept) == 0 {
		os.Remove(filePath)
	} else if eliminated > 0 {
		out := header + "\n\n" + strings.Join(kept, "\n\n") + "\n"
		os.WriteFile(filePath, []byte(out), 0644)
	}

	return eliminated
}

func isHelperFunc(name string) bool {
	if name == "" {
		return true
	}
	return name[0] >= 'a' && name[0] <= 'z'
}

// mainGoDCE performs transitive reachability analysis on main.go functions.
func mainGoDCE(outDir string) int {
	mainGoPath := filepath.Join(outDir, "main.go")
	content, err := os.ReadFile(mainGoPath)
	if err != nil {
		return 0
	}

	header, funcBlocks := splitSections(string(content))

	// Separate var lines from func blocks
	var allVarLines []string
	var pureFuncBlocks []string

	for _, block := range funcBlocks {
		lines := strings.Split(block, "\n")
		var varLines, funcLines []string
		for _, line := range lines {
			if strings.HasPrefix(line, "var ") {
				varLines = append(varLines, line)
			} else {
				funcLines = append(funcLines, line)
			}
		}
		allVarLines = append(allVarLines, varLines...)
		cleaned := strings.TrimSpace(strings.Join(funcLines, "\n"))
		if strings.HasPrefix(cleaned, "func ") {
			pureFuncBlocks = append(pureFuncBlocks, cleaned)
		}
	}

	// Build func name -> body map
	funcMap := map[string]string{}
	for _, block := range pureFuncBlocks {
		name := extractFuncName(block)
		if name != "" {
			funcMap[name] = block
		}
	}

	// Find seed functions
	// 1. Wrapper seeds: main.go functions referenced in wrapper files
	wrapperContent := readWrapperContent(outDir)
	var seeds []string
	seeds = append(seeds, "main")

	funcNames := make([]string, 0, len(funcMap))
	for name := range funcMap {
		funcNames = append(funcNames, name)
	}

	for _, name := range funcNames {
		if strings.Contains(wrapperContent, name) {
			seeds = append(seeds, name)
		}
	}

	// 2. Non-func seeds: functions referenced in header + var lines
	allNonFuncCode := header + "\n" + strings.Join(allVarLines, "\n")
	for _, name := range funcNames {
		if strings.Contains(allNonFuncCode, name) {
			seeds = append(seeds, name)
		}
	}

	// Build reachable set via BFS
	reachable := map[string]bool{}
	queue := seeds
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if reachable[name] {
			continue
		}
		reachable[name] = true

		body := funcMap[name]
		if body == "" {
			continue
		}

		for _, n := range funcNames {
			if n != name && !reachable[n] && strings.Contains(body, n) {
				queue = append(queue, n)
			}
		}
	}

	// Filter functions
	var keptFuncs []string
	eliminated := 0
	for _, block := range pureFuncBlocks {
		name := extractFuncName(block)
		if reachable[name] {
			keptFuncs = append(keptFuncs, block)
		} else {
			eliminated++
		}
	}

	if eliminated > 0 {
		varsSection := ""
		if len(allVarLines) > 0 {
			varsSection = "\n" + strings.Join(allVarLines, "\n")
		}
		bodyCode := varsSection + "\n\n" + strings.Join(keptFuncs, "\n\n") + "\n"
		newCode := header + bodyCode
		os.WriteFile(mainGoPath, []byte(newCode), 0644)

		// Run goimports to fix imports (adds missing ones)
		runTrimImports(mainGoPath)

		// Ensure skylive_rt import if needed
		if strings.Contains(bodyCode, "skylive_rt") {
			ensureSkyliveImport(mainGoPath)
		}
	}

	return eliminated
}

func readWrapperContent(outDir string) string {
	files := findWrapperFiles(outDir)
	var sb strings.Builder
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err == nil {
			sb.Write(data)
		}
	}
	// Also read live_init.go if present — its references must survive DCE
	liveInit := filepath.Join(outDir, "live_init.go")
	if data, err := os.ReadFile(liveInit); err == nil {
		sb.Write(data)
	}
	// Also read skydb_runtime.go if present — Sky.Db helpers reference main.go functions
	dbRuntime := filepath.Join(outDir, "skydb_runtime.go")
	if data, err := os.ReadFile(dbRuntime); err == nil {
		sb.Write(data)
	}
	return sb.String()
}

func splitSections(code string) (header string, functions []string) {
	lines := strings.Split(code, "\n")
	var headerLines []string
	var funcBlocks []string
	var currentFunc strings.Builder
	inFunc := false

	for _, line := range lines {
		if strings.HasPrefix(line, "func ") {
			if inFunc && currentFunc.Len() > 0 {
				funcBlocks = append(funcBlocks, strings.TrimSpace(currentFunc.String()))
				currentFunc.Reset()
			}
			currentFunc.WriteString(line)
			inFunc = true
		} else if inFunc {
			currentFunc.WriteString("\n")
			currentFunc.WriteString(line)
		} else {
			headerLines = append(headerLines, line)
		}
	}
	if inFunc && currentFunc.Len() > 0 {
		funcBlocks = append(funcBlocks, strings.TrimSpace(currentFunc.String()))
	}

	return strings.TrimSpace(strings.Join(headerLines, "\n")), funcBlocks
}

func extractFuncName(block string) string {
	if !strings.HasPrefix(block, "func ") {
		return ""
	}
	rest := block[5:]
	idx := strings.IndexByte(rest, '(')
	if idx <= 0 {
		return ""
	}
	return rest[:idx]
}

func trimUnusedImports(header string, bodyCode string) string {
	// Simple import trimming: check each import line against body usage
	lines := strings.Split(header, "\n")
	var result []string
	inImport := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "import (" {
			inImport = true
			result = append(result, line)
			continue
		}
		if inImport && trimmed == ")" {
			inImport = false
			result = append(result, line)
			continue
		}
		if inImport {
			// Extract package name from import line
			pkg := extractImportPkg(trimmed)
			if pkg == "" || strings.Contains(bodyCode, pkg) {
				result = append(result, line)
			}
		} else {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

func extractImportPkg(importLine string) string {
	// Handle: "pkg/path" or alias "pkg/path" or _ "pkg/path"
	line := strings.TrimSpace(importLine)
	line = strings.Trim(line, "\"")

	// Get the last segment of the import path
	parts := strings.Split(line, "/")
	pkg := parts[len(parts)-1]
	pkg = strings.Trim(pkg, "\"")

	// Handle aliased imports
	if idx := strings.Index(importLine, "\""); idx > 0 {
		alias := strings.TrimSpace(importLine[:idx])
		if alias != "" && alias != "_" {
			return alias
		}
	}

	return pkg
}

func runTrimImports(filePath string) {
	exec.Command("goimports", "-w", filePath).Run()
}

func ensureSkyliveImport(filePath string) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	code := string(content)
	if strings.Contains(code, "sky-app/skylive_rt") {
		return
	}
	code = strings.Replace(code, "import (", "import (\n\tskylive_rt \"sky-app/skylive_rt\"", 1)
	os.WriteFile(filePath, []byte(code), 0644)
}
