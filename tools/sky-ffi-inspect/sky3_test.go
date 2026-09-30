package main

import (
	"go/token"
	"go/types"
	"testing"
)

// The format-3 classification the runtime converter (rt/ffi_convert.go) and
// the checker agree on.
func TestSky3Classification(t *testing.T) {
	pkg := types.NewPackage("example.com/gopk", "gopk")
	named := func(name string, u types.Type) *types.Named {
		return types.NewNamed(types.NewTypeName(token.NoPos, pkg, name, nil), u, nil)
	}
	thing := named("Thing", types.NewStruct(nil, nil))
	thing.SetUnderlying(types.NewStruct([]*types.Var{types.NewField(token.NoPos, pkg, "N", types.Typ[types.Int], false)}, nil))
	dur := named("Dur", types.Typ[types.Int64])
	writer := named("Writer", types.NewInterfaceType([]*types.Func{
		types.NewFunc(token.NoPos, pkg, "Write", types.NewSignatureType(nil, nil, nil, nil, nil, false)),
	}, nil).Complete())
	value := named("Value", types.NewInterfaceType(nil, nil).Complete())
	str := types.Typ[types.String]
	cb := types.NewSignatureType(nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.Typ[types.Uint64])),
		types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.Universe.Lookup("error").Type())), false)
	cases := []struct {
		t    types.Type
		dir  sky3Dir
		top  bool
		want string
	}{
		{types.NewPointer(str), dirOut, false, "Maybe String"},
		{types.NewPointer(thing), dirOut, false, "Thing@example.com/gopk"},
		{types.NewSlice(types.NewPointer(types.Typ[types.Int])), dirOut, false, "List (Maybe Int)"},
		{types.NewMap(types.Typ[types.Int], str), dirOut, false, "Dict Int String"},
		{types.NewMap(thing, str), dirOut, false, "@map"},
		{types.Typ[types.Uint64], dirIn, true, "Int"},
		{dur, dirIn, true, "Int"},
		{types.NewArray(types.Typ[types.Byte], 32), dirOut, false, "Bytes"},
		{types.NewArray(str, 2), dirOut, false, "List String"},
		{writer, dirIn, true, "iface:Writer@example.com/gopk"},
		{writer, dirOut, false, "Writer@example.com/gopk"},
		{value, dirIn, true, "any"},
		{cb, dirIn, true, "(Int -> Result Error ())"},
		{cb, dirOut, false, "@func"},
		{types.NewSignatureType(nil, nil, nil, nil, nil, false), dirIn, true, "(() -> ())"},
		{types.Typ[types.Complex128], dirOut, false, "@complex"},
		{named("T", types.NewStruct(nil, nil)), dirOut, false, "T@example.com/gopk"},
	}
	for _, c := range cases {
		if got := sky3Of(c.t, c.dir, c.top); got != c.want {
			t.Errorf("sky3Of(%s) = %q, want %q", c.t, got, c.want)
		}
	}
}
