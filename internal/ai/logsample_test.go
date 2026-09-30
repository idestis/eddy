package ai

import (
	"fmt"
	"testing"
)

func TestSampleLogs(t *testing.T) {
	var lines []string
	for i := range 900 {
		lines = append(lines, fmt.Sprintf("2026-09-30T10:%02d:00Z info request %d ok", i%60, i))
	}
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("2026-09-30T11:00:%02dZ ERROR db connection refused after %dms id=deadbeef%02d", i, 100+i, i))
	}
	lines = append(lines, `{"level":"warn","msg":"cache miss storm"}`, "panic: runtime error: index out of range")

	s := sampleLogs(lines)
	if s.Scanned != len(lines) {
		t.Fatalf("scanned %d, want %d", s.Scanned, len(lines))
	}
	if s.Errors != 41 || s.Warnings != 1 {
		t.Fatalf("errors %d warnings %d, want 41 and 1", s.Errors, s.Warnings)
	}
	// The 40 connection errors differ only in numbers and ids, so they collapse into one line.
	var db signalLine
	for _, l := range s.Signal {
		if l.Count == 40 {
			db = l
		}
	}
	if db.Level != "error" || db.Line == "" {
		t.Fatalf("expected one deduplicated db error with count 40, got %+v", s.Signal)
	}
	if len(s.Signal) != 3 {
		t.Fatalf("signal lines %d, want 3: %+v", len(s.Signal), s.Signal)
	}
	if len(s.Recent) != logRecentLines || s.Recent[len(s.Recent)-1] != lines[len(lines)-1] {
		t.Fatalf("recent tail wrong: %d lines", len(s.Recent))
	}
}

func TestSampleLogsCapsAndTruncates(t *testing.T) {
	var lines []string
	for i := range 200 {
		lines = append(lines, fmt.Sprintf("error kind-%c%c happened", 'a'+i%26, 'a'+i/26))
	}
	long := "error " + string(make([]byte, 2000))
	lines = append(lines, long)
	s := sampleLogs(lines)
	if len(s.Signal) != logSignalLines {
		t.Fatalf("signal lines %d, want cap %d", len(s.Signal), logSignalLines)
	}
	if got := s.Recent[len(s.Recent)-1]; len(got) > 510 {
		t.Fatalf("long line not truncated: %d bytes", len(got))
	}
}
