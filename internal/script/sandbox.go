package script

import lua "github.com/yuin/gopher-lua"

// removedGlobals are base-library functions that reach the file system or
// the module system, or are debugging internals (spec 13: sandbox).
var removedGlobals = []string{"dofile", "loadfile", "require", "module", "newproxy", "_printregs"}

// newSandbox returns a Lua state with only the base, table, string and math
// libraries. No os, io, debug, package or channel/coroutine extras; no
// function that loads code from files. load and loadstring stay: they take
// code from strings or functions only.
func newSandbox() *lua.LState {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	for _, lib := range []struct {
		name string
		fn   lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		L.Push(L.NewFunction(lib.fn))
		L.Push(lua.LString(lib.name))
		L.Call(1, 0)
	}
	for _, n := range removedGlobals {
		L.SetGlobal(n, lua.LNil)
	}
	return L
}
