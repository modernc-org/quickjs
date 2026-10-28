// Copyright 2026 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quickjs // import "modernc.org/quickjs"

import (
	"os"
	"os/exec"
	"testing"
)

// TestPrintPipe checks that what print writes reaches a stdout that is a pipe,
// also when the program ends without closing the VM. Musl buffered it, and
// only the first write went out.
func TestPrintPipe(t *testing.T) {
	const env = "QUICKJS_TEST_PRINT_PIPE"
	if os.Getenv(env) != "" {
		vm, err := NewVM()
		if err != nil {
			os.Exit(2)
		}

		if err := vm.StdAddHelpers(); err != nil {
			os.Exit(3)
		}

		if _, err := vm.Eval(`print("a"); print("b", 42); print("c");`, EvalGlobal); err != nil {
			os.Exit(4)
		}

		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestPrintPipe$")
	cmd.Env = append(os.Environ(), env+"=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v, stdout=%q", err, out)
	}

	if g, e := string(out), "a\nb 42\nc\n"; g != e {
		t.Fatalf("got %q, expected %q", g, e)
	}
}
