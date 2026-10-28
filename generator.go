// Copyright 2025 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build ignore

package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	goruntime "runtime"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
)

const (
	docFn = "quickjs.go"
	modFn = "go.mod"
	qemu  = "    (qemu)"

	// fingerprintPrefix starts the first line of a results file, recording the
	// root go.mod the numbers below it were measured against. The benchmarks
	// re-run when that no longer matches, ie. exactly when a builder
	// regenerates: its autogen gate compares the same go.mod against
	// internal/autogen/<goos>_<goarch>.mod. Nothing needs bumping by hand.
	fingerprintPrefix = "# root go.mod sha256 "

	// The labels of the geomean rows compare/benchreport.go prints, one per
	// table: the V8 benchmark suite and the micro benchmarks.
	geomeanV8    = "v8-geomean"
	geomeanMicro = "geomean"
)

var (
	goos       = runtime.GOOS
	goarch     = runtime.GOARCH
	target     = fmt.Sprintf("%s_%s", goos, goarch) + ".txt"
	resultsDir = path.Join("testdata", "benchmarks")
	notes      = map[string]string{
		"freebsd/amd64": qemu,
		"freebsd/arm64": qemu,
		"linux/386":     qemu,
		"linux/s390x":   qemu,
		"openbsd/amd64": qemu,
		"windows/386":   qemu,
	}
)

func fail(rc int, s string, args ...any) {
	fmt.Fprintln(os.Stderr, fmt.Sprintf(s, args...))
	os.Exit(rc)
}

func main() {
	fp, err := fingerprint()
	if err != nil {
		fail(1, "%v", err)
	}

	resultsFn := path.Join(resultsDir, target)
	ok, err := measured(resultsFn, fp)
	if err != nil {
		fail(1, "%v", err)
	}

	switch {
	case ok:
		fmt.Printf("%s: measured against the current %s, skipping benchmarks\n", resultsFn, modFn)
	default:
		fmt.Printf("%s: missing or measured against a different %s, benchmarking\n", resultsFn, modFn)
		if err := benchmark(resultsFn, fp); err != nil {
			fail(1, "%v", err)
		}
	}

	// Unconditional: keeps the package documentation in sync with
	// testdata/benchmarks even when this platform had nothing to measure.
	if err := document(); err != nil {
		fail(1, "%v", err)
	}
}

// fingerprint returns the first line of a results file produced now.
func fingerprint() (string, error) {
	b, err := os.ReadFile(modFn)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s%x", fingerprintPrefix, sha256.Sum256(b)), nil
}

// measured reports whether fn holds results recorded for fingerprint fp. A
// missing file, one predating fingerprintPrefix or one predating the V8 table
// is not measured.
func measured(fn, fp string) (bool, error) {
	b, err := os.ReadFile(fn)
	switch {
	case err == nil:
		line, _, _ := bytes.Cut(b, []byte("\n"))
		return string(line) == fp && geomeans(b)[geomeanV8] != nil, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

// benchmark runs the ./compare module and records its report in fn, stamped
// with fp.
func benchmark(fn, fp string) error {
	if err := os.MkdirAll(resultsDir, 0775); err != nil {
		return err
	}

	var stdout, stderr bytes.Buffer
	// Through a shell: on Windows the builders run 'go generate' from git-bash
	// and make lives there.
	cmd := exec.Command("sh", "-c", "make benchmark")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("make benchmark: %v\nstdout=%s\nstderr=%s", err, stdout.Bytes(), stderr.Bytes())
	}

	// Only on success: a partial file carrying the current fingerprint would
	// pass for measured and suppress the retry the next cycle owes us.
	return os.WriteFile(fn, append([]byte(fp+"\n"), stdout.Bytes()...), 0660)
}

// geomeans returns the geomean rows of the results in b, keyed by their label,
// the values in the order CCGO, GOJA, QJS.
func geomeans(b []byte) map[string][]string {
	r := map[string][]string{}
	for _, v := range strings.Split(string(b), "\n") {
		if f := strings.Fields(v); len(f) >= 4 && (f[0] == geomeanV8 || f[0] == geomeanMicro) { // geomean 1.000 1.019 0.848
			r[f[0]] = f[1:4]
		}
	}
	return r
}

// requires returns the versions of the modules required by the go.mod at fn,
// keyed by module path.
func requires(fn string) (map[string]string, error) {
	b, err := os.ReadFile(fn)
	if err != nil {
		return nil, err
	}

	mod, err := modfile.ParseLax(fn, b, nil)
	if err != nil {
		return nil, err
	}

	r := map[string]string{}
	for _, v := range mod.Require {
		r[v.Mod.Path] = v.Mod.Version
	}
	return r, nil
}

// document rewrites the '# Performance' section of the docFn package
// documentation from every results file present.
func document() error {
	// The measured engine comes from the root go.mod. The compare module gets
	// this one through a 'replace' pointing at the work tree, so its own
	// modernc.org/quickjs require records nothing about what ran.
	root, err := requires(modFn)
	if err != nil {
		return err
	}

	compare, err := requires(path.Join("compare", "go.mod"))
	if err != nil {
		return err
	}

	m, err := filepath.Glob(path.Join(resultsDir, "*.txt"))
	if err != nil {
		return err
	}

	for i, v := range m {
		m[i] = filepath.ToSlash(v)
	}
	var results []platformResults
	for _, fn := range m {
		b, err := os.ReadFile(fn)
		if err != nil {
			return err
		}

		key := path.Base(fn)                    // os_arch.txt
		key = key[:len(key)-len(".txt")]        // os_arch
		key = strings.Replace(key, "_", "/", 1) // os/arch
		results = append(results, platformResults{key, geomeans(b)})
	}

	slices.SortFunc(results, func(a, b platformResults) int { return strings.Compare(a.key, b.key) })
	doc, err := os.ReadFile(docFn)
	if err != nil {
		return err
	}

	a := strings.Split(string(doc), "\n")
	buf := bytes.NewBuffer(nil)
	state := 0
	ver := func(ip string, versions map[string]string) string {
		return fmt.Sprintf("%s@%s", ip, versions[ip])
	}
	for _, v := range a {
		switch state {
		case 0:
			fmt.Fprintf(buf, "%s\n", v)
			if strings.HasPrefix(v, "// # Performance") {
				state++
			}
		case 1:
			fmt.Fprintf(buf, `//
// Geomeans of time/op relative to CCGO, lower number is better, n/a where not
// measured yet. Detailed results are in the testdata/benchmarks directory.
//
//  CCGO: %s
//  GOJA: %s
//   QJS: %s
//
// The V8 benchmark suite, version 7: Richards, DeltaBlue, Crypto, RayTrace,
// EarleyBoyer, RegExp, Splay and NavierStokes. These are complete programs and
// the better guide to how fast an application runs. QJS evaluates in strict
// mode only, in which Crypto and EarleyBoyer do not load; its number is over
// the other six.
//
`,
				ver("modernc.org/libquickjs", root),
				ver("github.com/dop251/goja", compare),
				ver("github.com/fastschema/qjs", compare),
			)
			table(buf, results, geomeanV8)
			fmt.Fprintf(buf, `//
// The QuickJS micro benchmarks, loops that each repeat a single operation,
// like a property read or a string append. They show which operations an
// engine is fast or slow at, not how fast it runs a program.
//
`)
			table(buf, results, geomeanMicro)
			state++
		case 2:
			switch {
			case strings.HasPrefix(v, "// # Notes"):
				fmt.Fprintf(buf, "%s\n", v)
				state++
			}
		case 3:
			fmt.Fprintf(buf, "%s\n", v)
		}
	}
	if err := os.WriteFile(docFn, buf.Bytes(), 0660); err != nil {
		return err
	}

	// The builders gofmt after generating, a local 'go generate' does not.
	if out, err := exec.Command("gofmt", "-s", "-w", docFn).CombinedOutput(); err != nil {
		return fmt.Errorf("gofmt %s: %v\n%s", docFn, err, out)
	}

	return nil
}

// platformResults are the geomean rows of one results file.
type platformResults struct {
	key      string              // os/arch
	geomeans map[string][]string // by label: CCGO, GOJA, QJS
}

// table writes the rows labeled label of results to w, n/a for a platform
// without one.
func table(w io.Writer, results []platformResults, label string) {
	fmt.Fprintf(w, `//	                        CCGO     GOJA     QJS
//	-----------------------------------------------
`)
	for _, v := range results {
		g := v.geomeans[label]
		if g == nil {
			g = []string{"n/a", "n/a", "n/a"}
		}
		fmt.Fprintf(w, "//	%20s%9s%9s%9s%s\n", v.key, g[0], g[1], g[2], notes[v.key])
	}
	fmt.Fprintf(w, `//	-----------------------------------------------
//	                        CCGO     GOJA     QJS
`)
}

// origin returns caller's short position, skipping skip frames.
func origin(skip int) string {
	pc, fn, fl, _ := goruntime.Caller(skip)
	f := goruntime.FuncForPC(pc)
	var fns string
	if f != nil {
		fns = f.Name()
		if x := strings.LastIndex(fns, "."); x > 0 {
			fns = fns[x+1:]
		}
		if strings.HasPrefix(fns, "func") {
			num := true
			for _, c := range fns[len("func"):] {
				if c < '0' || c > '9' {
					num = false
					break
				}
			}
			if num {
				return origin(skip + 2)
			}
		}
	}
	return fmt.Sprintf("%s:%d:%s", filepath.Base(fn), fl, fns)
}

// todo prints and return caller's position and an optional message tagged with TODO. Output goes to stderr.
func todo(s string, args ...interface{}) string {
	switch {
	case s == "":
		s = fmt.Sprintf(strings.Repeat("%v ", len(args)), args...)
	default:
		s = fmt.Sprintf(s, args...)
	}
	r := fmt.Sprintf("%s\n\tTODO %s", origin(2), s)
	// fmt.Fprintf(os.Stderr, "%s\n", r)
	// os.Stdout.Sync()
	return r
}

// trc prints and return caller's position and an optional message tagged with TRC. Output goes to stderr.
//
//lint:ignore U1000 debug helper
func trc(s string, args ...interface{}) string {
	switch {
	case s == "":
		s = fmt.Sprintf(strings.Repeat("%v ", len(args)), args...)
	default:
		s = fmt.Sprintf(s, args...)
	}
	r := fmt.Sprintf("%s: TRC %s", origin(2), s)
	fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format("15:04:05.000"), r)
	os.Stderr.Sync()
	return r
}
