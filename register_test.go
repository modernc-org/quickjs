// Copyright 2026 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quickjs // import "modernc.org/quickjs"

import (
	"strings"
	"testing"
)

// registered returns the number of Go functions registered in the process.
func registered() int {
	goFuncsMu.Lock()
	defer goFuncsMu.Unlock()

	return len(goFuncs)
}

// TestRegisterFuncExistingName registers Go functions under names the global
// object already has. JS_SetPropertyFunctionList, which RegisterFunc used,
// aborted the process for every one of them, so a regression may show as the
// test binary dying of SIGABRT.
//
// It also runs under the memgrind allocator (TestMemgrind2).
func TestRegisterFuncExistingName(t *testing.T) {
	m := newTestVM(t)
	if _, err := m.Eval("globalThis.assigned = 1; var declared = 1; function declaredFunc() {}", EvalGlobal); err != nil {
		t.Fatal(err)
	}

	// A configurable property is replaced, by a function with the
	// attributes a new one gets.
	for i, name := range []string{"assigned", "Array", "Math", "parseInt"} {
		var err error
		switch want := "go " + name; {
		case i%2 == 0:
			err = m.RegisterFunc(name, func() string { return want }, false)
		default:
			err = m.RegisterHostFunc(name, func([]any) (any, error) { return want, nil })
		}
		if err != nil {
			t.Fatalf("registering %s: %v", name, err)
		}

		if v, err := m.Eval(name+"()", EvalGlobal); err != nil || v != "go "+name {
			t.Errorf("%s(): got %v, %v, want %q", name, v, err, "go "+name)
		}

		const want = `{"writable":true,"enumerable":false,"configurable":true}`
		v, err := m.Eval("JSON.stringify(Object.getOwnPropertyDescriptor(globalThis, '"+name+"'), ['writable', 'enumerable', 'configurable'])", EvalGlobal)
		if err != nil || v != want {
			t.Errorf("%s: got attributes %v, %v, want %s", name, v, err, want)
		}
	}

	// A property that is not configurable stays, and registering it fails
	// without keeping a Go function.
	for _, name := range []string{"undefined", "NaN", "Infinity", "declared", "declaredFunc"} {
		before, err := m.Eval("String(globalThis['"+name+"'])", EvalGlobal)
		if err != nil {
			t.Fatal(err)
		}

		n := registered()
		if err := m.RegisterFunc(name, func() int { return 2 }, false); err == nil || !strings.Contains(err.Error(), "not configurable") {
			t.Errorf("registering %s: got error %v, want one saying it is not configurable", name, err)
		}
		if g := registered(); g != n {
			t.Errorf("registering %s failed, but the process has %d Go functions registered, want %d", name, g, n)
		}
		if g, err := m.Eval("String(globalThis['"+name+"'])", EvalGlobal); err != nil || g != before {
			t.Errorf("%s after a failed register: got %v, %v, want %v", name, g, err, before)
		}
	}

	// A name taken by a Go function before cannot be registered again.
	if err := m.RegisterFunc("assigned", func() int { return 3 }, false); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("registering assigned again: got error %v, want func already registered", err)
	}

	// Names are what Javascript sees: invalid UTF-8 becomes U+FFFD, so the
	// second name is the first one.
	if err := m.RegisterFunc("\xc0\x80", func() int { return 4 }, false); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterFunc("\xe4\xb8", func() int { return 5 }, false); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf(`registering "\xe4\xb8" after "\xc0\x80": got error %v, want func already registered`, err)
	}
	if v, err := m.Eval(`globalThis["��"]()`, EvalGlobal); err != nil || v != 4 {
		t.Errorf(`globalThis["��"](): got %v, %v, want 4`, v, err)
	}

	// A NUL byte would end the name.
	if err := m.RegisterFunc("Array\x00x", func() int { return 6 }, false); err == nil {
		t.Errorf("registering a name with a NUL byte: no error")
	}

	// A name in brackets is a string like any other.
	if err := m.RegisterFunc("[Symbol.iterator]", func() int { return 7 }, false); err != nil {
		t.Fatal(err)
	}
	if v, err := m.Eval(`globalThis["[Symbol.iterator]"]() + ' ' + typeof globalThis[Symbol.iterator]`, EvalGlobal); err != nil || v != "7 undefined" {
		t.Errorf("[Symbol.iterator]: got %v, %v, want 7 undefined", v, err)
	}
}

// TestRegisterFuncNotExtensible registers a Go function on a global object
// that does not take new properties.
func TestRegisterFuncNotExtensible(t *testing.T) {
	m := newTestVM(t)
	if _, err := m.Eval("Object.preventExtensions(globalThis)", EvalGlobal); err != nil {
		t.Fatal(err)
	}

	n := registered()
	if err := m.RegisterFunc("fresh", func() int { return 1 }, false); err == nil {
		t.Errorf("registering on a global object that is not extensible: no error")
	}
	if g := registered(); g != n {
		t.Errorf("registering failed, but the process has %d Go functions registered, want %d", g, n)
	}
	if v, err := m.Eval("typeof fresh", EvalGlobal); err != nil || v != "undefined" {
		t.Errorf("typeof fresh: got %v, %v, want undefined", v, err)
	}
}
