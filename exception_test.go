// Copyright 2026 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quickjs // import "modernc.org/quickjs"

import (
	"errors"
	"testing"
	"unsafe"

	"modernc.org/libc"
	lib "modernc.org/libquickjs"
)

// noPendingException fails the test if an exception is left in m's context,
// where the next call that finds one would report it as its own.
func noPendingException(t *testing.T, m *VM) {
	t.Helper()
	if lib.XJS_HasException(m.runtime.tls, m.cContext) != 0 {
		t.Errorf("an exception is pending: %v", m.errFromException())
	}
}

// TestCallLookupError calls functions that evaluating the name of fails.
// Call and CallValue reported "cannot call a non-function" and left the
// exception of the lookup pending.
//
// It also runs under the memgrind allocator (TestMemgrind2).
func TestCallLookupError(t *testing.T) {
	m := newTestVM(t)
	for _, test := range []struct{ function, want string }{
		{"noSuchFunction", "ReferenceError: 'noSuchFunction' is not defined"},
		{"(() => { throw new TypeError('lookup') })()", "TypeError: lookup"},
	} {
		if _, err := m.Call(test.function, 1); err == nil || err.Error() != test.want {
			t.Errorf("Call(%q): got %v, want %s", test.function, err, test.want)
		}
		noPendingException(t, m)

		if _, err := m.CallValue(test.function, 1); err == nil || err.Error() != test.want {
			t.Errorf("CallValue(%q): got %v, want %s", test.function, err, test.want)
		}
		noPendingException(t, m)
	}
}

// TestErrorFromOddValues throws values that reading an error from throws in
// turn. ErrorFromValue left those exceptions pending, where the next call
// finding one reported it instead of its own.
//
// It also runs under the memgrind allocator (TestMemgrind2).
func TestErrorFromOddValues(t *testing.T) {
	m := newTestVM(t)
	for _, test := range []struct {
		js      string
		want    string // what Error returns
		message string
	}{
		{"throw null", "null", ""},
		{"throw undefined", "undefined", ""},
		{"throw 42", "42", ""},
		{"throw Symbol('s')", "", ""},
		{"throw {toString() { throw new Error('inner') }, message: 'm'}", "", "m"},
		{"throw {get message() { throw 1 }, toString() { return 'o' }}", "o", ""},
	} {
		_, err := m.Eval(test.js, EvalGlobal)
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("%s: got %v, want an *Error", test.js, err)
		}

		if e.Error() != test.want || e.Message != test.message {
			t.Errorf("%s: got %q and message %q, want %q and message %q", test.js, e.Error(), e.Message, test.want, test.message)
		}
		noPendingException(t, m)
	}
}

// TestBigIntToStringThrows converts a BigInt whose toString throws. That used
// to be an error the conversion made up, with the exception left pending.
func TestBigIntToStringThrows(t *testing.T) {
	m := newTestVM(t)
	_, err := m.Eval("BigInt.prototype.toString = function() { throw new Error('no digits') }; 1n", EvalGlobal)
	if err == nil || err.Error() != "Error: no digits" {
		t.Errorf("got %v, want Error: no digits", err)
	}
	noPendingException(t, m)
}

// TestStringOutOfMemory takes a string to Go that there is no memory to
// convert. A string with non-ASCII characters needs a UTF-8 copy, which can
// fail near the memory limit. That used to give "" without an error, and leave
// the out of memory error pending.
func TestStringOutOfMemory(t *testing.T) {
	m := newTestVM(t)
	// 3M characters take 6 MB as UTF-16, and the UTF-8 copy needs 9 MB.
	if _, err := m.Eval("globalThis.s = '€'.repeat(3 << 20); 0", EvalGlobal); err != nil {
		t.Fatal(err)
	}

	// The memory a VM uses depends on the platform, so the limit is set
	// from what it uses now. 1 MB more leaves memory to evaluate a script,
	// but not to convert 's'.
	tls := m.runtime.tls
	p := libc.Xcalloc(tls, 1, libc.Tsize_t(unsafe.Sizeof(lib.TJSMemoryUsage{})))
	defer libc.Xfree(tls, p)

	lib.XJS_ComputeMemoryUsage(tls, m.runtime.cRuntime, p)
	m.SetMemoryLimit(uintptr((*lib.TJSMemoryUsage)(unsafe.Pointer(p)).Fmalloc_size) + 1<<20)
	if v, err := m.Eval("s.length", EvalGlobal); err != nil || v != 3<<20 {
		t.Fatalf("s.length: got %v, %v", v, err)
	}

	v, err := m.Eval("s", EvalGlobal)
	if err == nil || err.Error() != "InternalError: out of memory" {
		t.Errorf("got %d bytes and %v, want InternalError: out of memory", len(v.(string)), err)
	}
	noPendingException(t, m)
}
