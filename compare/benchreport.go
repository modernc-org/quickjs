// Copyright 2025 The quickjs-go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build ignore

// Benchreport copies the output of 'go test -bench' in this module to stdout
// and appends a table of time/op relative to CCGO for each benchmark suite. It
// fails if a benchmark did: generator.go must not record a partial report.
package main

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
)

// suites lists the tables in the order they are printed. Prefix is the first
// element of a benchmark name with three of them, BenchmarkV8/CCGO/Richards,
// and empty for the micro benchmarks, BenchmarkCCGO/array_for.js. The label of
// the geomean row is what generator.go looks for.
var suites = []struct{ prefix, title, geomean string }{
	{"V8", "V8 benchmark suite v7", "v8-geomean"},
	{"", "QuickJS micro benchmarks", "geomean"},
}

func main() {
	b := bytes.NewBuffer(nil)
	io.Copy(io.MultiWriter(os.Stdout, b), os.Stdin)
	m := map[string]map[string]map[string]float64{} // suite prefix: engine: arg: ns/op
	failed := false
	for _, v := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(v, "FAIL") || strings.HasPrefix(v, "--- FAIL") {
			failed = true
		}

		if !strings.HasPrefix(v, "Benchmark") {
			continue
		}

		f := strings.Fields(v[len("Benchmark"):])
		if len(f) < 3 {
			continue
		}

		var prefix, engine, arg string
		switch k := strings.Split(f[0], "/"); len(k) {
		case 2:
			engine, arg = k[0], k[1]
		case 3:
			prefix, engine, arg = k[0], k[1], k[2]
		default:
			continue
		}

		t, err := strconv.ParseFloat(f[2], 64)
		if err != nil {
			continue
		}

		if m[prefix] == nil {
			m[prefix] = map[string]map[string]float64{}
		}
		if m[prefix][engine] == nil {
			m[prefix][engine] = map[string]float64{}
		}
		m[prefix][engine][arg] = t
	}
	for _, v := range suites {
		if m[v.prefix] != nil {
			report(v.title, v.geomean, m[v.prefix])
		}
	}
	if failed {
		fmt.Fprintln(os.Stderr, "benchreport: a benchmark failed")
		os.Exit(1)
	}
}

// report prints the table of m, the times of each engine keyed by benchmark,
// relative to the first engine. A benchmark an engine did not run is n/a and
// left out of its geomean.
func report(title, geomean string, m map[string]map[string]float64) {
	var engines, args []string
	for engine, v := range m {
		engines = append(engines, engine)
		for arg := range v {
			args = append(args, arg)
		}
	}
	slices.Sort(engines)
	slices.Sort(args)
	args = slices.Compact(args)
	fmt.Printf("\n%s\n%35s", title, "arg")
	for _, v := range engines {
		fmt.Printf("%12s", v)
	}
	hr := strings.Repeat("-", 35+12*(len(engines)))
	fmt.Printf("\n%s\n", hr)
	nums := map[string][]float64{}
	for _, arg := range args {
		fmt.Printf("%35s", arg)
		t1 := m[engines[0]][arg]
		for _, engine := range engines {
			t, ok := m[engine][arg]
			if !ok || t1 == 0 {
				fmt.Printf("%12s", "n/a")
				continue
			}

			k := t / t1
			nums[engine] = append(nums[engine], k)
			fmt.Printf("%12.3f", k)
		}
		fmt.Println()
	}
	fmt.Printf("%s\n%35s", hr, geomean)
	for _, engine := range engines {
		switch s := nums[engine]; {
		case len(s) == 0:
			fmt.Printf("%12s", "n/a")
		default:
			fmt.Printf("%12.3f", geoMean(s))
		}
	}
	fmt.Println()
	fmt.Printf("%35s", "")
	for _, v := range engines {
		fmt.Printf("%12s", v)
	}
	fmt.Println()
}

func geoMean(s []float64) float64 {
	if len(s) == 0 {
		return 0.0
	}

	logSum := 0.0
	for _, num := range s {
		if num == 0 {
			return 0.0
		}

		logSum += math.Log(num)
	}

	return math.Exp(logSum / float64(len(s)))
}
