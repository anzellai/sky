package rt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A message a Sky user reads (an Err, a 403 body, a startup error) names what
// a Sky program writes, never a Go function of this runtime: a downstream
// project was told to "exempt the route with WithoutCsrf(path)", which no
// Sky program can call (it writes `Server.api`). This scans every string
// literal in the runtime's non-test sources for Go-level advice that has been
// replaced by the Sky API, so it cannot come back.
func TestUserFacingStringsNameTheSkyAPI(t *testing.T) {
	forbidden := regexp.MustCompile(`WithoutCsrf|RegisterPure|Live_app\b|blank import|hand-edited main\.go|` +
		`MountLiveSubAppInProcess directly|EmbeddedPostgresBundleName`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				s = lit.Value
			}
			if m := forbidden.FindString(s); m != "" {
				t.Errorf("%s: a user-facing string names the Go-level %q: %q",
					fset.Position(lit.Pos()), m, s)
			}
			return true
		})
	}
}
