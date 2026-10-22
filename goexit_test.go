// Copyright 2026 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quickjs // import "modernc.org/quickjs"

import (
	goruntime "runtime"
	"testing"
	"unsafe"

	"modernc.org/libc"
	lib "modernc.org/libquickjs"
)

// downJS defines down(n), which recurses n levels deep, each level keeping a
// closure over its own variables in keep, and then calls bottom(). A
// Javascript frame that bottom abandons holds such closures, which pointed at
// memory that was freed: after a Goexit, a closure read a variable of a frame
// that ran later.
const downJS = `
globalThis.keep = [];
globalThis.down = function(n) {
	let x = {n};
	keep.push(() => x);
	return n ? [n].map(k => down(k - 1))[0] : bottom();
}`

// goexited runs f on a new goroutine and reports whether runtime.Goexit ended
// it.
func goexited(f func()) bool {
	exited := true
	done := make(chan struct{})
	go func() {
		defer close(done)

		f()
		exited = false
	}()
	<-done
	return exited
}

// onlyCloses checks that m was abandoned: calls into Javascript and the other
// methods that use the engine panic with abandonedPanic, Value.Free does
// nothing, and Close drops every Go function of m, also those the engine would
// have finalized.
func onlyCloses(t *testing.T, m *VM, v Value) {
	t.Helper()
	if !m.abandoned {
		t.Fatal("the VM was not abandoned")
	}

	for _, c := range []struct {
		name string
		f    func()
	}{
		{"Eval", func() { m.Eval("1", EvalGlobal) }},
		{"Call", func() { m.Call("down", 1) }},
		{"Value.Any", func() { v.Any() }},
		{"RegisterFunc", func() { m.RegisterFunc("later", func() {}, false) }},
		{"NewObjectValue", func() { m.NewObjectValue() }},
		{"NewString", func() { m.NewString("s") }},
		{"Compile", func() { m.Compile("1", EvalGlobal) }},
		{"EvalBytecode", func() { m.EvalBytecode(nil) }},
		{"Then", func() { v.Then(func(Value) {}, nil) }},
	} {
		if p := recovered(c.f); p != abandonedPanic {
			t.Errorf("%s: recovered %v, want %q", c.name, p, abandonedPanic)
		}
	}
	if p := recovered(func() { v.Free() }); p != nil {
		t.Errorf("Value.Free: panicked with %v", p)
	}

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	goFuncsMu.Lock()
	defer goFuncsMu.Unlock()

	for magic, f := range goFuncs {
		if f.vm == m {
			t.Errorf("Go function %s (magic %d) outlived Close", f.name, magic)
		}
	}
}

// newDownVM returns a VM with down (see downJS) and a Value of keep.
func newDownVM(t *testing.T) (*VM, Value) {
	t.Helper()
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := m.Eval(downJS, EvalGlobal); err != nil {
		t.Fatal(err)
	}

	keep, err := m.EvalValue("keep", EvalGlobal)
	if err != nil {
		t.Fatal(err)
	}

	return m, keep
}

// TestGoexit calls runtime.Goexit, as t.Fatal does, in Go code that
// Javascript called. Goexit cannot be recovered: it went up through the
// engine, which kept pointing at the frames it abandoned. The next error
// walked them for its stack, and closures over their variables read and wrote
// the frames that ran later. Now the VM can only be closed.
func TestGoexit(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*VM)
	}{
		{"RegisterFunc", func(m *VM) {
			m.RegisterFunc("bottom", goruntime.Goexit, false)
			m.Eval("down(20)", EvalGlobal)
		}},
		{"RegisterHostFunc", func(m *VM) {
			m.RegisterHostFunc("bottom", func([]any) (any, error) { goruntime.Goexit(); return nil, nil })
			m.Call("down", 20)
		}},
		{"nested", func(m *VM) {
			m.RegisterFunc("bottom", goruntime.Goexit, false)
			m.RegisterFunc("nested", func() { m.Eval("down(20)", EvalGlobal) }, false)
			m.Eval("[1, 2].map(() => nested())", EvalGlobal)
		}},
		{"Then", func(m *VM) {
			pc, err := m.NewPromiseCapability()
			if err != nil {
				panic(err)
			}

			next, err := pc.Promise.Then(func(Value) { goruntime.Goexit() }, nil)
			if err != nil {
				panic(err)
			}

			pc.Resolve.Call(UndefinedValue, 1)
			m.ExecutePendingJobs()
			next.Free()
			pc.Free()
		}},
		{"module loader", func(m *VM) {
			m.SetModuleLoader(func(*VM, string) (string, error) { goruntime.Goexit(); return "", nil }, nil)
			m.Eval(`import "x";`, EvalModule)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, keep := newDownVM(t)
			if !goexited(func() { test.run(m) }) {
				t.Fatal("the goroutine returned")
			}

			onlyCloses(t, m, keep)
		})
	}
}

// enginePanic is what rawPanic panics with.
var enginePanic = &panicValue{"engine"}

// rawPanic is a native function that panics. Unlike a Go function of
// RegisterFunc, the engine calls it as one of its own, so its panic goes up
// through the engine as one in the engine itself would.
func rawPanic(tls *libc.TLS, ctx uintptr, this lib.TJSValue, argc int32, argv uintptr, magic int32) lib.TJSValue {
	panic(enginePanic)
}

var bottomName = [...]byte{'b', 'o', 't', 't', 'o', 'm', 0}

// defineRawPanic defines the global bottom as rawPanic.
func defineRawPanic(t *testing.T, m *VM) {
	t.Helper()
	tls := m.runtime.tls
	ctx := m.cContext
	name := uintptr(unsafe.Pointer(&bottomName[0]))
	g := lib.XJS_GetGlobalObject(tls, ctx)
	f := lib.XJS_NewCFunction2(tls, ctx, fp(rawPanic), name, 0, lib.EJS_CFUNC_generic_magic, 0)
	if lib.XJS_SetPropertyStr(tls, ctx, g, name, f) < 0 {
		t.Fatal("cannot define bottom")
	}

	lib.XFreeValue(tls, ctx, g)
}

// TestEnginePanic panics in the engine, which abandons the frames above the
// Go code that recovers the panic, wherever that is. As for runtime.Goexit,
// the VM can then only be closed.
func TestEnginePanic(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*VM)
		want any // what the Go code that called Javascript recovers
	}{
		{"Eval", func(m *VM) { m.Eval("down(20)", EvalGlobal) }, enginePanic},
		{"nested", func(m *VM) {
			m.RegisterFunc("nested", func() { m.Eval("down(20)", EvalGlobal) }, false)
			m.Eval("[1, 2].map(() => nested())", EvalGlobal)
		}, enginePanic},
		{"recovered", func(m *VM) {
			m.RegisterFunc("nested", func() string {
				defer func() { recover() }()

				m.Eval("down(20)", EvalGlobal)
				return "unreachable"
			}, false)
			m.Eval("try { [1, 2].map(() => nested()) } catch (e) {}", EvalGlobal)
		}, abandonedPanic},
		{"recovered and evaluated", func(m *VM) {
			m.RegisterFunc("nested", func() {
				recovered(func() { m.Eval("down(20)", EvalGlobal) })
				if p := recovered(func() { m.Eval("1", EvalGlobal) }); p != abandonedPanic {
					t.Errorf("Eval after the recovered panic: recovered %v, want %q", p, abandonedPanic)
				}
			}, false)
			m.Eval("try { [1, 2].map(() => nested()) } catch (e) {}", EvalGlobal)
		}, abandonedPanic},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, keep := newDownVM(t)
			defineRawPanic(t, m)
			if p := recovered(func() { test.run(m) }); p != test.want {
				t.Fatalf("recovered %v, want %v", p, test.want)
			}

			onlyCloses(t, m, keep)
		})
	}
}

// TestAbandonedFree checks the deferred frees of the Go code that called
// Javascript. They run while a Go panic or runtime.Goexit goes up through the
// engine, before leave sees it, and must not run engine code then. VM.Call
// frees that way the function it evaluates from its name, and bottom keeps a
// reference count that nothing else changes.
func TestAbandonedFree(t *testing.T) {
	for _, test := range []struct {
		name   string
		define func(*testing.T, *VM)
		ended  func(func()) bool // reports whether the call ended as it must
	}{
		{"Goexit", func(t *testing.T, m *VM) { m.RegisterFunc("bottom", goruntime.Goexit, false) }, goexited},
		{"engine panic", defineRawPanic, func(f func()) bool { return recovered(f) == enginePanic }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, keep := newDownVM(t)
			test.define(t, m)
			bottom, err := m.EvalValue("bottom", EvalGlobal)
			if err != nil {
				t.Fatal(err)
			}

			// The JSRefCountHeader comes right before the object.
			refs := (*int32)(unsafe.Pointer(jsvToPtr(bottom.v) - unsafe.Sizeof(lib.TJSRefCountHeader{})))
			want := *refs + 1 // what Call took
			if !test.ended(func() { m.Call("bottom") }) {
				t.Fatal("Call returned")
			}

			if *refs != want {
				t.Errorf("reference count %d, want %d", *refs, want)
			}

			onlyCloses(t, m, keep)
		})
	}
}
