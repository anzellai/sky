// sky_ffi_gen.go — Native FFI binding + wrapper generator for large Go packages.
// Usage: sky-ffi-gen <pkg_name> <inspect_json_path> <out_dir> [src_root]
//
// Reads inspect.json (Go package metadata), optionally scans source files
// for used symbols, and generates:
//   - bindings.skyi  (Sky type bindings)
//   - sky_wrappers/<safe_pkg>.go  (Go wrapper functions)
//
// For large packages (>1000 types), only generates bindings for symbols
// actually referenced in the source code.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Inspector JSON structures
type InspectData struct {
	Name   string      `json:"name"`
	Path   string      `json:"path"`
	Types  []TypeDef   `json:"types"`
	Funcs  []FuncDef   `json:"funcs"`
	Vars   []VarDef    `json:"vars"`
	Consts []ConstDef  `json:"consts"`
}

type TypeDef struct {
	Name    string     `json:"name"`
	Kind    string     `json:"kind"`
	Fields  []FieldDef `json:"fields"`
	Methods []FuncDef  `json:"methods"`
}

type FuncDef struct {
	Name          string     `json:"name"`
	Params        []ParamDef `json:"params"`
	Results       []ParamDef `json:"results"`
	Variadic      bool       `json:"variadic"`
	HasTypeParams bool       `json:"hasTypeParams"`
}

type ParamDef struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type FieldDef struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type VarDef struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type ConstDef struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Reserved Sky keywords
var reservedKeywords = map[string]bool{
	"type": true, "module": true, "import": true, "case": true,
	"let": true, "in": true, "if": true, "then": true, "else": true,
	"of": true, "foreign": true, "exposing": true, "as": true,
}

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Usage: sky-ffi-gen <pkg> <inspect.json> <outdir> [srcroot]\n")
		os.Exit(1)
		}
	pkgName := os.Args[1]
	inspectPath := os.Args[2]
	outDir := os.Args[3]
	srcRoot := "src"
	if len(os.Args) >= 5 {
		srcRoot = os.Args[4]
		}

	data, err := os.ReadFile(inspectPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot read %s: %v\n", inspectPath, err)
		os.Exit(1)
		}

	var inspect InspectData
	if err := json.Unmarshal(data, &inspect); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot parse %s: %v\n", inspectPath, err)
		os.Exit(1)
		}

	// For large packages, scan source to determine used symbols
	usedSymbols := map[string]bool{}
	isLarge := len(inspect.Types) > 200 || len(inspect.Consts) > 500
	if isLarge {
		alias := extractAlias(pkgName)
		usedSymbols = scanSourceForUsedSymbols(alias, srcRoot)
		fmt.Fprintf(os.Stderr, "[ffi-gen] Package %s: alias=%s, %d used symbols, %d types, %d consts\n", pkgName, alias, len(usedSymbols), len(inspect.Types), len(inspect.Consts))
		if len(usedSymbols) > 0 {
			syms := []string{}
			for s := range usedSymbols {
				syms = append(syms, s)
			}
			fmt.Fprintf(os.Stderr, "[ffi-gen] Used symbols: %v\n", syms[:min(len(syms), 20)])
		}
		}

	safePkg := safePkgName(pkgName)
	moduleName := pkgToModuleName(pkgName)
	ancestors := buildAncestorPkgs(pkgName)

	// Generate bindings.skyi
	var skyi strings.Builder
	skyi.WriteString(fmt.Sprintf("module %s exposing (..)\n\n", moduleName))
	skyi.WriteString(fmt.Sprintf("foreign import \"%s\" exposing (..)\n\n", pkgName))

	// Generate wrapper .go (functions only, import block added at the end)
	var wrapper strings.Builder
	extraImports := map[string]bool{}

	// Collect types referenced by used functions/methods/fields
	referencedTypes := map[string]bool{}
	if isLarge {
		collectReferencedTypes(&inspect, usedSymbols, ancestors, referencedTypes)
		}

	// Collect field accessor names so we don't create conflicting type aliases
	fieldAccessorNames := map[string]bool{}
	for _, t := range inspect.Types {
		if t.Kind != "struct" {
			continue
		}
		for _, field := range t.Fields {
			// Field accessor: lowerfirst(TypeName) + Capitalise(FieldName)
			// But the conflict is: type alias "CheckoutSessionCustomerDetails"
			// vs field accessor "checkoutSessionCustomerDetails" — the type alias
			// creates a constructor that shadows the function
			accessorPascal := t.Name + capitalise(field.Name)
			fieldAccessorNames[accessorPascal] = true
		}
		}

	// --- Types (opaque) — only structs/interfaces, skip if name conflicts with field accessor ---
	for _, t := range inspect.Types {
		if t.HasTypeParams(inspect) {
			continue
		}
		if t.Kind != "struct" && t.Kind != "interface" {
			continue
		}
		if isLarge && !isSymbolUsed(t.Name, usedSymbols) && !referencedTypes[t.Name] {
			continue
		}
		// Don't create type alias if the name matches a field accessor
		// (the accessor function would be shadowed by the constructor)
		if fieldAccessorNames[t.Name] {
			continue
		}
		skyi.WriteString(fmt.Sprintf("type %s = %s\n\n", t.Name, t.Name))
		}

	// --- Functions ---
	for _, f := range inspect.Funcs {
		if f.HasTypeParams || f.Name == "" {
			continue
		}
		if isLarge && !isSymbolUsed(f.Name, usedSymbols) {
			continue
		}
		if reservedKeywords[lowerFirst(f.Name)] {
			continue
		}
		if !isFuncSafe(f, pkgName, ancestors) {
			continue
		}

		skyiFunc, wrapFunc, imports := generateFuncBinding(f, pkgName, safePkg, ancestors)
		skyi.WriteString(skyiFunc)
		wrapper.WriteString(wrapFunc)
		for _, imp := range imports {
			extraImports[imp] = true
		}
		}

	// --- Methods ---
	for _, t := range inspect.Types {
		if t.HasTypeParams(inspect) {
			continue
		}
		for _, m := range t.Methods {
			if m.HasTypeParams || m.Name == "" {
				continue
			}
			if isLarge && !isSymbolUsed(m.Name, usedSymbols) && !isSymbolUsed(t.Name+m.Name, usedSymbols) {
				continue
			}
			if reservedKeywords[lowerFirst(m.Name)] {
				continue
			}
			if !isMethodSafe(m, pkgName, ancestors) {
				continue
			}

			skyiMethod, wrapMethod, imports := generateMethodBinding(m, t, pkgName, safePkg, ancestors)
			skyi.WriteString(skyiMethod)
			wrapper.WriteString(wrapMethod)
			for _, imp := range imports {
				extraImports[imp] = true
			}
		}
		}

	// --- Fields (getters) + Constructors + Setters ---
	for _, t := range inspect.Types {
		if t.Kind != "struct" || t.HasTypeParams(inspect) {
			continue
		}
		if isLarge && !isSymbolUsed(t.Name, usedSymbols) {
			continue
		}

		// Constructor: newTypeName : () -> TypeName
		skyiCtor, wrapCtor := generateStructConstructor(t, pkgName, safePkg)
		skyi.WriteString(skyiCtor)
		wrapper.WriteString(wrapCtor)

		for _, field := range t.Fields {
			if field.Name == "" || !unicode.IsUpper(rune(field.Name[0])) {
				continue
			}
			if isUnsafeFieldType(field.Type) {
				continue
			}

			// Getter
			skyiField, wrapField := generateFieldAccessor(field, t, pkgName, safePkg)
			skyi.WriteString(skyiField)
			wrapper.WriteString(wrapField)

			// Setter: typeNameSetFieldName : value -> TypeName -> TypeName
			skyiSetter, wrapSetter := generateFieldSetter(field, t, pkgName, safePkg)
			skyi.WriteString(skyiSetter)
			wrapper.WriteString(wrapSetter)
		}
		}

	// --- Variables ---
	for _, v := range inspect.Vars {
		if v.Name == "" || v.Type == "error" {
			continue
		}
		if isLarge && !isSymbolUsed(v.Name, usedSymbols) {
			continue
		}
		skyiVar, wrapVar := generateVarAccessor(v, pkgName, safePkg)
		skyi.WriteString(skyiVar)
		wrapper.WriteString(wrapVar)
		}

	// Collect method wrapper names to detect collisions with constants
	methodWrapperNames := map[string]bool{}
	for _, t := range inspect.Types {
		for _, m := range t.Methods {
			methodWrapperNames[fmt.Sprintf("Sky_%s_%s%s", safePkg, t.Name, m.Name)] = true
		}
	}

	// --- Constants ---
	for _, c := range inspect.Consts {
		if c.Name == "" || reservedKeywords[lowerFirst(c.Name)] {
			continue
		}
		if isLarge && !isSymbolUsed(c.Name, usedSymbols) {
			continue
		}
		constWrapperName := fmt.Sprintf("Sky_%s_%s", safePkg, c.Name)
		if methodWrapperNames[constWrapperName] {
			fmt.Fprintf(os.Stderr, "[SKIP] const %s collides with method wrapper %s\n", c.Name, constWrapperName)
			continue
		}
		skyiConst, wrapConst := generateConstAccessor(c, pkgName, safePkg)
		skyi.WriteString(skyiConst)
		wrapper.WriteString(wrapConst)
		}

	// Detect ancestor package references in wrapper code
	wrapperCode := wrapper.String()
	for _, a := range ancestors {
		alias := lastPathSegment(a)
		if strings.Contains(wrapperCode, alias+".") {
			extraImports[a] = true
		}
		}

	// Detect standard library type references in wrapper code
	stdlibRefs := map[string]string{
		"io.":      "io",
		"net.":     "net",
		"time.":    "time",
		"log.":     "log",
		"context.": "context",
		"sync.":    "sync",
	}
	for prefix, imp := range stdlibRefs {
		if imp != pkgName && strings.Contains(wrapperCode, prefix) {
			extraImports[imp] = true
		}
	}

	// Finalise wrapper imports
	var importBlock strings.Builder
	importBlock.WriteString("package sky_wrappers\n\nimport (\n")
	importBlock.WriteString(fmt.Sprintf("\t_ffi_fmt \"fmt\"\n"))
	importBlock.WriteString(fmt.Sprintf("\t_ffi_reflect \"reflect\"\n"))
	importBlock.WriteString(fmt.Sprintf("\t_ffi_pkg \"%s\"\n", pkgName))
	for imp := range extraImports {
		alias := lastPathSegment(imp)
		importBlock.WriteString(fmt.Sprintf("\t%s \"%s\"\n", alias, imp))
		}
	importBlock.WriteString(")\n\nvar _ = _ffi_fmt.Sprintf\nvar _ = _ffi_reflect.TypeOf\n\n")

	// Write output files
	os.MkdirAll(outDir, 0755)
	skyiPath := filepath.Join(outDir, "bindings.skyi")
	os.WriteFile(skyiPath, []byte(skyi.String()), 0644)

	wrapperDir := filepath.Join(outDir, "sky_wrappers")
	os.MkdirAll(wrapperDir, 0755)
	wrapperPath := filepath.Join(wrapperDir, safePkg+".go")

	wrapperContent := importBlock.String() + wrapper.String()
	os.WriteFile(wrapperPath, []byte(wrapperContent), 0644)

	fmt.Printf("Generated %s + %s\n", skyiPath, wrapperPath)
}

// --- Referenced type collection ---

func collectReferencedTypes(data *InspectData, used map[string]bool, ancestors []string, refs map[string]bool) {
	pkg := data.Path
	// Scan used functions for types in their signatures
	for _, f := range data.Funcs {
		if !isSymbolUsed(f.Name, used) {
			continue
		}
		for _, p := range f.Params {
			addReferencedType(p.Type, pkg, refs)
		}
		for _, r := range f.Results {
			addReferencedType(r.Type, pkg, refs)
		}
		}
	// Scan used types for field types
	for _, t := range data.Types {
		if !isSymbolUsed(t.Name, used) && !refs[t.Name] {
			continue
		}
		for _, m := range t.Methods {
			if !isSymbolUsed(m.Name, used) && !isSymbolUsed(t.Name+m.Name, used) {
				continue
			}
			for _, p := range m.Params {
				addReferencedType(p.Type, pkg, refs)
			}
			for _, r := range m.Results {
				addReferencedType(r.Type, pkg, refs)
			}
		}
		for _, field := range t.Fields {
			if isSymbolUsed(lowerFirst(t.Name)+capitalise(field.Name), used) {
				addReferencedType(field.Type, pkg, refs)
			}
		}
		}
}

func addReferencedType(goType string, pkg string, refs map[string]bool) {
	bare := strings.TrimPrefix(goType, "*")
	bare = strings.TrimPrefix(bare, "[]")
	bare = strings.TrimPrefix(bare, "*")
	if strings.HasPrefix(bare, pkg+".") {
		typeName := bare[len(pkg)+1:]
		refs[typeName] = true
		}
}

// --- Type safety checks ---

func isFuncSafe(f FuncDef, pkg string, ancestors []string) bool {
	if len(f.Results) > 3 {
		return false
		}
	for i, p := range f.Params {
		if f.Variadic && i == len(f.Params)-1 {
			// Variadic: accept any qualified type
			continue
		}
		if !isTypeSafe(p.Type, pkg, ancestors) {
			return false
		}
		}
	for _, r := range f.Results {
		if !isTypeSafe(r.Type, pkg, ancestors) {
			return false
		}
		}
	return true
}

func isMethodSafe(m FuncDef, pkg string, ancestors []string) bool {
	if len(m.Results) > 3 {
		return false
		}
	for i, p := range m.Params {
		if m.Variadic && i == len(m.Params)-1 {
			continue
		}
		if !isTypeSafe(p.Type, pkg, ancestors) {
			return false
		}
		}
	for _, r := range m.Results {
		if !isTypeSafe(r.Type, pkg, ancestors) {
			return false
		}
		}
	return true
}

func isTypeSafe(goType string, pkg string, ancestors []string) bool {
	// Primitives
	switch goType {
	case "string", "bool", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"byte", "rune",
		"float32", "float64", "error", "interface{}", "any",
		"[]byte", "[]string", "[]int", "[]float64", "[]bool", "[]any",
		"context.Context",
		"io.Reader", "io.Writer", "io.ReadCloser", "io.WriteCloser":
		return true
		}
	// Byte arrays
	if strings.HasPrefix(goType, "[") && strings.HasSuffix(goType, "]byte") {
		return true
		}
	// Function types — only HTTP handler
	if strings.HasPrefix(goType, "func(") {
		return strings.Contains(goType, "ResponseWriter")
		}
	// Maps — only string-keyed
	if strings.HasPrefix(goType, "map[") {
		return goType == "map[string]interface{}" || goType == "map[string]any" || goType == "map[string]string"
		}
	// Slices — check element
	if strings.HasPrefix(goType, "[]") {
		elem := goType[2:]
		if strings.HasPrefix(elem, "*") {
			elem = elem[1:]
		}
		return isFromPkgOrAncestor(elem, pkg, ancestors) || strings.Contains(elem, ".")
		}
	// Pointer to type
	bare := strings.TrimPrefix(goType, "*")
	// Same package or ancestor package
	return isFromPkgOrAncestor(bare, pkg, ancestors) || isFromPkgOrAncestor(goType, pkg, ancestors)
}

func isFromPkgOrAncestor(goType string, pkg string, ancestors []string) bool {
	if strings.HasPrefix(goType, pkg+".") || strings.HasPrefix(goType, pkg+"/") {
		return true
		}
	for _, a := range ancestors {
		if strings.HasPrefix(goType, a+".") {
			return true
		}
		}
	return false
}

func buildAncestorPkgs(pkg string) []string {
	parts := strings.Split(pkg, "/")
	if len(parts) <= 3 || !strings.Contains(parts[0], ".") {
		return nil
		}
	var ancestors []string
	for i := len(parts) - 1; i >= 3; i-- {
		ancestors = append(ancestors, strings.Join(parts[:i], "/"))
		}
	return ancestors
}

func isUnsafeFieldType(t string) bool {
	return strings.HasPrefix(t, "func(") ||
		strings.HasPrefix(t, "chan ") ||
		t == "unsafe.Pointer" ||
		strings.HasPrefix(t, "sync.")
}

// --- Binding generation ---

func generateFuncBinding(f FuncDef, pkg, safePkg string, ancestors []string) (string, string, []string) {
	skyName := lowerFirst(f.Name)
	wrapperName := fmt.Sprintf("Sky_%s_%s", safePkg, f.Name)

	// Sky binding
	var skyParams []string
	var skyParamNames []string
	for i, p := range f.Params {
		pName := fmt.Sprintf("arg%d", i)
		skyParams = append(skyParams, fmt.Sprintf("%s", mapGoTypeToSky(p.Type, pkg, ancestors)))
		skyParamNames = append(skyParamNames, pName)
		_ = p
		}

	retType := buildReturnType(f.Results, pkg, ancestors)
	sigParts := append(skyParams, retType)
	sig := strings.Join(sigParts, " -> ")
	if len(sigParts) == 1 {
		sig = retType
		}

	var skyi strings.Builder
	skyi.WriteString(fmt.Sprintf("%s : %s\n", skyName, sig))
	skyi.WriteString(fmt.Sprintf("%s", skyName))
	for _, n := range skyParamNames {
		skyi.WriteString(fmt.Sprintf(" %s", n))
		}
	skyi.WriteString(" =\n")
	skyi.WriteString(fmt.Sprintf("    %s", wrapperName))
	for _, n := range skyParamNames {
		skyi.WriteString(fmt.Sprintf(" %s", n))
		}
	skyi.WriteString("\n\n")

	// Go wrapper
	wrap, imports := generateGoWrapper(f, pkg, safePkg, wrapperName, false, "", ancestors)
	return skyi.String(), wrap, imports
}

func generateMethodBinding(m FuncDef, t TypeDef, pkg, safePkg string, ancestors []string) (string, string, []string) {
	skyName := lowerFirst(t.Name) + capitalise(m.Name)
	wrapperName := fmt.Sprintf("Sky_%s_%s%s", safePkg, t.Name, m.Name)

	// Receiver + params
	var skyParams []string
	skyParams = append(skyParams, t.Name) // receiver
	var skyParamNames []string
	skyParamNames = append(skyParamNames, "receiver")
	for i := range m.Params {
		pName := fmt.Sprintf("arg%d", i)
		skyParams = append(skyParams, mapGoTypeToSky(m.Params[i].Type, pkg, ancestors))
		skyParamNames = append(skyParamNames, pName)
		}

	retType := buildReturnType(m.Results, pkg, ancestors)
	sigParts := append(skyParams, retType)
	sig := strings.Join(sigParts, " -> ")

	var skyi strings.Builder
	skyi.WriteString(fmt.Sprintf("%s : %s\n", skyName, sig))
	skyi.WriteString(fmt.Sprintf("%s", skyName))
	for _, n := range skyParamNames {
		skyi.WriteString(fmt.Sprintf(" %s", n))
		}
	skyi.WriteString(" =\n")
	skyi.WriteString(fmt.Sprintf("    %s", wrapperName))
	for _, n := range skyParamNames {
		skyi.WriteString(fmt.Sprintf(" %s", n))
		}
	skyi.WriteString("\n\n")

	wrap, imports := generateGoWrapper(m, pkg, safePkg, wrapperName, true, t.Name, ancestors, t.Kind)
	return skyi.String(), wrap, imports
}

func generateFieldAccessor(field FieldDef, t TypeDef, pkg, safePkg string) (string, string) {
	skyName := lowerFirst(t.Name) + capitalise(field.Name)
	wrapperName := fmt.Sprintf("Sky_%s_FIELD_%s_%s", safePkg, t.Name, field.Name)

	skyType := mapGoTypeToSky(field.Type, pkg, nil)
	isPtr := strings.HasPrefix(field.Type, "*")
	if isPtr {
		skyType = "Maybe " + skyType
		}

	var skyi strings.Builder
	skyi.WriteString(fmt.Sprintf("%s : Any -> %s\n", skyName, skyType))
	skyi.WriteString(fmt.Sprintf("%s receiver =\n", skyName))
	skyi.WriteString(fmt.Sprintf("    %s receiver\n\n", wrapperName))

	var wrap strings.Builder
	if isPtr {
		wrap.WriteString(fmt.Sprintf("func %s(receiver any) any {\n", wrapperName))
		wrap.WriteString("\tv := _ffi_reflect.ValueOf(receiver)\n")
		wrap.WriteString("\tfor v.Kind() == _ffi_reflect.Ptr { v = v.Elem() }\n")
		wrap.WriteString("\tif v.Kind() == _ffi_reflect.Struct {\n")
		wrap.WriteString(fmt.Sprintf("\t\tf := v.FieldByName(\"%s\")\n", field.Name))
		wrap.WriteString("\t\tif f.IsValid() {\n")
		wrap.WriteString("\t\t\tif f.IsNil() { return SkyNothing() }\n")
		wrap.WriteString("\t\t\treturn SkyJust(f.Interface())\n")
		wrap.WriteString("\t\t}\n\t}\n\treturn SkyNothing()\n}\n\n")
	} else {
		wrap.WriteString(fmt.Sprintf("func %s(receiver any) any {\n", wrapperName))
		wrap.WriteString("\tv := _ffi_reflect.ValueOf(receiver)\n")
		wrap.WriteString("\tfor v.Kind() == _ffi_reflect.Ptr { v = v.Elem() }\n")
		wrap.WriteString("\tif v.Kind() == _ffi_reflect.Struct {\n")
		wrap.WriteString(fmt.Sprintf("\t\tf := v.FieldByName(\"%s\")\n", field.Name))
		wrap.WriteString("\t\tif f.IsValid() { return f.Interface() }\n")
		wrap.WriteString("\t}\n\treturn nil\n}\n\n")
		}
	return skyi.String(), wrap.String()
}

func generateStructConstructor(t TypeDef, pkg, safePkg string) (string, string) {
	skyName := "new" + t.Name
	wrapperName := fmt.Sprintf("Sky_%s_NEW_%s", safePkg, t.Name)

	var skyi strings.Builder
	skyi.WriteString(fmt.Sprintf("%s : () -> %s\n", skyName, t.Name))
	skyi.WriteString(fmt.Sprintf("%s _ =\n", skyName))
	skyi.WriteString(fmt.Sprintf("    %s ()\n\n", wrapperName))

	var wrap strings.Builder
	wrap.WriteString(fmt.Sprintf("func %s(_ any) any {\n", wrapperName))
	wrap.WriteString(fmt.Sprintf("\treturn &_ffi_pkg.%s{}\n", t.Name))
	wrap.WriteString("}\n\n")

	return skyi.String(), wrap.String()
}

func generateFieldSetter(field FieldDef, t TypeDef, pkg, safePkg string) (string, string) {
	skyName := lowerFirst(t.Name) + "Set" + capitalise(field.Name)
	wrapperName := fmt.Sprintf("Sky_%s_SET_%s_%s", safePkg, t.Name, field.Name)

	skyValType := mapGoTypeToSky(field.Type, pkg, nil)
	// For pointer fields, the setter takes the inner type (not Maybe)
	if strings.HasPrefix(field.Type, "*") {
		inner := field.Type[1:]
		skyValType = mapGoTypeToSky(inner, pkg, nil)
		}

	// Setter signature: value -> TypeName -> TypeName (for pipeline chaining)
	var skyi strings.Builder
	skyi.WriteString(fmt.Sprintf("%s : %s -> %s -> %s\n", skyName, skyValType, t.Name, t.Name))
	skyi.WriteString(fmt.Sprintf("%s val receiver =\n", skyName))
	skyi.WriteString(fmt.Sprintf("    %s val receiver\n\n", wrapperName))

	var wrap strings.Builder
	wrap.WriteString(fmt.Sprintf("func %s(val any, receiver any) any {\n", wrapperName))
	wrap.WriteString("\tv := _ffi_reflect.ValueOf(receiver)\n")
	wrap.WriteString("\tfor v.Kind() == _ffi_reflect.Ptr { v = v.Elem() }\n")
	wrap.WriteString("\tif v.Kind() != _ffi_reflect.Struct { return receiver }\n")
	wrap.WriteString(fmt.Sprintf("\tf := v.FieldByName(\"%s\")\n", field.Name))
	wrap.WriteString("\tif !f.IsValid() || !f.CanSet() { return receiver }\n")

	// Handle different field types
	if strings.HasPrefix(field.Type, "*string") {
		wrap.WriteString("\ts := sky_asString(val)\n")
		wrap.WriteString("\tf.Set(_ffi_reflect.ValueOf(&s))\n")
	} else if strings.HasPrefix(field.Type, "*int64") {
		wrap.WriteString("\tn := int64(sky_asInt(val))\n")
		wrap.WriteString("\tf.Set(_ffi_reflect.ValueOf(&n))\n")
	} else if strings.HasPrefix(field.Type, "*int") {
		wrap.WriteString("\tn := sky_asInt(val)\n")
		wrap.WriteString("\tf.Set(_ffi_reflect.ValueOf(&n))\n")
	} else if strings.HasPrefix(field.Type, "*float64") {
		wrap.WriteString("\tn := sky_asFloat(val)\n")
		wrap.WriteString("\tf.Set(_ffi_reflect.ValueOf(&n))\n")
	} else if strings.HasPrefix(field.Type, "*bool") {
		wrap.WriteString("\tb := sky_asBool(val)\n")
		wrap.WriteString("\tf.Set(_ffi_reflect.ValueOf(&b))\n")
	} else if field.Type == "string" {
		wrap.WriteString("\tf.Set(_ffi_reflect.ValueOf(sky_asString(val)))\n")
	} else if field.Type == "int" || field.Type == "int64" {
		wrap.WriteString(fmt.Sprintf("\tf.Set(_ffi_reflect.ValueOf(%s(sky_asInt(val))))\n", field.Type))
	} else if field.Type == "float64" {
		wrap.WriteString("\tf.Set(_ffi_reflect.ValueOf(sky_asFloat(val)))\n")
	} else if field.Type == "bool" {
		wrap.WriteString("\tf.Set(_ffi_reflect.ValueOf(sky_asBool(val)))\n")
	} else if field.Type == "[]string" {
		wrap.WriteString("\tf.Set(_ffi_reflect.ValueOf(sky_asStringSlice(val)))\n")
	} else if strings.HasPrefix(field.Type, "*") {
		// Pointer to struct or other type — pass through as-is
		wrap.WriteString("\tif val != nil { f.Set(_ffi_reflect.ValueOf(val)) }\n")
	} else if strings.HasPrefix(field.Type, "[]") {
		// Slice — convert from Sky list
		wrap.WriteString("\titems := sky_asList(val)\n")
		wrap.WriteString(fmt.Sprintf("\tslice := _ffi_reflect.MakeSlice(f.Type(), len(items), len(items))\n"))
		wrap.WriteString("\tfor i, item := range items {\n")
		wrap.WriteString("\t\tif item != nil { slice.Index(i).Set(_ffi_reflect.ValueOf(item).Elem()) }\n")
		wrap.WriteString("\t}\n")
		wrap.WriteString("\tf.Set(slice)\n")
	} else {
		// Other types — try direct set via reflection
		wrap.WriteString("\tif val != nil { f.Set(_ffi_reflect.ValueOf(val).Convert(f.Type())) }\n")
		}

	wrap.WriteString("\treturn receiver\n")
	wrap.WriteString("}\n\n")

	return skyi.String(), wrap.String()
}

func generateVarAccessor(v VarDef, pkg, safePkg string) (string, string) {
	skyName := lowerFirst(v.Name)
	getterName := fmt.Sprintf("Sky_%s_%s", safePkg, v.Name)

	var skyi strings.Builder
	var wrap strings.Builder

	if v.Type == "string" {
		setterName := fmt.Sprintf("Sky_%s_Set%s", safePkg, v.Name)
		skyi.WriteString(fmt.Sprintf("%s : () -> String\n", skyName))
		skyi.WriteString(fmt.Sprintf("%s _ =\n    %s ()\n\n", skyName, getterName))
		skyi.WriteString(fmt.Sprintf("set%s : String -> ()\n", capitalise(v.Name)))
		skyi.WriteString(fmt.Sprintf("set%s val =\n    %s val\n\n", capitalise(v.Name), setterName))
		wrap.WriteString(fmt.Sprintf("func %s(_ any) any { return _ffi_pkg.%s }\n\n", getterName, v.Name))
		wrap.WriteString(fmt.Sprintf("func %s(v any) any { _ffi_pkg.%s = sky_asString(v); return struct{}{} }\n\n", setterName, v.Name))
	} else {
		skyi.WriteString(fmt.Sprintf("%s : () -> Any\n", skyName))
		skyi.WriteString(fmt.Sprintf("%s _ =\n    %s ()\n\n", skyName, getterName))
		wrap.WriteString(fmt.Sprintf("func %s(_ any) any { return _ffi_pkg.%s }\n\n", getterName, v.Name))
		}
	return skyi.String(), wrap.String()
}

func generateConstAccessor(c ConstDef, pkg, safePkg string) (string, string) {
	skyName := lowerFirst(c.Name)
	wrapperName := fmt.Sprintf("Sky_%s_%s", safePkg, c.Name)

	// Only string and int constants + package-local custom types
	isCustom := strings.HasPrefix(c.Type, pkg+".")
	isPrimitive := c.Type == "string" || c.Type == "int" || c.Type == "int64"

	if !isCustom && !isPrimitive {
		return "", ""
		}

	skyType := "String"
	if c.Type == "int" || c.Type == "int64" {
		skyType = "Int"
		}

	var skyi strings.Builder
	skyi.WriteString(fmt.Sprintf("%s : %s\n", skyName, skyType))
	skyi.WriteString(fmt.Sprintf("%s =\n    %s\n\n", skyName, wrapperName))

	var wrap strings.Builder
	if isCustom {
		wrap.WriteString(fmt.Sprintf("func %s(_ any) any { return string(_ffi_pkg.%s) }\n\n", wrapperName, c.Name))
	} else if c.Type == "int" || c.Type == "int64" {
		wrap.WriteString(fmt.Sprintf("func %s(_ any) any { return _ffi_pkg.%s }\n\n", wrapperName, c.Name))
	} else {
		wrap.WriteString(fmt.Sprintf("func %s(_ any) any { return _ffi_pkg.%s }\n\n", wrapperName, c.Name))
		}
	return skyi.String(), wrap.String()
}

// --- Go wrapper generation ---

func generateGoWrapper(f FuncDef, pkg, safePkg, wrapperName string, isMethod bool, typeName string, ancestors []string, typeKind ...string) (string, []string) {
	var buf strings.Builder
	var imports []string

	// Build param list
	paramNames := []string{}
	if isMethod {
		paramNames = append(paramNames, "receiver")
		}
	for i := range f.Params {
		paramNames = append(paramNames, fmt.Sprintf("arg%d", i))
		}

	buf.WriteString(fmt.Sprintf("func %s(%s) any {\n", wrapperName,
		strings.Join(mapSlice(paramNames, func(n string) string { return n + " any" }), ", ")))

	// All FFI calls are effectful — wrapped in panic recovery, return SkyOk/SkyErr
	hasError := len(f.Results) > 0 && f.Results[len(f.Results)-1].Type == "error"

	buf.WriteString("\treturn func() (ret any) {\n")
	buf.WriteString("\t\tdefer func() { if r := recover(); r != nil { ret = SkyErr(_ffi_fmt.Sprintf(\"FFI panic: %v\", r)) } }()\n")

	// Cast arguments
	isInterface := len(typeKind) > 0 && typeKind[0] == "interface"
	castArgs := []string{}
	if isMethod {
		if isInterface {
			buf.WriteString(fmt.Sprintf("\t\t_receiver := receiver.(_ffi_pkg.%s)\n", typeName))
			castArgs = append(castArgs, "_receiver")
		} else {
			castExpr := generateTypeCast("receiver", "*_ffi_pkg."+typeName, pkg)
			if castExpr != "receiver" {
				buf.WriteString(fmt.Sprintf("\t\t_receiver := %s\n", castExpr))
				castArgs = append(castArgs, "_receiver")
			} else {
				castArgs = append(castArgs, "receiver.(*_ffi_pkg."+typeName+")")
			}
		}
		}

	for i, p := range f.Params {
		argName := fmt.Sprintf("arg%d", i)
		if f.Variadic && i == len(f.Params)-1 {
			// Variadic param: check if element type can be spread directly
			elemType := strings.TrimPrefix(p.Type, "[]")
			canSpread := elemType == "string" || elemType == "byte" || elemType == "int" ||
				elemType == "any" || elemType == "interface{}"
			if canSpread {
				cast := generateTypeCast(argName, p.Type, pkg)
				if cast != argName {
					varName := fmt.Sprintf("_%s", argName)
					buf.WriteString(fmt.Sprintf("\t\t%s := %s\n", varName, cast))
					castArgs = append(castArgs, varName+"...")
				} else {
					castArgs = append(castArgs, argName+"...")
				}
			}
			// else: drop the variadic arg (options are typically optional)
		} else {
			cast := generateTypeCast(argName, p.Type, pkg)
			if cast != argName {
				varName := fmt.Sprintf("_%s", argName)
				buf.WriteString(fmt.Sprintf("\t\t%s := %s\n", varName, cast))
				castArgs = append(castArgs, varName)
			} else {
				castArgs = append(castArgs, argName)
			}
		}
		}

	// Build call
	callExpr := ""
	if isMethod {
		callExpr = fmt.Sprintf("%s.%s(%s)", castArgs[0], f.Name, strings.Join(castArgs[1:], ", "))
	} else {
		callExpr = fmt.Sprintf("_ffi_pkg.%s(%s)", f.Name, strings.Join(castArgs, ", "))
		}

	if hasError && len(f.Results) == 1 {
		// Returns only error
		buf.WriteString(fmt.Sprintf("\t\t_err := %s\n", callExpr))
		buf.WriteString("\t\tif _err != nil { return SkyErr(_err.Error()) }\n")
		buf.WriteString("\t\treturn SkyOk(struct{}{})\n")
	} else if hasError && len(f.Results) == 2 {
		// Returns (T, error)
		buf.WriteString(fmt.Sprintf("\t\t_val, _err := %s\n", callExpr))
		buf.WriteString("\t\tif _err != nil { return SkyErr(_err.Error()) }\n")
		buf.WriteString("\t\treturn SkyOk(_val)\n")
	} else if hasError && len(f.Results) == 3 {
		// Returns (T1, T2, error) → Result String (T1, T2)
		buf.WriteString(fmt.Sprintf("\t\t_r0, _r1, _err := %s\n", callExpr))
		buf.WriteString("\t\tif _err != nil { return SkyErr(_err.Error()) }\n")
		buf.WriteString("\t\treturn SkyOk(SkyTuple2{V0: _r0, V1: _r1})\n")
	} else if len(f.Results) == 0 {
		// Void
		buf.WriteString(fmt.Sprintf("\t\t%s\n", callExpr))
		buf.WriteString("\t\treturn SkyOk(struct{}{})\n")
	} else if len(f.Results) == 2 {
		// Two non-error results → tuple
		buf.WriteString(fmt.Sprintf("\t\t_r0, _r1 := %s\n", callExpr))
		buf.WriteString("\t\treturn SkyOk(SkyTuple2{V0: _r0, V1: _r1})\n")
	} else if len(f.Results) == 3 {
		// Three non-error results → tuple
		buf.WriteString(fmt.Sprintf("\t\t_r0, _r1, _r2 := %s\n", callExpr))
		buf.WriteString("\t\treturn SkyOk(SkyTuple2{V0: _r0, V1: SkyTuple2{V0: _r1, V1: _r2}})\n")
	} else {
		// Single non-error result
		buf.WriteString(fmt.Sprintf("\t\t_val := %s\n", callExpr))
		buf.WriteString("\t\treturn SkyOk(_val)\n")
		}

	buf.WriteString("\t}()\n")
	buf.WriteString("}\n\n")
	return buf.String(), imports
}

func generateTypeCast(varName, goType, pkg string) string {
	switch goType {
	case "string":
		return fmt.Sprintf("sky_asString(%s)", varName)
	case "int":
		return fmt.Sprintf("sky_asInt(%s)", varName)
	case "int64":
		return fmt.Sprintf("sky_asInt64(%s)", varName)
	case "uint64":
		return fmt.Sprintf("uint64(sky_asInt(%s))", varName)
	case "float64":
		return fmt.Sprintf("sky_asFloat(%s)", varName)
	case "float32":
		return fmt.Sprintf("sky_asFloat32(%s)", varName)
	case "byte", "uint8":
		return fmt.Sprintf("byte(sky_asInt(%s))", varName)
	case "rune", "int32":
		return fmt.Sprintf("rune(sky_asInt(%s))", varName)
	case "int8":
		return fmt.Sprintf("int8(sky_asInt(%s))", varName)
	case "int16":
		return fmt.Sprintf("int16(sky_asInt(%s))", varName)
	case "uint16":
		return fmt.Sprintf("uint16(sky_asInt(%s))", varName)
	case "uint32":
		return fmt.Sprintf("uint32(sky_asInt(%s))", varName)
	case "uint":
		return fmt.Sprintf("uint(sky_asInt(%s))", varName)
	case "bool":
		return fmt.Sprintf("sky_asBool(%s)", varName)
	case "[]byte":
		return fmt.Sprintf("sky_asBytes(%s)", varName)
	case "[]string":
		return fmt.Sprintf("sky_asStringSlice(%s)", varName)
	case "error":
		return fmt.Sprintf("sky_asError(%s)", varName)
	case "context.Context":
		return fmt.Sprintf("sky_asContext(%s)", varName)
	case "interface{}", "any":
		return varName
	case "io.Reader":
		return fmt.Sprintf("%s.(io.Reader)", varName)
	case "io.Writer":
		return fmt.Sprintf("%s.(io.Writer)", varName)
	case "io.ReadCloser":
		return fmt.Sprintf("%s.(io.ReadCloser)", varName)
	case "io.WriteCloser":
		return fmt.Sprintf("%s.(io.WriteCloser)", varName)
	case "io.Closer":
		return fmt.Sprintf("%s.(io.Closer)", varName)
	case "io.ReadWriter":
		return fmt.Sprintf("%s.(io.ReadWriter)", varName)
	case "io.ReadSeeker":
		return fmt.Sprintf("%s.(io.ReadSeeker)", varName)
	case "net.Listener":
		return fmt.Sprintf("%s.(net.Listener)", varName)
	case "net.Conn":
		return fmt.Sprintf("%s.(net.Conn)", varName)
		}
	// Function types: type assertion to the concrete function type
	if strings.HasPrefix(goType, "func(") {
		// Replace full package paths with short aliases
		castType := goType
		for _, a := range buildAncestorPkgs(pkg) {
			shortPkg := lastPathSegment(a)
			castType = strings.ReplaceAll(castType, a+".", shortPkg+".")
		}
		castType = strings.ReplaceAll(castType, pkg+".", "_ffi_pkg.")
		return fmt.Sprintf("%s.(%s)", varName, castType)
	}
	// Pointer type: nil-safe cast using import alias
	if strings.HasPrefix(goType, "*") {
		innerType := goType[1:]
		goTypeAlias := goType
		// Replace full package path with _ffi_pkg alias for same-package types
		if strings.HasPrefix(innerType, pkg+".") {
			typeName := innerType[len(pkg)+1:]
			goTypeAlias = "*_ffi_pkg." + typeName
		} else {
			// Check ancestor packages
			for _, a := range buildAncestorPkgs(pkg) {
				if strings.HasPrefix(innerType, a+".") {
					typeName := innerType[len(a)+1:]
					shortPkg := lastPathSegment(a)
					goTypeAlias = "*" + shortPkg + "." + typeName
					break
				}
			}
		}
		return fmt.Sprintf("func() %s { if %s == nil { return nil }; return %s.(%s) }()", goTypeAlias, varName, varName, goTypeAlias)
		}
	// Same-package custom types: use type assertion
	if strings.HasPrefix(goType, pkg+".") {
		typeName := goType[len(pkg)+1:]
		qualType := "_ffi_pkg." + typeName
		return fmt.Sprintf("func() %s { if v, ok := %s.(%s); ok { return v }; var zero %s; return zero }()", qualType, varName, qualType, qualType)
		}
	// Ancestor package types: use type assertion
	for _, a := range buildAncestorPkgs(pkg) {
		if strings.HasPrefix(goType, a+".") {
			typeName := goType[len(a)+1:]
			shortPkg := lastPathSegment(a)
			qualType := shortPkg + "." + typeName
			return fmt.Sprintf("func() %s { if v, ok := %s.(%s); ok { return v }; var zero %s; return zero }()", qualType, varName, qualType, qualType)
		}
		}
	// Typed slices: convert []any to []ElementType
	if strings.HasPrefix(goType, "[]") {
		elemType := goType[2:]
		castElem := elemType
		if strings.HasPrefix(elemType, pkg+".") {
			castElem = "_ffi_pkg." + elemType[len(pkg)+1:]
		} else {
			for _, a := range buildAncestorPkgs(pkg) {
				if strings.HasPrefix(elemType, a+".") {
					castElem = lastPathSegment(a) + "." + elemType[len(a)+1:]
					break
				}
			}
		}
		return fmt.Sprintf("func() []%s { lst := sky_asList(%s); out := make([]%s, len(lst)); for i, v := range lst { if cv, ok := v.(%s); ok { out[i] = cv } }; return out }()", castElem, varName, castElem, castElem)
	}
	// Other: pass through (Go will handle at runtime)
	return varName
}

// --- Type mapping ---

func mapGoTypeToSky(goType, pkg string, ancestors []string) string {
	switch goType {
	case "string":
		return "String"
	case "bool":
		return "Bool"
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		return "Int"
	case "float32", "float64":
		return "Float"
	case "[]byte":
		return "Bytes"
	case "error":
		return "Error"
	case "interface{}", "any":
		return "Any"
	case "[]string":
		return "List String"
	case "[]int":
		return "List Int"
	case "[]float64":
		return "List Float"
	case "[]bool":
		return "List Bool"
	case "rune":
		return "Char"
	case "context.Context":
		return "Context"
		}
	if strings.HasPrefix(goType, "[]") {
		return "List Any"
		}
	if strings.HasPrefix(goType, "map[") {
		return "Any"
		}
	if strings.HasPrefix(goType, "*") {
		inner := goType[1:]
		skyInner := mapGoTypeToSky(inner, pkg, ancestors)
		if isPrimitive(inner) {
			return "Maybe " + skyInner
		}
		return skyInner
		}
	if strings.Contains(goType, ".") {
		parts := strings.Split(goType, ".")
		return parts[len(parts)-1]
		}
	return goType
}

func buildReturnType(results []ParamDef, pkg string, ancestors []string) string {
	if len(results) == 0 {
		return "Result String ()"
		}
	if len(results) == 1 && results[0].Type == "error" {
		return "Result String ()"
		}
	if len(results) == 1 {
		return "Result String " + mapGoTypeToSky(results[0].Type, pkg, ancestors)
		}
	if len(results) == 2 && results[1].Type == "error" {
		return "Result String " + mapGoTypeToSky(results[0].Type, pkg, ancestors)
		}
	if len(results) == 2 {
		t0 := mapGoTypeToSky(results[0].Type, pkg, ancestors)
		t1 := mapGoTypeToSky(results[1].Type, pkg, ancestors)
		return "Result String (" + t0 + ", " + t1 + ")"
		}
	if len(results) == 3 && results[2].Type == "error" {
		t0 := mapGoTypeToSky(results[0].Type, pkg, ancestors)
		t1 := mapGoTypeToSky(results[1].Type, pkg, ancestors)
		return "Result String (" + t0 + ", " + t1 + ")"
		}
	if len(results) == 3 {
		t0 := mapGoTypeToSky(results[0].Type, pkg, ancestors)
		t1 := mapGoTypeToSky(results[1].Type, pkg, ancestors)
		t2 := mapGoTypeToSky(results[2].Type, pkg, ancestors)
		return "Result String (" + t0 + ", " + t1 + ", " + t2 + ")"
		}
	return "Result String Any"
}

func isPrimitive(t string) bool {
	switch t {
	case "string", "int", "int64", "float64", "bool":
		return true
		}
	return false
}

// --- Source scanning ---

func scanSourceForUsedSymbols(alias, srcRoot string) map[string]bool {
	symbols := map[string]bool{}
	pattern := regexp.MustCompile(regexp.QuoteMeta(alias) + `\.(\w+)`)

	filepath.Walk(srcRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".sky") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		matches := pattern.FindAllStringSubmatch(string(data), -1)
		for _, m := range matches {
			if len(m) > 1 {
				symbols[m[1]] = true
				// Also include the PascalCase variant
				symbols[capitalise(m[1])] = true
			}
		}
		return nil
	})
	return symbols
}

func isSymbolUsed(name string, used map[string]bool) bool {
	if len(used) == 0 {
		return true // No filter = include all
		}
	if used[name] || used[lowerFirst(name)] {
		return true
		}
	// For types referenced by field accessors:
	// used symbol "checkoutSessionID" → PascalCase "CheckoutSessionID"
	// type "CheckoutSession" should match because the used symbol STARTS WITH the type name
	for sym := range used {
		pascal := capitalise(sym)
		if len(name) >= 3 && strings.HasPrefix(pascal, name) {
			return true
		}
		// For vars: "setKey" in source → var "Key" in Go
		if strings.HasPrefix(sym, "set") && len(sym) > 3 {
			varName := capitalise(sym[3:])
			if varName == name {
				return true
			}
		}
		// For constructors: "newTypeName" → type "TypeName"
		if strings.HasPrefix(sym, "new") && len(sym) > 3 {
			typeName := capitalise(sym[3:])
			if typeName == name {
				return true
			}
		}
	}
	return false
}

// --- Helpers ---

func (t TypeDef) HasTypeParams(data InspectData) bool {
	// Check if any method has type params
	for _, m := range t.Methods {
		if m.HasTypeParams {
			return true
		}
		}
	return false
}

func pkgToModuleName(pkg string) string {
	parts := strings.Split(pkg, "/")
	var result []string
	for _, part := range parts {
		subParts := strings.Split(part, ".")
		for _, sp := range subParts {
			// Join hyphenated segments as PascalCase within a single part
			// e.g. "stripe-go" → "StripeGo" (one segment)
			hyphenParts := strings.Split(sp, "-")
			var combined string
			for _, hp := range hyphenParts {
				if len(hp) > 0 {
					combined += capitalise(hp)
				}
			}
			if len(combined) > 0 {
				result = append(result, combined)
			}
		}
		}
	return strings.Join(result, ".")
}

func safePkgName(pkg string) string {
	s := strings.ReplaceAll(pkg, ".", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

func extractAlias(pkg string) string {
	// Build the default module name and extract last part
	modName := pkgToModuleName(pkg)
	modParts := strings.Split(modName, ".")
	defaultAlias := modParts[len(modParts)-1]

	// Also try scanning source files for actual import alias
	aliases := []string{defaultAlias}

	// Build all possible aliases from the module path
	// e.g. github.com/stripe/stripe-go/v84 → StripeGo, V84, Stripe
	parts := strings.Split(pkg, "/")
	for _, p := range parts {
		cleaned := strings.ReplaceAll(p, "-", "")
		if len(cleaned) > 0 {
			aliases = append(aliases, capitalise(cleaned))
		}
		}

	// Scan source for "import ... as <Alias>" patterns
	filepath.Walk("src", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".sky") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		// Look for "import <ModuleName> as <Alias>"
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "import ") && strings.Contains(line, " as ") {
				importParts := strings.SplitN(line, " as ", 2)
				if len(importParts) == 2 {
					importedModule := strings.TrimPrefix(strings.TrimSpace(importParts[0]), "import ")
					alias := strings.TrimSpace(importParts[1])
					// Check if this import maps to our package
					if moduleLooksLikePkg(importedModule, pkg) {
						aliases = append([]string{alias}, aliases...) // Prioritise actual alias
					}
				}
			}
		}
		return nil
	})

	return aliases[0]
}

func moduleLooksLikePkg(moduleName, pkg string) bool {
	// Normalise both to lowercase for comparison since casing may differ
	modLower := strings.ToLower(strings.ReplaceAll(moduleName, ".", "/"))
	pkgLower := strings.ToLower(strings.ReplaceAll(pkg, "-", ""))
	pkgLower = strings.ReplaceAll(pkgLower, ".", "/")
	return modLower == pkgLower || strings.HasSuffix(modLower, "/"+pkgLower) ||
		strings.HasPrefix(pkgLower, modLower)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
		}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func capitalise(s string) string {
	if s == "" {
		return s
		}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func lastPathSegment(pkgPath string) string {
	if idx := strings.LastIndex(pkgPath, "/"); idx >= 0 {
		return pkgPath[idx+1:]
		}
	return pkgPath
}

func shortTypeName(goType string) string {
	if idx := strings.LastIndex(goType, "."); idx >= 0 {
		return goType[idx+1:]
		}
	return goType
}

func mapSlice[T, U any](s []T, f func(T) U) []U {
	result := make([]U, len(s))
	for i, v := range s {
		result[i] = f(v)
		}
	return result
}
