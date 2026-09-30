package ai

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode/utf8"
)

// newNonce returns 128 random bits as hex. A fresh nonce is used per ask so
// data cannot forge a closing delimiter it has never seen.
func newNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error.
	return hex.EncodeToString(b[:])
}

// delimRE matches anything that looks like an eddy_data tag, in any case,
// with optional whitespace, so data cannot open or close a data block.
var delimRE = regexp.MustCompile(`(?i)<\s*/?\s*eddy_data`)

// sourceRE limits the source attribute to a safe charset.
var sourceRE = regexp.MustCompile(`[^A-Za-z0-9_:./-]`)

// Wrap marks content as untrusted data for the model:
//
//	<eddy_data nonce="N" source="tool:get_events">
//	…
//	</eddy_data nonce="N">
//
// Any tag-like "<eddy_data" or "</eddy_data" inside content is defused, and
// any occurrence of the nonce is replaced, so the content can neither close
// the block nor start a new one.
func Wrap(nonce, source, content string) string {
	content = delimRE.ReplaceAllStringFunc(content, func(m string) string {
		return "&lt;" + strings.TrimLeft(m, "<")
	})
	if nonce != "" {
		content = strings.ReplaceAll(content, nonce, "[nonce]")
	}
	source = sourceRE.ReplaceAllString(source, "_")
	var b strings.Builder
	b.Grow(len(content) + 96)
	b.WriteString(`<eddy_data nonce="`)
	b.WriteString(nonce)
	b.WriteString(`" source="`)
	b.WriteString(source)
	b.WriteString("\">\n")
	b.WriteString(content)
	b.WriteString("\n</eddy_data nonce=\"")
	b.WriteString(nonce)
	b.WriteString("\">")
	return b.String()
}

// truncate cuts s to at most n bytes on a UTF-8 boundary and reports whether
// it cut anything.
func truncate(s string, n int) (string, bool) {
	if n <= 0 || len(s) <= n {
		return s, false
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// firstRunes returns at most n runes of s, trimmed, on one line.
func firstRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n]))
}
