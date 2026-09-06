// Package frontmatter parses the YAML frontmatter of hand-written markdown — the port of
// src/lib/frontmatter.ts.
//
// It reads a deliberately small subset of YAML: `key: scalar`, `key: [a, b]`, `key:` followed
// by `- item` lines, and — in Maps, kept apart from Data — `key:` followed by indented
// `label: scalar` lines. Anything fancier — a mapping nested two deep, anchors, block scalars,
// multi-document files — makes the parser DROP that key rather than guess at its meaning. A
// partial parse is worse than a missing one here: a half-understood `tags:` renders a chip
// reading `[a, b]` to a visitor, and nobody would ever look at it and see a bug.
//
// The other thing this file is careful about is where the block ENDS. Two halves of the notes
// feature need the same answer — ingest reads keys out of the block, the note viewer has to
// hide exactly the same span before rendering the rest as markdown — and while they carried
// separate regexes they drifted: a block closed with YAML's `...` terminator parsed correctly
// during ingest and then rendered its own metadata as prose on the page.
package frontmatter

import (
	"regexp"
	"strings"
)

// Value is a frontmatter value. Only strings and string lists are produced: the artifact needs
// title/description/date (strings) and tags (a list), and everything else is skipped rather
// than guessed at.
type Value struct {
	// Str is set when the value is a scalar.
	Str string
	// List is set when the value is a sequence. Exactly one of Str/List is meaningful, which
	// IsList decides.
	List []string
	// IsList distinguishes an empty list from an empty string.
	IsList bool
}

// MapEntry is one `label: scalar` pair of a nested mapping. Entries keep the order they were
// written in: `links:` becomes a row of pills, and a reader who put GitHub first meant it.
type MapEntry struct {
	Key   string
	Value string
}

// Frontmatter is a parsed block plus the prose after it.
type Frontmatter struct {
	// Data holds the scalar and sequence keys the parser understood. Unsupported constructs
	// are absent, never partially parsed.
	Data map[string]Value
	// Maps holds one-level nested mappings — `forges:` on the profile, `links:` on an
	// organization — which are a mapping of key to SCALAR and nothing deeper.
	//
	// Kept apart from Data rather than folded into Value because Data is a cross-language
	// contract: testdata/expected.json is dumped from the TypeScript notes parser, which has
	// no notion of a nested map and drops the key. Notes ingest reads Data and is unchanged;
	// the site build reads both, which is what puts the profile's forge pills and an
	// organization's link pills back on the page.
	//
	// A key is here only when EVERY line of its block is `key: scalar` at one indentation. One
	// deeper line and the whole key is dropped, the same rule Data follows.
	Maps map[string][]MapEntry
	// Body is everything after the closing delimiter, joined with "\n".
	Body string
}

// Split is a frontmatter block located inside a file, before its keys are parsed.
type Split struct {
	// Raw is the lines between the delimiters, joined with "\n"; empty when there is no block.
	// Never includes the `---` / `...` delimiter lines.
	Raw string
	// Body is everything after the closing delimiter; the whole file when there is no block.
	Body string
	// Present is false when the file opens with no `---`, or opens one that is never closed.
	Present bool
}

// utf8BOM is the byte order mark an editor on Windows will happily add to a markdown file.
// Built from its code point rather than written as a literal, because a literal BOM is only
// legal at the very start of a Go source file.
var utf8BOM = string(rune(0xFEFF))

var lineSplit = regexp.MustCompile(`\r\n|\n|\r`)

// isCloser — YAML closes a document with `---` (a new one starts) or `...` (this one ends).
func isCloser(trimmed string) bool { return trimmed == "---" || trimmed == "..." }

// SplitFile separates a markdown file into its leading YAML frontmatter block and everything
// after it.
//
// A file whose first line is not `---`, or whose block is never closed, is treated as having
// NO frontmatter: its whole text is the body. Silently swallowing an unterminated block would
// hide the author's typo behind an empty-looking note.
func SplitFile(src string) Split {
	text := strings.TrimPrefix(src, utf8BOM)
	lines := lineSplit.Split(text, -1)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Split{Body: text}
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if isCloser(strings.TrimSpace(lines[i])) {
			end = i
			break
		}
	}
	if end == -1 {
		return Split{Body: text}
	}
	return Split{
		Raw:     strings.Join(lines[1:end], "\n"),
		Body:    strings.Join(lines[end+1:], "\n"),
		Present: true,
	}
}

var (
	// keyLine matches `key:` or `key: value` at the top level of the block (no indentation).
	keyLine = regexp.MustCompile(`^([A-Za-z_][\w.-]*)[ \t]*:(?:[ \t]+(.*))?$`)
	// itemLine matches `- item`, at any indentation.
	itemLine = regexp.MustCompile(`^[ \t]*-[ \t]+(.*)$`)
	// nestedItem matches a block-sequence item that is itself a mapping (`- name: x`).
	nestedItem = regexp.MustCompile(`^[A-Za-z_][\w.-]*\s*:(\s|$)`)
	// plainComment finds a `#` that follows whitespace (or begins the line) — a YAML comment.
	plainComment = regexp.MustCompile(`(^|\s)#`)
	leadingSpace = regexp.MustCompile(`^\s`)
)

// Parse reads the supported YAML subset out of a markdown file's frontmatter block.
func Parse(src string) Frontmatter {
	split := SplitFile(src)
	if !split.Present {
		return Frontmatter{Data: map[string]Value{}, Maps: map[string][]MapEntry{}, Body: split.Body}
	}
	lines := strings.Split(split.Raw, "\n")
	data := map[string]Value{}
	maps := map[string][]MapEntry{}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		m := keyLine.FindStringSubmatch(line)
		if m == nil {
			continue // indented continuation, a list item without a key, or anything fancier
		}
		key := m[1]
		rawValue := strings.TrimSpace(m[2])

		if rawValue != "" && !strings.HasPrefix(rawValue, "#") {
			switch {
			case strings.HasPrefix(rawValue, "{"):
				// flow mapping — outside the supported subset
			case strings.HasPrefix(rawValue, "["):
				if list, ok := parseFlowSequence(rawValue); ok {
					data[key] = Value{List: list, IsList: true}
				}
			default:
				if scalar, ok := parseScalar(rawValue); ok {
					data[key] = Value{Str: scalar}
				}
			}
			continue
		}

		// `key:` with nothing after it — a block sequence, or something unsupported. Either
		// way the indented lines belong to this key and must not be re-read as keys of their
		// own.
		var items []string
		supported := true
		j := i + 1
		for ; j < len(lines); j++ {
			next := lines[j]
			nextTrimmed := strings.TrimSpace(next)
			if nextTrimmed == "" || strings.HasPrefix(nextTrimmed, "#") {
				continue
			}
			item := itemLine.FindStringSubmatch(next)
			if item == nil {
				if leadingSpace.MatchString(next) {
					supported = false // indented, but not `- item`: a nested mapping
				}
				break
			}
			value := strings.TrimSpace(item[1])
			if nestedItem.MatchString(value) || opensCollection(value) {
				supported = false
				break
			}
			if scalar, ok := parseScalar(value); ok {
				items = append(items, scalar)
			}
		}
		// Consume the block either way: on the unsupported path its lines must not become keys.
		blockStart := i + 1
		for j < len(lines) && leadingSpace.MatchString(lines[j]) && strings.TrimSpace(lines[j]) != "" {
			j++
		}
		i = j - 1
		switch {
		case supported && len(items) > 0:
			data[key] = Value{List: items, IsList: true}
		case !supported && len(items) == 0:
			// Not a sequence. It may still be the one nested shape the site build needs.
			if entries, ok := parseNestedMap(lines[blockStart:j]); ok {
				maps[key] = entries
			}
		}
	}

	return Frontmatter{Data: data, Maps: maps, Body: split.Body}
}

// parseNestedMap reads an indented block as a one-level mapping of key to scalar.
//
// ok is false unless EVERY non-blank, non-comment line is `key: scalar` at the same
// indentation. A second level, a sequence, a flow collection or a bare `key:` fails the whole
// block: the caller then drops the key, which is this package's rule everywhere else. Half a
// mapping on a page is a wrong answer nobody would recognise as a bug.
func parseNestedMap(lines []string) ([]MapEntry, bool) {
	var out []MapEntry
	indent := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lead := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if lead == "" {
			return nil, false // a top-level line: not part of this block at all
		}
		if indent == "" {
			indent = lead
		} else if lead != indent {
			return nil, false // a deeper (or shallower) level — outside the subset
		}
		m := keyLine.FindStringSubmatch(trimmed)
		if m == nil {
			return nil, false
		}
		value := strings.TrimSpace(m[2])
		if value == "" || opensCollection(value) {
			return nil, false
		}
		scalar, ok := parseScalar(value)
		if !ok {
			return nil, false
		}
		out = append(out, MapEntry{Key: m[1], Value: scalar})
	}
	return out, len(out) > 0
}

// opensCollection reports a block-sequence item that opens a nested collection (`- [a, b]`,
// `- {k: v}`).
//
// Checked separately from nestedItem because a flow collection is still one "item" to the line
// splitter: without this it would be captured as the plain scalar "[a, b]" and shown to a
// reader as a tag chip reading `[a, b]` — precisely the partial parse this package rules out.
func opensCollection(value string) bool {
	return strings.HasPrefix(value, "[") || strings.HasPrefix(value, "{")
}

// stripPlainComment removes a trailing YAML comment from a plain scalar.
func stripPlainComment(value string) string {
	loc := plainComment.FindStringSubmatchIndex(value)
	if loc == nil {
		return value
	}
	// loc[2]:loc[3] is the leading-whitespace group; keep it out of the value.
	cut := loc[0]
	if loc[2] >= 0 && loc[3] > loc[2] {
		cut = loc[0] + (loc[3] - loc[2])
	}
	return value[:cut]
}

// parseScalar reads one scalar: a double-quoted string (with \\, \", \n, \r, \t escapes), a
// single-quoted string (” escapes a quote), or a plain scalar with any trailing comment
// removed. ok is false for an empty value or an unterminated quote — the caller then drops the
// key instead of inventing a value.
func parseScalar(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	if strings.HasPrefix(value, `"`) {
		var out strings.Builder
		runes := []rune(value)
		for i := 1; i < len(runes); i++ {
			ch := runes[i]
			if ch == '\\' {
				i++
				if i >= len(runes) {
					return "", false
				}
				switch runes[i] {
				case 'n':
					out.WriteByte('\n')
				case 'r':
					out.WriteByte('\r')
				case 't':
					out.WriteByte('\t')
				default:
					out.WriteRune(runes[i])
				}
				continue
			}
			if ch == '"' {
				return out.String(), true
			}
			out.WriteRune(ch)
		}
		return "", false // unterminated
	}
	if strings.HasPrefix(value, "'") {
		var out strings.Builder
		runes := []rune(value)
		for i := 1; i < len(runes); i++ {
			ch := runes[i]
			if ch == '\'' {
				if i+1 < len(runes) && runes[i+1] == '\'' {
					out.WriteByte('\'')
					i++
					continue
				}
				return out.String(), true
			}
			out.WriteRune(ch)
		}
		return "", false // unterminated
	}
	plain := strings.TrimSpace(stripPlainComment(value))
	if plain == "" {
		return "", false
	}
	return plain, true
}

// splitFlowItems splits a flow sequence body (`a, "b, c", d`) on commas that are not inside
// quotes. ok is false when a nested collection appears — a list of lists or maps is outside
// the supported subset.
func splitFlowItems(body string) ([]string, bool) {
	var items []string
	var current strings.Builder
	var quote rune
	runes := []rune(body)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if quote != 0 {
			current.WriteRune(ch)
			if ch == '\\' && quote == '"' {
				i++
				if i < len(runes) {
					current.WriteRune(runes[i])
				}
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			current.WriteRune(ch)
			continue
		}
		if ch == '[' || ch == '{' || ch == ']' || ch == '}' {
			return nil, false
		}
		if ch == ',' {
			items = append(items, current.String())
			current.Reset()
			continue
		}
		current.WriteRune(ch)
	}
	if quote != 0 {
		return nil, false // unterminated quote
	}
	items = append(items, current.String())
	return items, true
}

// parseFlowSequence turns `[a, b]` into ["a","b"]; ok is false when the value is not a flat
// flow sequence. A trailing comment is allowed (`[a, b] # why`): the closing bracket is taken
// to be the LAST `]` on the line, so a `#` inside a quoted item does not truncate the list.
func parseFlowSequence(raw string) ([]string, bool) {
	close := strings.LastIndex(raw, "]")
	if close == -1 {
		return nil, false
	}
	rest := strings.TrimSpace(raw[close+1:])
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return nil, false
	}
	parts, ok := splitFlowItems(raw[1:close])
	if !ok {
		return nil, false
	}
	out := []string{}
	for _, part := range parts {
		if item, ok := parseScalar(part); ok {
			out = append(out, item)
		}
	}
	return out, true
}

/* ---- headings -------------------------------------------------------------- */

var (
	// fence matches a fenced code block opener/closer; an H1 inside one is not a heading.
	fence = regexp.MustCompile("^\\s{0,3}(```+|~~~+)")
	atxH1 = regexp.MustCompile(`^\s{0,3}#[ \t]+(.+?)[ \t]*#*\s*$`)
	// leadingH1 matches a document-opening level-1 heading, ATX or setext, capturing the text.
	leadingH1 = regexp.MustCompile(`^[ \t]*(?:\r?\n)*(?:#[ \t]+([^\r\n]*?)[ \t]*#*[ \t]*(?:\r?\n|$)|([^\r\n]+)\r?\n=+[ \t]*(?:\r?\n|$))`)
)

// FirstHeading is the text of the first ATX H1 outside a fenced code block, or "".
//
// Setext headings are deliberately not recognised here: they are rare in these files and the
// two-line lookahead is one more thing to get wrong.
//
// Pass prose only — Parse(src).Body, never the raw file. A `#` line inside a frontmatter block
// is a YAML comment, and scanning raw text turns it into a title.
func FirstHeading(body string) string {
	var open string
	for _, raw := range lineSplit.Split(body, -1) {
		if m := fence.FindStringSubmatch(raw); m != nil {
			marker := string(m[1][0])
			if open == "" {
				open = marker
			} else if open == marker {
				open = ""
			}
			continue
		}
		if open != "" {
			continue
		}
		if m := atxH1.FindStringSubmatch(raw); m != nil {
			if title := strings.TrimSpace(m[1]); title != "" {
				return title
			}
		}
	}
	return ""
}

// StripLeadingHeading drops a document-opening H1 from a markdown body, but ONLY when it is
// `title` repeating itself.
//
// A note page prints the note's title as the page's one <h1>, and when that title came from the
// H1 fallback, rendering the heading again echoes the title immediately under itself. That is
// the only case worth removing. Removing the first heading unconditionally deletes real content
// instead — a file whose frontmatter set a different title, or the second markdown file of a
// folder note, would lose its opening heading with nothing to show it was ever there.
func StripLeadingHeading(body, title string) string {
	m := leadingH1.FindStringSubmatchIndex(body)
	if m == nil {
		return body
	}
	text := ""
	if m[2] >= 0 {
		text = body[m[2]:m[3]]
	} else if m[4] >= 0 {
		text = body[m[4]:m[5]]
	}
	text = strings.TrimSpace(text)
	if text != "" && text == strings.TrimSpace(title) {
		return body[m[1]:]
	}
	return body
}
