// Copyright 2026 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quickjs // import "modernc.org/quickjs"

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// codePoints returns the code points of 's' as Go decodes it, written as the
// Javascript function cps of TestGoInvalidUTF8 writes them.
func codePoints(s string) string {
	var a []string
	for _, r := range s {
		a = append(a, fmt.Sprintf("%x", r))
	}
	return strings.Join(a, " ")
}

// TestGoInvalidUTF8 passes Go strings that are not valid UTF-8 to Javascript,
// every way a string goes there. Each byte that does not start a valid UTF-8
// sequence becomes U+FFFD, as Go decodes the string and as encoding/json,
// which converts a struct, writes it. The engine used to skip the bytes after
// an invalid one, ASCII included, and took the three-byte form of a surrogate
// (CESU-8) for that surrogate, so two of them made an emoji.
//
// It also runs under the memgrind allocator (TestMemgrind2).
func TestGoInvalidUTF8(t *testing.T) {
	m := newTestVM(t)
	if _, err := m.Eval("globalThis.cps = s => Array.from(s, c => c.codePointAt(0).toString(16)).join(' ')", EvalGlobal); err != nil {
		t.Fatal(err)
	}

	for _, s := range []string{
		"\x80a",
		"a\xc0\x80b",
		"\xed\xa0\xbd\xed\xb8\x80", // U+1F600 as two surrogates
		"\xe4\xb8",                 // a sequence cut short
		"\xff",
		"ok € \U0001F600", // valid, unchanged
	} {
		want := codePoints(s)
		check := func(how string, v any, err error) {
			t.Helper()
			if err != nil || v != want {
				t.Errorf("%q %s: got %v, %v, want %s", s, how, v, err, want)
			}
		}

		v, err := m.Call("cps", s)
		check("as an argument", v, err)

		v, err = m.Call("o => cps(o.S)", struct{ S string }{s})
		check("in a struct", v, err)

		v, err = m.Call("o => cps(Object.keys(o)[0])", map[string]string{s: ""})
		check("as a map key", v, err)

		v, err = m.Call("o => cps(o.k)", map[string]string{"k": s})
		check("as a map value", v, err)

		js, err := m.NewString(s)
		if err != nil {
			t.Fatal(err)
		}

		v, err = m.Call("cps", js)
		js.Free()
		check("from NewString", v, err)

		atom, err := m.NewAtom(s)
		if err != nil {
			t.Fatal(err)
		}

		o, err := m.EvalValue("({})", EvalGlobal)
		if err != nil {
			t.Fatal(err)
		}

		if err := o.SetProperty(atom, 1); err != nil {
			t.Fatal(err)
		}

		v, err = m.Call("o => cps(Object.keys(o)[0])", o)
		o.Free()
		check("from NewAtom", v, err)
	}
}

// TestGoInvalidUTF8FromGoFunc returns invalid UTF-8 from Go functions, and
// throws it as the message of an error.
func TestGoInvalidUTF8FromGoFunc(t *testing.T) {
	m := newTestVM(t)
	const s = "\x80a"
	want := codePoints(s)
	if err := m.RegisterFunc("goResult", func() string { return s }, false); err != nil {
		t.Fatal(err)
	}

	if err := m.RegisterHostFunc("hostResult", func([]any) (any, error) { return s, nil }); err != nil {
		t.Fatal(err)
	}

	if err := m.RegisterHostFunc("hostError", func([]any) (any, error) { return nil, errors.New(s) }); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Eval("globalThis.cps = s => Array.from(s, c => c.codePointAt(0).toString(16)).join(' ')", EvalGlobal); err != nil {
		t.Fatal(err)
	}

	for _, js := range []string{
		"cps(goResult())",
		"cps(hostResult())",
		"try { hostError() } catch (e) { cps(e.message) }",
	} {
		if v, err := m.Eval(js, EvalGlobal); err != nil || v != want {
			t.Errorf("%s: got %v, %v, want %s", js, v, err, want)
		}
	}
}

// TestJSLoneSurrogate takes Javascript strings with lone surrogates to Go,
// every way a string goes there. A Javascript string is UTF-16 and can hold
// one half of a surrogate pair without the other. It used to reach Go as the
// three bytes of the surrogate's code point, which is not UTF-8. Now each lone
// surrogate becomes U+FFFD, as Go's utf16.Decode, a browser's TextEncoder and
// Node's Buffer make it.
//
// It also runs under the memgrind allocator (TestMemgrind2).
func TestJSLoneSurrogate(t *testing.T) {
	m := newTestVM(t)
	var arg string
	if err := m.RegisterFunc("take", func(s string) { arg = s }, false); err != nil {
		t.Fatal(err)
	}

	var module string
	m.SetModuleLoader(func(_ *VM, name string) (string, error) {
		module = name
		return "", nil
	}, nil)
	for _, test := range []struct{ js, want string }{
		{`a\ud83db`, "a�b"},
		{`\ude00`, "�"},
		{`x\udfff`, "x�"},
		{`\ud83d\ud83d`, "��"},
		{`😀`, "\U0001F600"}, // a pair, one character
		{`􏿿\udc00`, "\U0010FFFF�"},
	} {
		check := func(how string, g any, err error) {
			t.Helper()
			if s, ok := g.(string); err != nil || !ok || s != test.want || !utf8.ValidString(s) {
				t.Errorf("'%s' %s: got %q, %v, want %q", test.js, how, g, err, test.want)
			}
		}

		v, err := m.Eval("'"+test.js+"'", EvalGlobal)
		check("as a result", v, err)

		_, err = m.Eval("take('"+test.js+"')", EvalGlobal)
		check("as an argument", arg, err)

		_, err = m.Eval("throw new Error('"+test.js+"')", EvalGlobal)
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("got %v, want an *Error", err)
		}

		check("as an error message", e.Message, nil)

		v, err = m.Eval("({s: '"+test.js+"'})", EvalGlobal)
		var o struct{ S string }
		if err == nil {
			err = v.(*Object).Into(&o)
		}
		check("in an object", o.S, err)

		_, err = m.Eval(`import "`+test.js+`";`, EvalModule)
		check("as a module name", module, err)
	}
}
