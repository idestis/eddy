package redact

import (
	"regexp"
	"strings"
)

// dropKeys are mapping keys whose whole value is removed, wherever they
// appear. data/stringData/binaryData hold Secret and ConfigMap payloads;
// the last-applied annotation repeats the full object, secrets included.
var dropKeys = map[string]string{
	"data":       "data",
	"stringData": "data",
	"binaryData": "data",
	"kubectl.kubernetes.io/last-applied-configuration": "last_applied",
}

// keyLineRE splits a YAML mapping line into indent, optional list marker,
// key and the rest. It handles plain, single- and double-quoted keys.
var keyLineRE = regexp.MustCompile(`^(\s*)((?:-\s+)*)("[^"]*"|'[^']*'|[^\s:#'"][^:#]*?):(\s.*|)$`)

// secretKeyRE matches keys that hold a secret directly.
var secretKeyRE = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?)`)

// YAML redacts a Kubernetes object rendered as YAML. It removes data,
// stringData and binaryData blocks, the values of env[] entries (names and
// valueFrom are kept), the last-applied-configuration annotation and block
// scalars under secret-looking keys, then applies Text to what is left.
// It works line by line so the output keeps the input's layout, and it never
// fails on malformed input.
func YAML(s string) (string, int) {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	n := 0

	// skipIndent >= 0 drops every following line indented deeper than it.
	skipIndent := -1
	// envIndent >= 0 while inside an env: list; its entries may sit at the
	// same indent as the key ("env:\n- name: x").
	envIndent := -1

	for i, line := range lines {
		ind := indentOf(line)
		blank := strings.TrimSpace(line) == ""

		if skipIndent >= 0 {
			if blank || ind > skipIndent {
				continue
			}
			skipIndent = -1
		}
		if envIndent >= 0 && !blank {
			trimmed := strings.TrimLeft(line, " \t")
			if ind < envIndent || ind == envIndent && !strings.HasPrefix(trimmed, "-") {
				envIndent = -1
			}
		}

		m := keyLineRE.FindStringSubmatch(line)
		if m == nil {
			out = append(out, line)
			continue
		}
		lead, dash, key, rest := m[1], m[2], m[3], m[4]
		bare := strings.Trim(key, `"'`)
		keyIndent := len(lead) + len(dash)
		value := strings.TrimSpace(stripComment(rest))

		if kind, ok := dropKeys[bare]; ok {
			if value != "" && value != "{}" && value != "[]" && value != "null" {
				n++
			} else if hasChildren(lines, i) {
				n++
			}
			out = append(out, lead+dash+key+": "+Mask(kind))
			skipIndent = keyIndent
			continue
		}

		if bare == "env" && value == "" {
			envIndent = keyIndent
			out = append(out, line)
			continue
		}

		if envIndent >= 0 && bare == "value" {
			if value != "" && value != `""` && value != "''" {
				n++
			}
			out = append(out, lead+dash+key+": "+Mask("env"))
			skipIndent = keyIndent
			continue
		}

		if isBlockScalar(value) && secretKeyRE.MatchString(bare) && !isReferenceKey(bare) {
			n++
			out = append(out, lead+dash+key+": "+Mask("secret"))
			skipIndent = keyIndent
			continue
		}

		out = append(out, line)
	}

	text, m := Text(strings.Join(out, "\n"))
	return text, n + m
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

func isBlockScalar(v string) bool {
	return v != "" && (v[0] == '|' || v[0] == '>')
}

func stripComment(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		return s[:i]
	}
	return s
}

// hasChildren reports whether the next non-blank line after lines[i] is
// nested under it: deeper, or a list item at the same indent.
func hasChildren(lines []string, i int) bool {
	for _, next := range lines[i+1:] {
		if strings.TrimSpace(next) == "" {
			continue
		}
		ind, own := indentOf(next), indentOf(lines[i])
		return ind > own || ind == own && strings.HasPrefix(strings.TrimLeft(next, " \t"), "-")
	}
	return false
}
