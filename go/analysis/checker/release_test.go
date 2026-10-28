// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package checker_test

import (
	"errors"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/internal/testenv"
	"golang.org/x/tools/txtar"
)

type bigResult struct{ data []byte }

// TestReleaseResults checks that an intermediate result is released once
// its last dependent has run, before Analyze returns, and that root
// results are kept.
func TestReleaseResults(t *testing.T) {
	testenv.NeedsGoPackages(t)

	const src = `
-- go.mod --
module example.com/hello
go 1.13

-- hello.go --
package hello
`
	dir := t.TempDir()
	fs, err := txtar.FS(txtar.Parse([]byte(src)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dir, fs); err != nil {
		t.Fatal(err)
	}
	pkgs, err := packages.Load(&packages.Config{Mode: packages.LoadAllSyntax, Dir: dir}, "example.com/hello")
	if err != nil {
		t.Fatal(err)
	}

	released := make(chan struct{})
	producer := &analysis.Analyzer{
		Name:       "producer",
		Doc:        "Returns a result whose release is observed.",
		ResultType: reflect.TypeFor[*bigResult](),
		Run: func(pass *analysis.Pass) (any, error) {
			result := &bigResult{data: make([]byte, 1<<20)}
			runtime.AddCleanup(result, func(struct{}) { close(released) }, struct{}{})
			return result, nil
		},
	}
	consumer := &analysis.Analyzer{
		Name:       "consumer",
		Doc:        "Requires the producer's result.",
		Requires:   []*analysis.Analyzer{producer},
		ResultType: reflect.TypeFor[string](),
		Run: func(pass *analysis.Pass) (any, error) {
			if pass.ResultOf[producer] == nil {
				return nil, errors.New("producer's result is missing")
			}
			return "kept", nil
		},
	}
	// waiter runs after consumer, the producer's only dependent, so the
	// producer's result must be collectable by then.
	waiter := &analysis.Analyzer{
		Name:     "waiter",
		Doc:      "Waits for the producer's result to be released.",
		Requires: []*analysis.Analyzer{consumer},
		Run: func(pass *analysis.Pass) (any, error) {
			for range 50 {
				runtime.GC()
				select {
				case <-released:
					return nil, nil
				case <-time.After(20 * time.Millisecond):
				}
			}
			return nil, errors.New("producer's result was not released")
		},
	}

	graph, err := checker.Analyze([]*analysis.Analyzer{consumer, waiter}, pkgs, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range graph.Roots {
		if root.Err != nil {
			t.Errorf("%s: %v", root, root.Err)
		}
		if root.Analyzer == consumer && root.Result != "kept" {
			t.Errorf("consumer's root result = %v, want %q", root.Result, "kept")
		}
	}
}
