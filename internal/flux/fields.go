package flux

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Helpers for reading unstructured objects without copying. They never fail:
// a missing or mistyped field reads as its zero value.

func field(obj map[string]any, path ...string) (any, bool) {
	var cur any = obj
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func str(obj map[string]any, path ...string) string {
	v, _ := field(obj, path...)
	s, _ := v.(string)
	return s
}

func boolean(obj map[string]any, path ...string) bool {
	v, _ := field(obj, path...)
	b, _ := v.(bool)
	return b
}

// integer reads a number, reporting whether it was present.
func integer(obj map[string]any, path ...string) (int64, bool) {
	v, ok := field(obj, path...)
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}

func mapping(obj map[string]any, path ...string) map[string]any {
	v, _ := field(obj, path...)
	m, _ := v.(map[string]any)
	return m
}

func list(obj map[string]any, path ...string) []any {
	v, _ := field(obj, path...)
	l, _ := v.([]any)
	return l
}

// maps returns the map elements of a list, skipping anything else.
func maps(obj map[string]any, path ...string) []map[string]any {
	var out []map[string]any
	for _, e := range list(obj, path...) {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func timestamp(obj map[string]any, path ...string) time.Time {
	t, err := time.Parse(time.RFC3339, str(obj, path...))
	if err != nil {
		return time.Time{}
	}
	return t
}

// OneLine collapses whitespace (including newlines) and caps s at max runes,
// adding an ellipsis when it cuts.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}
