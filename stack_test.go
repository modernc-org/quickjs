// Copyright 2026 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quickjs // import "modernc.org/quickjs"

import (
	"testing"

	lib "modernc.org/libquickjs"
)

// TestStackLimit checks that a new VM stops deep recursion with a catchable
// error. JS_NewRuntime set the stack size to 1 MB, which the stack check of
// the port reads as a million libc.TLS stack slots, so recursing without end
// overflowed the goroutine stack first: a fatal error that ends the process.
// A regression may so show as a crash of the test binary rather than as a
// failure. Nesting too deep in source code or in JSON hits the same limit.
//
// It also runs under the memgrind allocator (TestMemgrind2).
func TestStackLimit(t *testing.T) {
	m := newTestVM(t)
	for _, test := range []struct{ js, want string }{
		{"function f() { return f() + 1 }; f()", "InternalError: stack overflow"},
		{"eval('['.repeat(1e5))", "SyntaxError: stack overflow"},
		{"JSON.parse('['.repeat(1e5))", "SyntaxError: stack overflow"},
		{"let o = {}; for (let i = 0; i < 1e5; i++) o = {o}; JSON.stringify(o)", "InternalError: stack overflow"},
	} {
		v, err := m.Eval("try { "+test.js+"; 'no error' } catch (e) { String(e) }", EvalGlobal)
		if err != nil || v != test.want {
			t.Errorf("%s: got %v, %v, want %s", test.js, v, err, test.want)
		}
	}
}

// TestSetMaxStackSize checks the depth the stack limit lets a recursion reach:
// about one libc.TLS stack slot per call, so a bit less than the limit.
func TestSetMaxStackSize(t *testing.T) {
	m := newTestVM(t)
	depth := func() int {
		v, err := m.Eval("{ let d = 0; function f() { d++; f() }; try { f() } catch (e) {}; d }", EvalGlobal)
		if err != nil {
			t.Fatal(err)
		}

		return v.(int)
	}

	check := func(what string, max int) {
		if g := depth(); g > max || g <= max/2 {
			t.Errorf("%s: recursed %d deep, want at most %d and more than %d", what, g, max, max/2)
		}
	}

	check("new VM", lib.MaxStackSlots)
	m.SetMaxStackSize(200)
	check("SetMaxStackSize(200)", 200)
	m.SetMaxStackSize(0)
	check("SetMaxStackSize(0)", lib.MaxStackSlots)
}
