//go:build !dev

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDevConfigNeedsDevBuild(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"dev-config"}, nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "requires a dev build") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
}
