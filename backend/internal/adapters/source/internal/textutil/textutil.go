// Package textutil has small helpers shared by the source adapters.
package textutil

import (
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	tagRe   = regexp.MustCompile(`<[^>]+>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// StripHTML removes tags, unescapes entities and collapses whitespace.
func StripHTML(s string) string {
	s = html.UnescapeString(s)
	s = tagRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// Truncate cuts s to at most max runes, adding "..." when cut.
func Truncate(s string, max int) string {
	if max <= 3 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-3]) + "..."
}
