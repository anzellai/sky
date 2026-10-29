//go:build !js

package rt

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// wasmServerOnlyPackages are packages the runtime uses on the server only.
// None of them may reach the js/wasm build of `rt`, which every Sky.Spa client
// links.
//
// The function-level dead-code elimination of the Go linker is not enough to
// keep a package out: Go runs the init of every package a binary imports, and
// an init keeps whatever it references (archive/zip's init registers the
// deflate writer, encoding/xml's builds its entity tables). So an import in a
// file without a `//go:build !js` tag puts the package into every wasm client,
// even when no client code calls it. That happened with Std.Xml, Std.Zip and
// Std.Office: the clients grew by about 255 kB raw, 64 kB gzip. The fix keeps
// the import in a server-only file and gives the js build a twin that returns
// a classified Err (xml_stream.go / xml_stream_js.go, zip_archive.go /
// zip_archive_js.go).
//
// A new server-only kernel that pulls a heavy package belongs on this list.
var wasmServerOnlyPackages = []string{
	"archive/tar",
	"archive/zip",
	"database/sql",
	"encoding/xml",
	"github.com/jackc/pgx/v5",
	"github.com/redis/go-redis/v9",
	"golang.org/x/text/encoding",
	"golang.org/x/text/encoding/charmap",
	"google.golang.org/grpc",
	"html/template",
	"image/gif",
	"image/jpeg",
	"image/png",
	"modernc.org/sqlite",
	"net/smtp",
	"os/exec",
}

// wasmServerOnlyMustBeNative are the entries of wasmServerOnlyPackages the
// native build must import, so the list keeps naming packages the runtime
// really uses (a renamed import would otherwise pass in silence).
var wasmServerOnlyMustBeNative = []string{"archive/zip", "encoding/xml", "golang.org/x/text/encoding/charmap"}

// wasmServerOnlyKernelPrefixes are the kernel families whose every exported
// function the probe client references, so the probe is the worst case: a
// client that names every server-only kernel still links none of the
// server-only packages.
var wasmServerOnlyKernelPrefixes = []string{"Xml_", "Zip_", "Office_"}

// TestWasmClientLinksNoServerOnlyPackage builds a js/wasm program that
// references every Xml_, Zip_ and Office_ kernel and checks that the binary
// links none of wasmServerOnlyPackages: neither in its import graph (where
// package inits come from) nor among the function names of the wasm.
//
// The harness gate spa-wasm-server-only runs this test and requires its
// `ASSERTIONS:` line. Its falsifying mutation imports archive/zip from
// zip_safe.go, which has no build tag; the import-graph and function-name
// assertions for archive/zip must go red.
func TestWasmClientLinksNoServerOnlyPackage(t *testing.T) {
	goBin, err := exec.LookPath("go")
	requireLive(t, "the Go toolchain (`go` on PATH)",
		"install Go (https://go.dev/dl/); this test builds the runtime for js/wasm", err == nil)
	n := 0

	rtDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	modRoot := filepath.Dir(rtDir)

	// The kernels the js build of rt exports, from the files that build sees.
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = "js", "wasm", false
	pkg, err := ctx.ImportDir(rtDir, 0)
	if err != nil {
		t.Fatalf("read the js build of rt: %v", err)
	}
	kernels := wasmKernelNames(t, rtDir, pkg.GoFiles)
	for _, p := range wasmServerOnlyKernelPrefixes {
		found := false
		for _, k := range kernels {
			found = found || strings.HasPrefix(k, p)
		}
		if !found {
			t.Fatalf("the js build of rt exports no %s kernel; the probe would test nothing", p)
		}
	}

	// The probe: a main package under the module (added by -overlay, nothing
	// is written to the tree) that keeps every kernel alive.
	tmp := t.TempDir()
	var src strings.Builder
	src.WriteString("package main\n\nimport \"sky-app/rt\"\n\nvar sink []any\n\nfunc main() {\n")
	for _, k := range kernels {
		fmt.Fprintf(&src, "\tsink = append(sink, rt.%s)\n", k)
	}
	src.WriteString("\tprintln(len(sink))\n}\n")
	probeSrc := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(probeSrc, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	probeDir := filepath.Join(modRoot, fmt.Sprintf("zz_wasm_probe_%d", os.Getpid()))
	overlay, _ := json.Marshal(map[string]map[string]string{
		"Replace": {filepath.Join(probeDir, "main.go"): probeSrc},
	})
	overlayPath := filepath.Join(tmp, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o644); err != nil {
		t.Fatal(err)
	}
	goCmd := func(goos string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(goBin, args...)
		cmd.Dir = modRoot
		cmd.Env = append(os.Environ(), "GOFLAGS=", "CGO_ENABLED=0", "GOOS="+goos)
		if goos == "js" {
			cmd.Env = append(cmd.Env, "GOARCH=wasm")
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go %s (GOOS=%s): %v\n%s", strings.Join(args, " "), goos, err, stderr.String())
		}
		return out
	}

	// 1. The import graph of the probe: no server-only package. Package inits
	// come from this graph, so a package here is in the binary.
	jsDeps := wasmLines(goCmd("js", "list", "-overlay", overlayPath, "-deps", "./"+filepath.Base(probeDir)))
	for _, p := range wasmServerOnlyPackages {
		if jsDeps[p] {
			t.Errorf("the js/wasm build imports %s, a server-only package. Every Sky.Spa client "+
				"would link it (Go runs the init of every imported package). Move the import "+
				"into a `//go:build !js` file and give the js build a twin that returns a "+
				"classified Err; see xml_stream.go and zip_archive.go.", p)
		}
		n++
	}

	// 2. The list names packages the server really uses.
	nativeDeps := wasmLines(goCmd(build.Default.GOOS, "list", "-deps", "./rt"))
	for _, p := range wasmServerOnlyMustBeNative {
		if !nativeDeps[p] {
			t.Errorf("the native build of rt no longer imports %s; update wasmServerOnlyPackages", p)
		}
		n++
	}

	// 3. The linked wasm: no function of a server-only package, and the
	// kernels the probe names are there (so the scan reads real names).
	wasm := filepath.Join(tmp, "probe.wasm")
	goCmd("js", "build", "-overlay", overlayPath, "-o", wasm, "./"+filepath.Base(probeDir))
	bin, err := os.ReadFile(wasm)
	if err != nil {
		t.Fatal(err)
	}
	names, err := wasmFunctionNames(bin)
	if err != nil {
		t.Fatalf("read the function names of %s: %v", wasm, err)
	}
	for _, p := range wasmServerOnlyPackages {
		var hits []string
		for _, name := range names {
			if strings.HasPrefix(name, wasmMangle(p)+".") {
				hits = append(hits, name)
			}
		}
		if len(hits) > 0 {
			sort.Strings(hits)
			if len(hits) > 5 {
				hits = append(hits[:5], "...")
			}
			t.Errorf("the js/wasm probe links %d functions of %s, a server-only package: %s",
				len(hits), p, strings.Join(hits, ", "))
		}
		n++
	}
	have := map[string]bool{}
	for _, name := range names {
		have[name] = true
	}
	for _, k := range []string{"Xml_parse", "Zip_read", "Office_xlsxRead"} {
		if !have[wasmMangle("sky-app/rt")+"."+k] {
			t.Errorf("the probe wasm has no function sky-app/rt.%s; the name scan is not reading the binary", k)
		}
		n++
	}
	fmt.Printf("ASSERTIONS: %d\n", n)
}

// wasmKernelNames lists the exported, non-generic, receiver-less functions of
// the files whose names start with a prefix of wasmServerOnlyKernelPrefixes.
func wasmKernelNames(t *testing.T, dir string, files []string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var out []string
	for _, f := range files {
		file, err := parser.ParseFile(fset, filepath.Join(dir, f), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Type.TypeParams != nil || !fn.Name.IsExported() {
				continue
			}
			for _, p := range wasmServerOnlyKernelPrefixes {
				if strings.HasPrefix(fn.Name.Name, p) {
					out = append(out, fn.Name.Name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func wasmLines(b []byte) map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			m[l] = true
		}
	}
	return m
}

// wasmMangle spells a package path the way the Go linker writes it in the
// wasm name section: every byte that is not a letter, a digit or `_` becomes
// `_` ("sky-app/rt" is "sky_app_rt", "archive/zip" is "archive_zip").
func wasmMangle(pkg string) string {
	b := []byte(pkg)
	for i, c := range b {
		if !(c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			b[i] = '_'
		}
	}
	return string(b)
}

// wasmFunctionNames reads the function names subsection of the wasm "name"
// custom section, which the Go linker writes for every linked function.
func wasmFunctionNames(b []byte) ([]string, error) {
	if len(b) < 8 || string(b[:4]) != "\x00asm" {
		return nil, errors.New("not a wasm module")
	}
	uleb := func(p *[]byte) (uint64, error) {
		v, k := binary.Uvarint(*p)
		if k <= 0 {
			return 0, errors.New("bad LEB128")
		}
		*p = (*p)[k:]
		return v, nil
	}
	name := func(p *[]byte) (string, error) {
		l, err := uleb(p)
		if err != nil || uint64(len(*p)) < l {
			return "", errors.New("bad name")
		}
		s := string((*p)[:l])
		*p = (*p)[l:]
		return s, nil
	}
	rest := b[8:]
	for len(rest) > 0 {
		id := rest[0]
		rest = rest[1:]
		size, err := uleb(&rest)
		if err != nil || uint64(len(rest)) < size {
			return nil, errors.New("bad section header")
		}
		body := rest[:size]
		rest = rest[size:]
		if id != 0 {
			continue
		}
		sec, err := name(&body)
		if err != nil {
			return nil, err
		}
		if sec != "name" {
			continue
		}
		for len(body) > 0 {
			sub := body[0]
			body = body[1:]
			ssize, err := uleb(&body)
			if err != nil || uint64(len(body)) < ssize {
				return nil, errors.New("bad name subsection")
			}
			sb := body[:ssize]
			body = body[ssize:]
			if sub != 1 {
				continue
			}
			count, err := uleb(&sb)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, count)
			for i := uint64(0); i < count; i++ {
				if _, err := uleb(&sb); err != nil {
					return nil, err
				}
				s, err := name(&sb)
				if err != nil {
					return nil, err
				}
				out = append(out, s)
			}
			return out, nil
		}
	}
	return nil, errors.New("no function names in the wasm (built with -ldflags=-s?)")
}
