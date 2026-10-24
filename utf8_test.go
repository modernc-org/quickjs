// Copyright 2026 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quickjs // import "modernc.org/quickjs"

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"
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

// codeUnits returns the UTF-16 code units of 's' as Go decodes it, written as
// the Javascript function cus of TestWTF8 writes them.
func codeUnits(s string) string {
	var a []string
	for _, u := range utf16.Encode([]rune(s)) {
		a = append(a, fmt.Sprintf("%x", u))
	}
	return strings.Join(a, " ")
}

// TestWTF8 sets a VM to keep lone surrogates, SetWTF8, and takes strings
// between Go and Javascript every way a string goes. A lone surrogate reaches
// Go as the three bytes of its code point and comes back as that code unit,
// and two of them, a high and a low one, make a pair. Any other invalid byte
// from Go still becomes U+FFFD, and so does a lone surrogate in a string that
// goes through encoding/json.
//
// It also runs under the memgrind allocator (TestMemgrind2).
func TestWTF8(t *testing.T) {
	m := newTestVM(t)
	m.SetWTF8(true)
	var ret, arg string
	var args []any
	for _, f := range []struct {
		name string
		f    func([]any) (any, error)
	}{
		{"hostResult", func([]any) (any, error) { return ret, nil }},
		{"hostKey", func([]any) (any, error) { return map[string]int{ret: 1}, nil }},
		{"hostValue", func([]any) (any, error) { return map[string]string{"k": ret}, nil }},
		{"hostSlice", func([]any) (any, error) { return []string{ret}, nil }},
		{"hostStruct", func([]any) (any, error) { return struct{ S string }{ret}, nil }},
		{"hostError", func([]any) (any, error) { return nil, errors.New(ret) }},
		{"hostTake", func(a []any) (any, error) { args = a; return nil, nil }},
		{"cat", func(a []any) (any, error) { return a[0].(string) + a[1].(string), nil }},
	} {
		if err := m.RegisterHostFunc(f.name, f.f); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.RegisterFunc("goResult", func() string { return ret }, false); err != nil {
		t.Fatal(err)
	}

	if err := m.RegisterFunc("take", func(s string) { arg = s }, false); err != nil {
		t.Fatal(err)
	}

	var module string
	m.SetModuleLoader(func(_ *VM, name string) (string, error) {
		module = name
		return "", nil
	}, nil)
	if _, err := m.Eval("globalThis.cus = s => Array.from({length: s.length}, (_, i) => s.charCodeAt(i).toString(16)).join(' ')", EvalGlobal); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct{ s, want string }{
		{"a\xed\xa0\xbdb", "61 d83d 62"},
		{"\xed\xb8\x80", "de00"},
		{"\xed\xa0\xbd\xed\xb8\x80", "d83d de00"}, // U+1F600 as two surrogates
		{"\xed\xb8\x80\xed\xa0\xbd", "de00 d83d"},
		{"\U0001F600", "d83d de00"},
		{"\xed\x9f\xbf", "d7ff"}, // U+D7FF, valid UTF-8
		{"\x80a", "fffd 61"},
		{"\xed\xa0", "fffd fffd"}, // cut short
		{"\xed\xa0\x80\x80", "d800 fffd"},
		{"\xed\xc0\x80", "fffd fffd fffd"},
		{"\xff\xed\xbf\xbf", "fffd dfff"},
	} {
		check := func(how string, v any, err error, want string) {
			t.Helper()
			if err != nil || v != want {
				t.Errorf("%q %s: got %v, %v, want %s", test.s, how, v, err, want)
			}
		}

		v, err := m.Call("cus", test.s)
		check("as an argument", v, err, test.want)

		// Call converts an argument of any other type with encoding/json.
		v, err = m.Call("o => cus(Object.keys(o)[0])", map[string]string{test.s: ""})
		check("as a map key", v, err, codeUnits(test.s))

		v, err = m.Call("o => cus(o.S)", struct{ S string }{test.s})
		check("in a struct", v, err, codeUnits(test.s))

		js, err := m.NewString(test.s)
		if err != nil {
			t.Fatal(err)
		}

		v, err = m.Call("cus", js)
		js.Free()
		check("from NewString", v, err, test.want)

		atom, err := m.NewAtom(test.s)
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

		v, err = m.Call("o => cus(Object.keys(o)[0])", o)
		o.Free()
		check("from NewAtom", v, err, test.want)

		ret = test.s
		for _, js := range []string{
			"cus(goResult())",
			"cus(hostResult())",
			"cus(Object.keys(hostKey())[0])",
			"cus(hostValue().k)",
			"cus(hostSlice()[0])",
			"try { hostError() } catch (e) { cus(e.message) }",
		} {
			v, err := m.Eval(js, EvalGlobal)
			check(js, v, err, test.want)
		}

		// A struct goes through encoding/json.
		v, err = m.Eval("cus(hostStruct().S)", EvalGlobal)
		check("in a struct result", v, err, codeUnits(test.s))
	}

	for _, test := range []struct{ js, want string }{
		{`a\ud83db`, "a\xed\xa0\xbdb"},
		{`\ude00`, "\xed\xb8\x80"},
		{`x\udfff`, "x\xed\xbf\xbf"},
		{`\ud83d\ud83d`, "\xed\xa0\xbd\xed\xa0\xbd"},
		{`\ude00\ud83d`, "\xed\xb8\x80\xed\xa0\xbd"},
		{`\ud83d\ude00`, "\U0001F600"}, // a pair, one character
		{`😀`, "\U0001F600"},
		{`􏿿\udc00`, "\U0010FFFF\xed\xb0\x80"},
	} {
		check := func(how string, g any, err error, want string) {
			t.Helper()
			if s, ok := g.(string); err != nil || !ok || s != want {
				t.Errorf("'%s' %s: got %q, %v, want %q", test.js, how, g, err, want)
			}
		}

		v, err := m.Eval("'"+test.js+"'", EvalGlobal)
		check("as a result", v, err, test.want)

		_, err = m.Eval("take('"+test.js+"')", EvalGlobal)
		check("as an argument", arg, err, test.want)

		args = nil
		_, err = m.Eval("hostTake('"+test.js+"')", EvalGlobal)
		var a any
		if len(args) == 1 {
			a = args[0]
		}
		check("as a host argument", a, err, test.want)

		_, err = m.Eval("throw new Error('"+test.js+"')", EvalGlobal)
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("got %v, want an *Error", err)
		}

		check("as an error message", e.Message, nil, test.want)

		v, err = m.Eval("({s: '"+test.js+"'})", EvalGlobal)
		var o struct{ S string }
		if err == nil {
			err = v.(*Object).Into(&o)
		}
		check("in an object", o.S, err, wellFormed([]byte(test.want))) // encoding/json

		_, err = m.Eval(`import "`+test.js+`";`, EvalModule)
		check("as a module name", module, err, test.want)
	}

	// Two Go strings, each with one half of a pair, concatenated in Go.
	v, err := m.Eval(`var r = cat("a\ud83d", "\ude00b"); [cus(r), r.codePointAt(1).toString(16)].join(" | ")`, EvalGlobal)
	if want := "61 d83d de00 62 | 1f600"; err != nil || v != want {
		t.Errorf("concatenated in Go: got %v, %v, want %s", v, err, want)
	}

	// Text kept in Go, built one code unit at a time.
	var stored string
	if err := m.RegisterHostFunc("put", func(a []any) (any, error) { stored = a[0].(string); return nil, nil }); err != nil {
		t.Fatal(err)
	}

	if err := m.RegisterHostFunc("get", func([]any) (any, error) { return stored, nil }); err != nil {
		t.Fatal(err)
	}

	v, err = m.Eval(`
		var s = "a\ud83d\ude00b";
		put("");
		for (var i = 0; i < s.length; i++) { put(get() + s[i]); }
		cus(get());
	`, EvalGlobal)
	if want := "61 d83d de00 62"; err != nil || v != want || stored != "a\U0001F600b" {
		t.Errorf("built one code unit at a time: got %v, %v, %q, want %s", v, err, stored, want)
	}

	m.SetWTF8(false)
	ret = "\xed\xa0\x80"
	if v, err := m.Eval("cus(hostResult())", EvalGlobal); err != nil || v != "fffd fffd fffd" {
		t.Errorf("after SetWTF8(false): got %v, %v, want fffd fffd fffd", v, err)
	}

	if v, err := m.Eval(`'\ud800'`, EvalGlobal); err != nil || v != "�" {
		t.Errorf("after SetWTF8(false): got %q, %v, want %q", v, err, "�")
	}
}
