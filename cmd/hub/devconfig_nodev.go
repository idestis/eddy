//go:build !dev

package main

import (
	"fmt"
	"io"
)

func devConfig(_ []string, _ func(string) string, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "eddy-hub: dev-config requires a dev build (go run -tags dev ./cmd/hub dev-config)")
	return 2
}
