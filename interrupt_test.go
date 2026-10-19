// Copyright 2026 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quickjs // import "modernc.org/quickjs"

import (
	"math"
	"sync"
	"testing"
	"time"
)

// TestInterruptAfterClose calls Interrupt on a closed VM, as a watchdog does
// when the script it guards has just finished. Interrupt used to write
// through the address of the interrupt flag, which Close frees and clears: a
// nil pointer dereference.
func TestInterruptAfterClose(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	m.Close()
	m.Interrupt()
}

// TestInterruptDuringClose calls Interrupt from other goroutines while the VM
// closes. Run with -race, the race detector checks that the two are ordered.
func TestInterruptDuringClose(t *testing.T) {
	for range 20 {
		m, err := NewVM()
		if err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 4 {
			wg.Go(func() {
				<-start
				for range 100 {
					m.Interrupt()
				}
			})
		}
		close(start)
		m.Close()
		wg.Wait()
	}
}

// nestedEval returns a VM with a Go function 'nested' that evaluates more
// Javascript on the same VM, as a host function calling back into a page
// does.
func nestedEval(t *testing.T) *VM {
	m := newTestVM(t)
	if err := m.RegisterFunc("nested", func() {
		if _, err := m.Eval("1", EvalGlobal); err != nil && err.Error() != "InternalError: interrupted" {
			t.Error(err)
		}
	}, false); err != nil {
		t.Fatal(err)
	}

	return m
}

// stopsWithin evaluates 'js' on m and fails the test unless an interrupt ends
// it within 'd'. On a failure it interrupts m until the script ends.
func stopsWithin(t *testing.T, m *VM, js string, d time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := m.Eval(js, EvalGlobal)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || err.Error() != "InternalError: interrupted" {
			t.Errorf("got %v, want an interrupt", err)
		}
	case <-time.After(d):
		t.Errorf("the script ran for %v", d)
		for {
			m.Interrupt()
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}
}

// TestNestedEvalKeepsTimeout checks that Javascript evaluated by a Go function
// that Javascript called does not get a new eval timeout. Every Eval armed it
// again, so a script calling such a function in a loop ran forever.
func TestNestedEvalKeepsTimeout(t *testing.T) {
	m := nestedEval(t)
	m.SetEvalTimeout(100 * time.Millisecond)
	stopsWithin(t, m, "for (;;) nested()", 10*time.Second)
}

// TestNestedEvalKeepsInterrupt checks that Javascript evaluated by a Go
// function that Javascript called does not clear an Interrupt. Every Eval
// cleared it, so it took luck to stop a script calling such a function in a
// loop.
func TestNestedEvalKeepsInterrupt(t *testing.T) {
	m := nestedEval(t)
	go func() {
		time.Sleep(50 * time.Millisecond)
		m.Interrupt()
	}()
	stopsWithin(t, m, "for (;;) nested()", 10*time.Second)
}

// TestHugeEvalTimeout sets timeouts too long for the clock. The deadline
// wrapped around to the past and the timeout fired at once.
func TestHugeEvalTimeout(t *testing.T) {
	m := newTestVM(t)
	for _, d := range []time.Duration{math.MaxInt64, math.MaxInt64 - 1, math.MaxInt64 / 2} {
		m.SetEvalTimeout(d)
		if _, err := m.Eval("{ let s = 0; for (let i = 0; i < 1e6; i++) s += i; s }", EvalGlobal); err != nil {
			t.Errorf("timeout %v: %v", d, err)
		}
	}
}

// TestPendingJobsStopAtInterrupt checks that an interrupt ends a drain of the
// job queue. The jobs after the interrupted one ran, and only a job that ran
// long enough for the engine to check for the interrupt was stopped.
func TestPendingJobsStopAtInterrupt(t *testing.T) {
	m := newTestVM(t)
	if _, err := m.Eval("Promise.resolve().then(() => { for (;;); }); Promise.resolve().then(() => { globalThis.later = true })", EvalGlobal); err != nil {
		t.Fatal(err)
	}

	m.SetEvalTimeout(50 * time.Millisecond)
	n, err := m.ExecutePendingJobs()
	if err == nil || err.Error() != "InternalError: interrupted" || n != 1 {
		t.Fatalf("got %d jobs and %v, want 1 job and an interrupt", n, err)
	}

	if v, err := m.Eval("typeof later", EvalGlobal); err != nil || v != "undefined" {
		t.Fatalf("a job after the interrupt ran: %v, %v", v, err)
	}

	if n, err := m.ExecutePendingJobs(); err != nil || n != 1 {
		t.Fatalf("draining the rest: got %d jobs and %v, want 1 job", n, err)
	}

	if v, err := m.Eval("typeof later", EvalGlobal); err != nil || v != "boolean" {
		t.Fatalf("the job left queued did not run: %v, %v", v, err)
	}
}

// TestGoGetterAfterIdle reads a property whose getter calls a Go function,
// after the VM was idle for longer than its eval timeout. The read must arm
// the timeout again, or the Go function would return to Javascript past the
// deadline of the last Eval, and be taken for interrupted.
func TestGoGetterAfterIdle(t *testing.T) {
	m := newTestVM(t)
	if err := m.RegisterFunc("goValue", func() int { return 42 }, false); err != nil {
		t.Fatal(err)
	}

	o, err := m.EvalValue("({get x() { return goValue() }})", EvalGlobal)
	if err != nil {
		t.Fatal(err)
	}

	defer o.Free()

	x, err := m.NewAtom("x")
	if err != nil {
		t.Fatal(err)
	}

	m.SetEvalTimeout(20 * time.Millisecond)
	if _, err := m.Eval("0", EvalGlobal); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)
	if v, err := o.Property(x); err != nil || v != 42 {
		t.Fatalf("got %v, %v, want 42", v, err)
	}
}
