// Copyright 2026 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quickjs // import "modernc.org/quickjs"

import (
	"strings"
	"testing"
	"time"
)

// panicValue is what the tests panic with, a pointer so that a recovered value
// can be compared with the one thrown.
type panicValue struct{ where string }

// recovered runs f and returns what it panicked with.
func recovered(f func()) (p any) {
	defer func() { p = recover() }()

	f()
	return nil
}

// vmWorks fails the test if m cannot make an error with a stack, or if the
// eval timeout does not stop a script. A panic that unwound through the
// engine left it pointing at stack frames that were gone: the next error
// walked them, forever or into a crash, and the timeout never fired.
func vmWorks(t *testing.T, m *VM) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		defer close(done)

		_, err := m.Eval("function thrower() { throw new Error('x') }; thrower()", EvalGlobal)
		if err == nil || !strings.Contains(err.(*Error).Stack, "thrower") {
			done <- err
			return
		}

		m.SetEvalTimeout(50 * time.Millisecond)
		defer m.SetEvalTimeout(0)
		if _, err := m.Eval("for (;;);", EvalGlobal); err == nil || err.Error() != "InternalError: interrupted" {
			done <- err
		}
	}()

	select {
	case err, ok := <-done:
		if ok {
			t.Fatalf("after the panic: got %v from a throw, then a timeout", err)
		}
	case <-time.After(10 * time.Second):
		m.Interrupt()
		t.Fatal("after the panic, an error with a stack or the eval timeout did not return in 10s")
	}
}

// TestHostPanic panics in Go code that Javascript calls. The panic used to
// unwind through the engine (see vmWorks). Now the Javascript ends without
// running any of its catch or finally blocks, as for an interrupt, and the
// panic goes on with the same value in the Go code that called Javascript.
//
// It also runs under the memgrind allocator (TestMemgrind2).
func TestHostPanic(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*VM, *panicValue) error
	}{
		{"RegisterFunc", func(m *VM, p *panicValue) error {
			return m.RegisterFunc("boom", func(...any) { panic(p) }, false)
		}},
		{"RegisterHostFunc", func(m *VM, p *panicValue) error {
			return m.RegisterHostFunc("boom", func([]any) (any, error) { panic(p) })
		}},
		{"RegisterFunc with arguments", func(m *VM, p *panicValue) error {
			return m.RegisterFunc("boom", func(s string, n int) string { panic(p) }, false)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newTestVM(t)
			p := &panicValue{test.name}
			if err := test.setup(m, p); err != nil {
				t.Fatal(err)
			}

			if _, err := m.Eval(`
globalThis.seen = [];
globalThis.run = function() {
	try {
		boom('x', 1);
		seen.push('after');
	} catch (e) {
		seen.push('catch');
	} finally {
		seen.push('finally');
	}
}`, EvalGlobal); err != nil {
				t.Fatal(err)
			}

			for _, call := range []struct {
				name string
				f    func() error
			}{
				{"Eval", func() error { _, err := m.Eval("run()", EvalGlobal); return err }},
				{"Call", func() error { _, err := m.Call("run"); return err }},
				{"async Eval", func() error { _, err := m.Eval("(async () => run())()", EvalGlobal); return err }},
			} {
				if g := recovered(func() { call.f() }); g != p {
					t.Fatalf("%s: recovered %v, want the panic of the Go function", call.name, g)
				}

				noPendingException(t, m)
				if v, err := m.Eval("seen.join()", EvalGlobal); err != nil || v != "" {
					t.Fatalf("%s: page code saw the panic: %v, %v", call.name, v, err)
				}
			}
			vmWorks(t, m)
		})
	}
}

// TestHostPanicNested panics in a Go function that Javascript called from a
// Go function that Javascript called. The panic goes on in the outer Go
// function, out of its Eval, and then in the Go code that called the outer
// Javascript.
func TestHostPanicNested(t *testing.T) {
	m := newTestVM(t)
	p := &panicValue{"inner"}
	if err := m.RegisterFunc("boom", func() { panic(p) }, false); err != nil {
		t.Fatal(err)
	}

	var inner any
	if err := m.RegisterFunc("outer", func() {
		inner = recovered(func() { m.Eval("boom()", EvalGlobal) })
		panic(inner)
	}, false); err != nil {
		t.Fatal(err)
	}

	if g := recovered(func() { m.Eval("try { outer() } catch (e) { globalThis.caught = true }", EvalGlobal) }); g != p || inner != p {
		t.Fatalf("recovered %v outside and %v inside, want the panic of the inner Go function", g, inner)
	}

	if v, err := m.Eval("typeof caught", EvalGlobal); err != nil || v != "undefined" {
		t.Fatalf("page code saw the panic: %v, %v", v, err)
	}

	noPendingException(t, m)
	vmWorks(t, m)
}

// TestHostPanicThen panics in a Go callback of a promise, which runs as a job.
// The promise machinery takes the exception thrown in place of the panic, but
// the panic goes on all the same, and the jobs after it do not run. The engine
// calls the callback without a Javascript frame on the stack, so unwinding
// through it happened to be harmless, but the panic must still get out.
func TestHostPanicThen(t *testing.T) {
	m := newTestVM(t)
	p := &panicValue{"Then"}
	pc, err := m.NewPromiseCapability()
	if err != nil {
		t.Fatal(err)
	}

	defer pc.Free()

	next, err := pc.Promise.Then(func(Value) { panic(p) }, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer next.Free()

	if _, err := m.Eval("Promise.resolve().then(() => {}).then(() => { globalThis.later = true })", EvalGlobal); err != nil {
		t.Fatal(err)
	}

	if _, err := pc.Resolve.Call(UndefinedValue, 1); err != nil {
		t.Fatal(err)
	}

	if g := recovered(func() { m.ExecutePendingJobs() }); g != p {
		t.Fatalf("recovered %v, want the panic of the callback", g)
	}

	if v, err := m.Eval("typeof later", EvalGlobal); err != nil || v != "undefined" {
		t.Fatalf("a job after the panic ran: %v, %v", v, err)
	}

	noPendingException(t, m)
	vmWorks(t, m)
}

// TestHostPanicModuleLoader panics in a module loader and in a module
// normalizer. As for TestHostPanicThen, the engine calls them without a
// Javascript frame on the stack, and the panic must still get out.
func TestHostPanicModuleLoader(t *testing.T) {
	for _, normalizer := range []bool{false, true} {
		m := newTestVM(t)
		p := &panicValue{"loader"}
		loader := func(*VM, string) (string, error) { panic(p) }
		var normalize ModuleNormalizeFunc
		if normalizer {
			p = &panicValue{"normalizer"}
			loader = func(*VM, string) (string, error) { return "", nil }
			normalize = func(*VM, string, string) (string, error) { panic(p) }
		}
		m.SetModuleLoader(loader, normalize)

		if g := recovered(func() { m.Eval(`import "x";`, EvalModule) }); g != p {
			t.Fatalf("%s: recovered %v, want the panic of the %[1]s", p.where, g)
		}

		noPendingException(t, m)
		vmWorks(t, m)
	}
}
