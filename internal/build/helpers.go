package build

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"
)

// Shared helpers for the page emitters. Everything here is small and boring on purpose: the
// interesting logic belongs in the family that needs it, and anything that ends up here is
// something two families genuinely share.

// htmlOf marks already-rendered HTML as safe for html/template.
//
// The ONLY values that may go through this are outputs of internal/markdown and
// internal/highlight — both of which produce HTML deliberately, from content they have already
// made inert. Everything else must reach a template as a plain string so the template escapes
// it. If you find yourself reaching for this anywhere else, the answer is no.
func htmlOf(s string) template.HTML { return template.HTML(s) }

// decodePath turns the percent-encoded segments of a route back into literal filenames.
//
// The URL is what a browser asks for; the filename is what the host must have. `read%20me.md`
// in a URL is `read me.md` on disk, and a static host decodes before it looks. Only `#` and `%`
// break this round trip, and routes.IsRawServable has already excluded both.
func decodePath(rel string) (string, error) {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		decoded, err := url.PathUnescape(p)
		if err != nil {
			return "", fmt.Errorf("path segment %q is not valid percent-encoding: %w", p, err)
		}
		parts[i] = decoded
	}
	return strings.Join(parts, "/"), nil
}

// pluralise returns one or many depending on n. Templates have `plural`; Go code has this.
func pluralise(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// deref reads a *string that may be nil.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
