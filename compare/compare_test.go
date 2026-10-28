// Copyright 2025 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package compare // import "modernc.org/quickjs/compare"

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"slices"
	"testing"

	"github.com/dop251/goja"
	"github.com/fastschema/qjs"
	"modernc.org/quickjs"
)

var (
	//go:embed testdata/*.js
	embedFS embed.FS
	files   []fs.DirEntry
)

func TestMain(m *testing.M) {
	var err error
	if files, err = embedFS.ReadDir("testdata"); err != nil {
		os.Exit(1)
	}

	// --- FAIL: BenchmarkGOJA/date_parse.js
	//             err=TypeError: Object has no member 'toGMTString' at date_parse (<eval>:40:38(52)) out=<nil>
	// --- FAIL: BenchmarkQJS/global_destruct_strict.js
	//             err=ReferenceError: global_v1 is not defined
	//             at global_destruct_strict (global_destruct_strict.js:35:9)
	//             at <eval> (global_destruct_strict.js:41:1)
	// --- FAIL: BenchmarkQJS/global_write_strict.js
	//             err=ReferenceError: global_var0 is not defined
	//             at global_write_strict (global_write_strict.js:32:9)
	//             at <eval> (global_write_strict.js:40:1)
	// --- FAIL: BenchmarkQJS/map_delete.js
	//             err=ReferenceError: len is not defined
	//             at map_delete (map_delete.js:32:5)
	//             at <eval> (map_delete.js:45:1)
	//          out=<nil>
	// --- FAIL: BenchmarkQJS/weak_map_delete.js
	//             err=ReferenceError: len is not defined
	//             at weak_map_delete (weak_map_delete.js:32:5)
	//             at <eval> (weak_map_delete.js:49:1)
	//          out=<nil>
	// --- FAIL: BenchmarkQJS/weak_map_set.js
	//             err=ReferenceError: len is not defined
	//             at weak_map_set (weak_map_set.js:32:5)
	//             at <eval> (weak_map_set.js:46:1)
	//          out=<nil>
	files = slices.DeleteFunc(files, func(e fs.DirEntry) bool {
		switch e.Name() {
		case
			"date_parse.js",
			"global_destruct_strict.js",
			"global_write_strict.js",
			"map_delete.js",
			"weak_map_delete.js",
			"weak_map_set.js":

			return true
		default:
			return false
		}
	})

	os.Exit(m.Run())
}

func getTest(b *testing.B, fn string, bn int) (r string) {
	buf, err := embedFS.ReadFile(path.Join("testdata", fn))
	if err != nil {
		b.Fatal(err)
	}

	r = string(bytes.Replace(buf, []byte("$N"), []byte(fmt.Sprint(bn)), 1))
	debug.FreeOSMemory()
	return r
}

func BenchmarkCCGO(b *testing.B) {
	for _, v := range files {
		fn := v.Name()
		b.Run(fn, func(b *testing.B) {
			js := getTest(b, fn, b.N)
			vm, err := quickjs.NewVM()
			if err != nil {
				b.Fatal(fn, err)
			}

			defer vm.Close()

			b.ReportAllocs()
			b.ResetTimer()
			if out, err := vm.Eval(js, quickjs.EvalGlobal); err != nil {
				b.Fatalf("err=%v out=%v", err, out)
			}
		})
	}
}

func BenchmarkQJS(b *testing.B) {
	for _, v := range files {
		fn := v.Name()
		b.Run(fn, func(b *testing.B) {
			js := getTest(b, fn, b.N)
			vm, err := qjs.New()
			if err != nil {
				b.Fatal(fn, err)
			}

			defer vm.Close()

			b.ReportAllocs()
			b.ResetTimer()
			if out, err := vm.Eval(fn, qjs.Code(js)); err != nil {
				b.Fatalf("err=%v out=%v", err, out)
			}
		})
	}
}

func BenchmarkGOJA(b *testing.B) {
	for _, v := range files {
		fn := v.Name()
		b.Run(fn, func(b *testing.B) {
			js := getTest(b, fn, b.N)
			vm := goja.New()
			b.ReportAllocs()
			b.ResetTimer()
			if out, err := vm.RunString(js); err != nil {
				b.Fatalf("err=%v out=%v", err, out)
			}
		})
	}
}

// v8Dir is the V8 benchmark suite, version 7. It stays in the root module,
// where memgrind_test.go runs it as a leak test.
var v8Dir = filepath.Join("..", "internal", "arewefastyet", "v8-v7")

// v8Benchmarks lists the benchmarks of the suite with the file defining each.
// Crypto defines two, Encrypt and Decrypt, and so does EarleyBoyer.
var v8Benchmarks = []struct{ name, file string }{
	{"Richards", "richards.js"},
	{"DeltaBlue", "deltablue.js"},
	{"Encrypt", "crypto.js"},
	{"Decrypt", "crypto.js"},
	{"RayTrace", "raytrace.js"},
	{"Earley", "earley-boyer.js"},
	{"Boyer", "earley-boyer.js"},
	{"RegExp", "regexp.js"},
	{"Splay", "splay.js"},
	{"NavierStokes", "navier-stokes.js"},
}

// qjsStrict lists the files of the suite fastschema/qjs cannot load. It
// evaluates everything in strict mode, and the field holding that flag is
// unexported, so its callers cannot turn it off.
var qjsStrict = map[string]string{
	"crypto.js":       "assigns to an undeclared variable",
	"earley-boyer.js": "has an octal escape sequence",
}

// v8Glue finds the benchmark named by its argument, runs its setup and
// defines __run, which runs the benchmark n times. The harness of the suite
// runs the benchmarks of a suite in order and Decrypt decrypts what Encrypt
// left behind, so the ones before it run once first.
const v8Glue = `
var __bench = (function(name) {
	var suites = BenchmarkSuite.suites;
	for (var i = 0; i < suites.length; i++) {
		var a = suites[i].benchmarks;
		for (var j = 0; j < a.length; j++) {
			if (a[j].name !== name) {
				continue;
			}

			for (var k = 0; k < j; k++) {
				a[k].Setup();
				a[k].run();
				a[k].TearDown();
			}
			return a[j];
		}
	}
	throw new Error("no benchmark " + name);
})(%q);

function __run(n) {
	for (var i = 0; i < n; i++) {
		__bench.run();
	}
}

void __bench.Setup();
`

// v8Engine evaluates scripts in a VM it keeps until close.
type v8Engine struct {
	eval  func(src string) error
	close func()
}

// BenchmarkV8 times one run of each benchmark of the V8 suite. The harness of
// the suite turns the same time into a score, reference time over measured
// time, so the ratios of time/op between engines are the inverse ratios of
// their V8 scores.
func BenchmarkV8(b *testing.B) {
	b.Run("CCGO", func(b *testing.B) {
		benchmarkV8(b, nil, func() (*v8Engine, error) {
			vm, err := quickjs.NewVM()
			if err != nil {
				return nil, err
			}

			return &v8Engine{
				eval: func(src string) error {
					_, err := vm.Eval(src, quickjs.EvalGlobal)
					return err
				},
				close: func() { vm.Close() },
			}, nil
		})
	})
	b.Run("GOJA", func(b *testing.B) {
		benchmarkV8(b, nil, func() (*v8Engine, error) {
			vm := goja.New()
			return &v8Engine{
				eval: func(src string) error {
					_, err := vm.RunString(src)
					return err
				},
				close: func() {},
			}, nil
		})
	})
	b.Run("QJS", func(b *testing.B) {
		benchmarkV8(b, qjsStrict, func() (*v8Engine, error) {
			vm, err := qjs.New()
			if err != nil {
				return nil, err
			}

			return &v8Engine{
				eval: func(src string) error {
					_, err := vm.Eval("v8.js", qjs.Code(src))
					return err
				},
				close: func() { vm.Close() },
			}, nil
		})
	})
}

// benchmarkV8 runs the suite on the engines newEngine returns, skipping the
// files in skip.
func benchmarkV8(b *testing.B, skip map[string]string, newEngine func() (*v8Engine, error)) {
	base, err := os.ReadFile(filepath.Join(v8Dir, "base.js"))
	if err != nil {
		b.Fatal(err)
	}

	for _, v := range v8Benchmarks {
		// Like the harness of the suite, set up once and measure after a
		// warm-up: the engine lives across the rounds of b.N, the first of
		// which, b.N == 1, warms it up. Unless that one already takes longer
		// than -benchtime, then it is the only round.
		var e *v8Engine
		b.Run(v.name, func(b *testing.B) {
			if why, ok := skip[v.file]; ok {
				b.Skipf("%s %s", v.file, why)
			}

			if e == nil {
				s, err := os.ReadFile(filepath.Join(v8Dir, v.file))
				if err != nil {
					b.Fatal(err)
				}

				debug.FreeOSMemory()
				if e, err = newEngine(); err != nil {
					b.Fatal(err)
				}

				if err := e.eval(string(base) + "\n" + string(s) + fmt.Sprintf(v8Glue, v.name)); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			b.ResetTimer()
			if err := e.eval(fmt.Sprintf("__run(%d)", b.N)); err != nil {
				b.Fatal(err)
			}
		})
		if e != nil {
			e.eval("__bench.TearDown()")
			e.close()
		}
	}
}
