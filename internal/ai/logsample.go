package ai

import (
	"regexp"
	"strings"
)

// Log sampling for get_logs: instead of the last N lines, the model gets the
// lines that usually matter (errors and warnings, deduplicated) plus the most
// recent context, in roughly the same token budget.
const (
	logScanLines   = 1000 // lines fetched per pod and container
	logSignalLines = 60   // distinct error/warning lines kept
	logRecentLines = 60   // most recent lines kept
)

var (
	errorPattern = regexp.MustCompile(`(?i)\b(error|err|fatal|panic|exception|traceback|failed|failure|crash(ed)?|oomkilled|segfault)\b|level=(error|fatal)|"level":"(error|fatal)"`)
	warnPattern  = regexp.MustCompile(`(?i)\b(warn(ing)?|deprecated|timeout|timed out|retry(ing)?|refused|unavailable)\b|level=warn|"level":"warn`)
	// Numbers, hex ids and timestamps vary between otherwise identical lines.
	noisePattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8,}\b|\d+`)
)

type logSample struct {
	Scanned  int          `json:"scanned"`
	Errors   int          `json:"errors"`
	Warnings int          `json:"warnings"`
	Signal   []signalLine `json:"signal,omitempty"`
	Recent   []string     `json:"recent"`
	First    string       `json:"first,omitempty"`
	Last     string       `json:"last,omitempty"`
}

type signalLine struct {
	Level string `json:"level"`
	Count int    `json:"count"`
	Line  string `json:"line"`
}

// sampleLogs builds a logSample from lines, oldest first.
func sampleLogs(lines []string) logSample {
	s := logSample{Scanned: len(lines)}
	if len(lines) > 0 {
		s.First, s.Last = truncateLine(lines[0]), truncateLine(lines[len(lines)-1])
	}
	index := map[string]int{}
	for _, l := range lines {
		level := ""
		switch {
		case errorPattern.MatchString(l):
			level = "error"
			s.Errors++
		case warnPattern.MatchString(l):
			level = "warning"
			s.Warnings++
		default:
			continue
		}
		key := level + "|" + noisePattern.ReplaceAllString(l, "#")
		if i, ok := index[key]; ok {
			s.Signal[i].Count++
			s.Signal[i].Line = truncateLine(l) // keep the latest occurrence
			continue
		}
		if len(s.Signal) < logSignalLines {
			index[key] = len(s.Signal)
			s.Signal = append(s.Signal, signalLine{Level: level, Count: 1, Line: truncateLine(l)})
		}
	}
	recent := lines[max(0, len(lines)-logRecentLines):]
	s.Recent = make([]string, len(recent))
	for i, l := range recent {
		s.Recent[i] = truncateLine(l)
	}
	return s
}

func truncateLine(l string) string {
	const maxLine = 500
	l = strings.TrimRight(l, "\r")
	if len(l) > maxLine {
		return l[:maxLine] + "…"
	}
	return l
}
