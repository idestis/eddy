// Package redact masks secrets in text and Kubernetes YAML before it reaches
// an AI provider or an MCP client (ADR-0003 §6, "Redaction").
//
// The redactor is deliberately conservative: it prefers masking a harmless
// value over leaking a credential. It keeps the data that makes Flux
// troubleshooting possible, such as revisions ("main@sha1:<hex>"), image
// digests ("sha256:<hex>"), resource names and SSH-style Git URLs without a
// password ("ssh://git@github.com/org/repo").
//
// Every mask has the form "[REDACTED:<kind>]". Both entry points return the
// number of masks applied, which callers write to the audit log.
package redact

import (
	"math"
	"regexp"
	"strings"
)

// Mask returns the replacement text for a value of the given kind.
func Mask(kind string) string { return "[REDACTED:" + kind + "]" }

const maskPrefix = "[REDACTED:"

// pattern is one masking rule applied by Text.
type pattern struct {
	kind string
	re   *regexp.Regexp
	// repl builds the replacement for one match. It reports false to keep
	// the match unchanged (a false-positive guard). Nil means "mask it all".
	repl func(m []string) (string, bool)
}

var patterns = []pattern{
	// PEM blocks, including a truncated block with no END line.
	{kind: "pem", re: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+-----(?:[\s\S]*?-----END [A-Z0-9 ]+-----|[A-Za-z0-9+/=\s\\n]*)`)},
	// JSON Web Tokens: base64url header and payload, both starting with '{"'.
	{kind: "jwt", re: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`)},
	{kind: "aws_key", re: regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{kind: "eddy_pat", re: regexp.MustCompile(`eddy_pat_[0-9A-Za-z]{10,}`)},
	{kind: "github_token", re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)},
	{kind: "slack_token", re: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{kind: "anthropic_key", re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{10,}`)},
	// HTTP auth schemes. The scheme is kept so the reader knows what it was.
	{
		kind: "auth",
		re:   regexp.MustCompile(`(?i)\b(bearer|basic)([ \t]+)([A-Za-z0-9._~+/-]{8,}=*)`),
		repl: func(m []string) (string, bool) {
			if strings.HasPrefix(m[3], maskPrefix) || !looksLikeCredential(m[3]) {
				return "", false
			}
			return m[1] + m[2] + Mask("auth"), true
		},
	},
	// URLs with a password in the userinfo. "git@host" without a password is kept.
	{
		kind: "userinfo",
		re:   regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s:@]*:[^/\s@]+@`),
		repl: func(m []string) (string, bool) { return m[1] + Mask("userinfo") + "@", true },
	},
	// key=value and key: value pairs whose key names a secret.
	{
		kind: "secret",
		re: regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?)[A-Za-z0-9_.-]*)` +
			`(["']?[ \t]*[:=][ \t]*)(["']?)([^\s"',;&}\]]+)`),
		repl: func(m []string) (string, bool) {
			key, sep, quote, val := m[1], m[2], m[3], m[4]
			if strings.HasPrefix(val, maskPrefix) || isReferenceKey(key) || isHarmlessValue(val) {
				return "", false
			}
			return key + sep + quote + Mask("secret"), true
		},
	},
}

// tokenRE finds candidate high-entropy tokens.
var tokenRE = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)

// uuidRE matches Kubernetes UIDs, which are identifiers rather than secrets.
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// digestPrefixes precede hex strings that are revisions or digests, which
// are useful for troubleshooting and are not secret.
var digestPrefixes = []string{"sha1:", "sha256:", "sha384:", "sha512:", "blake3:", "@", "/"}

// Text masks credentials in free text, such as log lines, event messages and
// thread bodies. It returns the redacted text and the number of masks.
func Text(s string) (string, int) {
	n := 0
	for _, p := range patterns {
		s = replace(s, p, &n)
	}
	s = maskHighEntropy(s, &n)
	return s, n
}

func replace(s string, p pattern, n *int) string {
	if p.repl == nil {
		return p.re.ReplaceAllStringFunc(s, func(string) string {
			*n++
			return Mask(p.kind)
		})
	}
	idx := p.re.FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range idx {
		m := make([]string, len(loc)/2)
		for i := range m {
			if loc[2*i] >= 0 {
				m[i] = s[loc[2*i]:loc[2*i+1]]
			}
		}
		out, ok := p.repl(m)
		if !ok {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(out)
		last = loc[1]
		*n++
	}
	b.WriteString(s[last:])
	return b.String()
}

func maskHighEntropy(s string, n *int) string {
	idx := tokenRE.FindAllStringIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range idx {
		from, to, ok := highEntropy(s[loc[0]:loc[1]], s[:loc[0]])
		if !ok {
			continue
		}
		b.WriteString(s[last : loc[0]+from])
		b.WriteString(Mask("high_entropy"))
		last = loc[0] + to
		*n++
	}
	b.WriteString(s[last:])
	return b.String()
}

// highEntropy decides whether tok (with the text before it) looks like, or
// contains, a random secret. It returns the byte range of tok to mask.
func highEntropy(tok, before string) (from, to int, ok bool) {
	if uuidRE.MatchString(tok) {
		return 0, 0, false
	}
	if isHex(tok) {
		for _, p := range digestPrefixes {
			if strings.HasSuffix(before, p) {
				return 0, 0, false
			}
		}
		// Hex has at most 4 bits per character; require both digits and
		// letters so runs like "0000…" or "deadbeef…" repeats stay.
		return 0, len(tok), hasDigit(tok) && hasLetter(tok) && entropy(tok) >= 3.0
	}
	// Standard base64 (for example a cloud secret key) may contain a few
	// slashes that split its runs; judge the whole token.
	if base64RE.MatchString(tok) && strings.Count(tok, "/") <= 2 && mixed(tok) && entropy(tok) >= threshold(len(tok)) {
		return 0, len(tok), true
	}
	// Random keys have a long separator-free run that mixes upper, lower
	// and digits with near-maximal entropy for its length. Object ids,
	// paths and CamelCase names are split by separators or fall well
	// below the threshold. Only the run is masked, so the path around a
	// webhook secret stays readable.
	if from, to := longestRun(tok); to-from >= minRun {
		run := tok[from:to]
		if mixed(run) && entropy(run) >= threshold(len(run)) {
			return from, to, true
		}
	}
	return 0, 0, false
}

// minRun is the shortest separator-free run judged by entropy.
const minRun = 24

var base64RE = regexp.MustCompile(`^[A-Za-z0-9+/]+=*$`)

// threshold is the entropy a random base62 string of length n exceeds
// about 99% of the time (log2(n) - 0.9, capped). Measured: n=24 p1≈3.9,
// n=32 p1≈4.2, n=40 p1≈4.45; CamelCase identifiers of 35-40 characters
// score about 3.9-4.0.
func threshold(n int) float64 { return min(math.Log2(float64(n))-0.9, 4.8) }

func mixed(s string) bool { return hasDigit(s) && hasUpper(s) && hasLower(s) }

// longestRun returns the byte range of the longest part of s between
// separators ('/', '_', '-').
func longestRun(s string) (from, to int) {
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '/' || s[i] == '_' || s[i] == '-' {
			if i-start > to-from {
				from, to = start, i
			}
			start = i + 1
		}
	}
	return from, to
}

// entropy returns the Shannon entropy of s in bits per byte.
func entropy(s string) float64 {
	var freq [256]int
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	var h float64
	l := float64(len(s))
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := float64(c) / l
		h -= p * math.Log2(p)
	}
	return h
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

func hasDigit(s string) bool  { return strings.ContainsAny(s, "0123456789") }
func hasUpper(s string) bool  { return strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") }
func hasLower(s string) bool  { return strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz") }
func hasLetter(s string) bool { return hasUpper(s) || hasLower(s) }

// looksLikeCredential guards the bearer/basic rule against prose such as
// "basic authentication": a credential has a digit or a token symbol.
func looksLikeCredential(v string) bool {
	return hasDigit(v) || strings.ContainsAny(v, "._~+/=-")
}

// referenceSuffixes mark keys whose values name another object (for
// example secretRef, tokenSecretName, passwordFile) rather than holding the
// secret itself.
var referenceSuffixes = []string{"ref", "name", "names", "namespace", "path", "file", "kind", "type", "selector", "keys"}

func isReferenceKey(key string) bool {
	k := strings.ToLower(strings.Trim(key, `"'`))
	for _, s := range referenceSuffixes {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

// harmlessValues are words that follow "secret:" or "token:" in error
// messages rather than being a secret ("secret: not found").
var harmlessValues = map[string]bool{
	"true": true, "false": true, "null": true, "~": true, "none": true, "nil": true,
	"not": true, "is": true, "was": true, "missing": true, "required": true,
	"invalid": true, "expired": true, "found": true, "empty": true, "unset": true,
	"|": true, "|-": true, ">": true, ">-": true, "{": true, "[": true, "[]": true, "{}": true,
}

func isHarmlessValue(v string) bool { return harmlessValues[strings.ToLower(v)] }
