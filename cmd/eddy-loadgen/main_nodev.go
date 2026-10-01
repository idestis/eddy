//go:build !dev

// Command eddy-loadgen is a synthetic agent fleet for load tests
// (ADR-0006). It exists only in dev builds: go run -tags dev ./cmd/eddy-loadgen.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "eddy-loadgen is a dev tool: build it with -tags dev (go run -tags dev ./cmd/eddy-loadgen -h)")
	os.Exit(2)
}
