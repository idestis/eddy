//go:build !dev

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLocalNeedsDevBuild(t *testing.T) {
	for _, args := range [][]string{{"--local"}, {"-local"}, {"--local=true", "--contexts", "x"}} {
		var stderr bytes.Buffer
		code := runMain(args, func(string) string { return "1" }, &bytes.Buffer{}, &stderr)
		if code != 2 || !strings.Contains(stderr.String(), "local mode requires a dev build") {
			t.Fatalf("%v: code %d: %s", args, code, stderr.String())
		}
	}
	var envErr bytes.Buffer
	env := func(k string) string {
		if k == "EDDY_AGENT_LOCAL" {
			return "1"
		}
		return ""
	}
	if code := runMain(nil, env, &bytes.Buffer{}, &envErr); code != 2 || !strings.Contains(envErr.String(), "requires a dev build") {
		t.Fatalf("EDDY_AGENT_LOCAL=1: code %d: %s", code, envErr.String())
	}
	// The local-mode flags do not exist at all.
	var stderr bytes.Buffer
	if code := runMain([]string{"--contexts", "x"}, func(string) string { return "" }, &bytes.Buffer{}, &stderr); code != 2 {
		t.Fatalf("--contexts accepted in a release build: %d %s", code, stderr.String())
	}
}
