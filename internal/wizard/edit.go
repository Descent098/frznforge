package wizard

// Textual editing of frznforge.config.jsonc — the wizard's engine, ported from
// scripts/lib/config-edit.ts.
//
// The contract survives the change of language and of file format, and it is the whole point:
// the config file is hand-written and mostly comments, so re-serialising it would destroy the
// documentation the user wrote. Every editor here is a SPLICE — only the bytes of the field
// actually being changed move, and everything else (comments, blank lines, indentation,
// untouched fields) comes through byte for byte. Navigation is comment- and string-aware (the
// walkers below), never a regex over the whole file: a `repos: [` inside a commented-out block
// must not be what an edit lands in.
//
// The dialect is the one internal/config/jsonc.go reads: JSON, plus `//` and `/* */` comments,
// plus a trailing comma before a closing bracket. That last one is what lets an append write
// `item,` and still leave a file that parses. Two things are simpler than in the TypeScript
// this replaces: `"` is the only string delimiter (a `'` is just a character), and there is no
// `defineConfig(` wrapper — the root object is the file.
//
// internal/config/migrate.go has a scanner over the same dialect. It is not reused because it
// yields tokens for a converter, and a splice needs byte spans in the original text; the rules
// it encodes about strings and comments are the ones repeated here.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

/* ------------------------------------------------------------------ value rendering */

// jsonQuote renders a Go string as a JSON string literal.
//
// encoding/json is not used because it escapes <, > and & — a config full of URLs and prose
// would come out unreadable, and this file is meant to be edited by hand afterwards. Same
// reasoning, same implementation as internal/config/migrate.go's quoting.
func jsonQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		case r == utf8.RuneError && size == 1:
			b.WriteString(`�`)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
	return b.String()
}

// field is one key/value pair of an object the wizard writes.
//
// An ordered slice rather than a map because the output is a file a person reads: two saves of
// the same settings must produce the same bytes, and Go's map iteration order would make the
// key order of a new entry random.
type field struct {
	key   string
	value any
}

// object is a rendered object literal, in the order its fields will be written.
type object []field

// renderValue renders a value as one line of JSONC source.
//
// Only the shapes the wizard actually writes are supported; anything else is a programming
// error rather than a user-facing one, so it panics loudly instead of silently emitting
// something the config schema would then have to refuse.
func renderValue(v any) string {
	switch value := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(value)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case float64:
		// 'f' with -1 precision keeps 524288 an integer instead of 524288.0 — JSON numbers
		// arrive as float64 and most of these fields are byte counts.
		return strconv.FormatFloat(value, 'f', -1, 64)
	case string:
		return jsonQuote(value)
	case []string:
		parts := make([]string, len(value))
		for i, s := range value {
			parts[i] = jsonQuote(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []any:
		parts := make([]string, len(value))
		for i, item := range value {
			parts[i] = renderValue(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case object:
		if len(value) == 0 {
			return "{}"
		}
		parts := make([]string, len(value))
		for i, f := range value {
			parts[i] = jsonQuote(f.key) + ": " + renderValue(f.value)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	default:
		panic(fmt.Sprintf("wizard: cannot render %T into config source", v))
	}
}

/* ------------------------------------------------------------------ source walkers */

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f'
}

// skipString returns the index just past the string literal starting at src[i] == '"'. An
// unterminated literal runs to end, which keeps a malformed file from looping forever.
func skipString(src string, i, end int) int {
	for j := i + 1; j < end; j++ {
		switch src[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return end
}

// skipComment returns the index just past a comment starting at i, or i when none starts there.
// An unterminated block comment runs to end.
func skipComment(src string, i, end int) int {
	if i+1 >= end || src[i] != '/' {
		return i
	}
	switch src[i+1] {
	case '/':
		if nl := strings.IndexByte(src[i:end], '\n'); nl >= 0 {
			return i + nl + 1
		}
		return end
	case '*':
		if c := strings.Index(src[i+2:end], "*/"); c >= 0 {
			return i + 2 + c + 2
		}
		return end
	}
	return i
}

// skipTrivia returns the index of the next byte that is neither whitespace nor part of a
// comment.
func skipTrivia(src string, i, end int) int {
	for i < end {
		if isSpaceByte(src[i]) {
			i++
			continue
		}
		if next := skipComment(src, i, end); next != i {
			i = next
			continue
		}
		return i
	}
	return end
}

// matchBracket walks from an opening bracket to its match, skipping strings and comments, and
// returns -1 when the file does not close it.
func matchBracket(src string, open int) int {
	var closer byte
	switch src[open] {
	case '[':
		closer = ']'
	case '{':
		closer = '}'
	default:
		return -1
	}
	depth := 0
	for i := open; i < len(src); {
		c := src[i]
		if c == '/' {
			if next := skipComment(src, i, len(src)); next != i {
				i = next
				continue
			}
		}
		if c == '"' {
			i = skipString(src, i, len(src))
			continue
		}
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				if c == closer {
					return i
				}
				return -1
			}
		}
		i++
	}
	return -1
}

// lastMeaningfulIndex is the index of the last byte of body that is neither whitespace nor part
// of a comment. It is what makes an append land after the last real value rather than after a
// trailing comment, which would put the new entry inside the comment.
func lastMeaningfulIndex(body string) int {
	last := -1
	for i := 0; i < len(body); {
		c := body[i]
		if c == '/' {
			if next := skipComment(body, i, len(body)); next != i {
				i = next
				continue
			}
		}
		if c == '"' {
			next := skipString(body, i, len(body))
			last = next - 1
			i = next
			continue
		}
		if !isSpaceByte(c) {
			last = i
		}
		i++
	}
	return last
}

// itemSpan is one top-level element of an array or object body, with the span it occupies.
type itemSpan struct {
	// start is the index just after the previous top-level comma (or 0).
	start int
	// end is the index of the next top-level comma, or len(body).
	end int
	// comma is the index of the trailing top-level comma, or -1 for the last, comma-less item.
	comma int
	text  string
}

// topLevelItemSpans splits an array or object body into its top-level elements, skipping
// strings and comments. The spans are what make a removal a splice: a removed item takes
// exactly its own bytes plus one separating comma with it.
func topLevelItemSpans(body string) []itemSpan {
	var spans []itemSpan
	depth := 0
	start := 0
	push := func(end, comma int) {
		text := body[start:end]
		if strings.TrimSpace(text) != "" {
			spans = append(spans, itemSpan{start: start, end: end, comma: comma, text: text})
		}
	}
	for i := 0; i < len(body); {
		c := body[i]
		if c == '/' {
			if next := skipComment(body, i, len(body)); next != i {
				i = next
				continue
			}
		}
		if c == '"' {
			i = skipString(body, i, len(body))
			continue
		}
		switch {
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			push(i, i)
			start = i + 1
		}
		i++
	}
	push(len(body), -1)
	return spans
}

// stripComments removes comments from one element's text, leaving string literals intact.
//
// topLevelItemSpans skips comments when it looks for separators, but the slice it hands back
// still contains them — and a commented-out entry sitting above a real one would otherwise be
// read as if it were the entry. Configs ship with exactly that inside `repos: [ … ]`.
func stripComments(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		c := text[i]
		if c == '/' && i+1 < len(text) {
			switch text[i+1] {
			case '/':
				next := skipComment(text, i, len(text))
				// The line comment ate its own newline; put one back so the following line
				// does not fuse onto this one.
				b.WriteByte('\n')
				i = next
				continue
			case '*':
				next := skipComment(text, i, len(text))
				b.WriteByte(' ')
				i = next
				continue
			}
		}
		if c == '"' {
			next := skipString(text, i, len(text))
			b.WriteString(text[i:next])
			i = next
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

/* ------------------------------------------------------------------ object navigation */

// keyRange locates one key/value pair inside an object body.
type keyRange struct {
	// keyStart is the index of the key token's opening quote.
	keyStart int
	// valueStart is the index of the value's first byte.
	valueStart int
	// valueEnd is one past the value's last meaningful byte — so a trailing comment after the
	// value is outside the span and survives an edit.
	valueEnd int
}

// decodeKey reads the JSON string starting at i and returns its value.
func decodeKey(src string, i, end int) (string, int, bool) {
	next := skipString(src, i, end)
	var out string
	if err := json.Unmarshal([]byte(src[i:next]), &out); err != nil {
		return "", next, false
	}
	return out, next, true
}

// findKeyRange finds `"key":` at the TOP level of the object body source[open+1:close].
//
// Comment- and string-aware, so a key of the same name inside a nested object, a string or a
// comment never matches — the property that keeps an edit to `site.title` out of
// `ingest.title`, and out of a block somebody commented away last year.
func findKeyRange(source string, open, close int, key string) (keyRange, bool) {
	depth := 0
	expectKey := true // at the body's start, and after every top-level comma
	for i := open + 1; i < close; {
		c := source[i]
		if c == '/' {
			if next := skipComment(source, i, close); next != i {
				i = next
				continue
			}
		}
		if c == '"' {
			if depth != 0 || !expectKey {
				i = skipString(source, i, close)
				continue
			}
			name, afterKey, ok := decodeKey(source, i, close)
			expectKey = false
			colon := skipTrivia(source, afterKey, close)
			if !ok || colon >= close || source[colon] != ':' {
				i = afterKey
				continue
			}
			if name != key {
				i = colon + 1
				continue
			}
			valueStart := skipTrivia(source, colon+1, close)
			return keyRange{
				keyStart:   i,
				valueStart: valueStart,
				valueEnd:   findValueEnd(source, valueStart, close),
			}, true
		}
		switch {
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			expectKey = true
		case depth == 0 && !isSpaceByte(c):
			// Not a quoted key: some other token (a stray value in a malformed file). Stop
			// expecting a key until the next comma rather than trying to interpret it.
			expectKey = false
		}
		i++
	}
	return keyRange{}, false
}

// findValueEnd walks to the next top-level comma and backs up over trailing whitespace and
// comments, so a same-line comment after the value is never part of the value's span.
func findValueEnd(source string, valueStart, close int) int {
	depth := 0
	i := valueStart
	for i < close {
		c := source[i]
		if c == '/' {
			if next := skipComment(source, i, close); next != i {
				i = next
				continue
			}
		}
		if c == '"' {
			i = skipString(source, i, close)
			continue
		}
		if c == ',' && depth == 0 {
			break
		}
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		}
		i++
	}
	last := lastMeaningfulIndex(source[valueStart:i])
	if last == -1 {
		return valueStart
	}
	return valueStart + last + 1
}

// findRootObject returns the brace span of the file's single top-level object.
//
// Comment-aware for the same reason everything here is: a config whose first line is a
// commented-out `{ … }` example must not be edited inside the comment.
func findRootObject(source string) (open, close int, ok bool) {
	i := skipTrivia(source, 0, len(source))
	if i >= len(source) || source[i] != '{' {
		return 0, 0, false
	}
	end := matchBracket(source, i)
	if end == -1 {
		return 0, 0, false
	}
	return i, end, true
}

// readField returns the string value of key at the top level of one array element's object
// literal. Values that are not strings (a number, a nested object) report false: every field
// the wizard matches on — a slug, an owner, a repo — is a string.
func readField(item, key string) (string, bool) {
	open := -1
	for i := 0; i < len(item); {
		c := item[i]
		if c == '/' {
			if next := skipComment(item, i, len(item)); next != i {
				i = next
				continue
			}
		}
		if c == '"' {
			i = skipString(item, i, len(item))
			continue
		}
		if c == '{' {
			open = i
			break
		}
		i++
	}
	if open == -1 {
		return "", false
	}
	close := matchBracket(item, open)
	if close == -1 {
		return "", false
	}
	r, ok := findKeyRange(item, open, close, key)
	if !ok || r.valueStart >= len(item) || item[r.valueStart] != '"' {
		return "", false
	}
	var out string
	if err := json.Unmarshal([]byte(item[r.valueStart:r.valueEnd]), &out); err != nil {
		return "", false
	}
	return out, true
}

/* ------------------------------------------------------------------ splices */

// editResult is the outcome of one splice. changed is false when the file already said what
// was asked for — a no-op must not take a backup or count as a write.
type editResult struct {
	text    string
	changed bool
}

// appendToBody inserts one already-rendered line into the body open..close, indented to match
// the block it joins.
func appendToBody(source string, open, close int, line, indent string) string {
	body := source[open+1 : close]
	last := lastMeaningfulIndex(body)
	closeIndent := ""
	if len(indent) >= 2 {
		closeIndent = indent[:len(indent)-2]
	}
	var newBody string
	if last == -1 {
		newBody = "\n" + indent + line + ",\n" + closeIndent
	} else {
		head := body[:last+1]
		comma := ","
		if body[last] == ',' {
			comma = ""
		}
		tail := body[last+1:]
		trailing := tail[len(strings.TrimRight(tail, " \t\r\n")):]
		keep := tail[:len(tail)-len(trailing)]
		closeGap := trailing
		if !strings.Contains(trailing, "\n") {
			closeGap = "\n" + closeIndent
		}
		newBody = head + comma + keep + "\n" + indent + line + closeGap
	}
	return source[:open+1] + newBody + source[close:]
}

// descend walks the key chain path[:len(path)-1] from the root object, returning the innermost
// block's brace span. missing names the first key of the chain that is absent (so the caller
// can create it); ok is false when a key exists but is not an object literal, which is a shape
// no splice can safely edit.
func descend(source string, path []string) (open, close, missing int, ok bool) {
	open, close, ok = findRootObject(source)
	if !ok {
		return 0, 0, 0, false
	}
	for depth := 0; depth < len(path)-1; depth++ {
		r, found := findKeyRange(source, open, close, path[depth])
		if !found {
			return open, close, depth, true
		}
		if r.valueStart >= len(source) || source[r.valueStart] != '{' {
			return 0, 0, 0, false
		}
		open = r.valueStart
		close = matchBracket(source, open)
		if close == -1 {
			return 0, 0, 0, false
		}
	}
	return open, close, -1, true
}

func indentFor(depth int) string { return strings.Repeat("  ", depth) }

// setObjectField sets one field of the config object to valueSource (already-rendered JSONC,
// from renderValue). path is the key chain from the root, e.g. ["theme","heat","hot"].
//
// Only the field's own value bytes are replaced, so a trailing same-line comment after the
// value survives and so does every other byte of the file. Missing intermediate blocks (and the
// leaf) are created, appended at the end of their parent. ok is false when the file has no
// top-level object or an intermediate key's value is not an object literal — nothing this
// editor can descend into safely.
func setObjectField(source string, path []string, valueSource string) (editResult, bool) {
	if len(path) == 0 {
		return editResult{}, false
	}
	open, close, missing, ok := descend(source, path)
	if !ok {
		return editResult{}, false
	}
	if missing >= 0 {
		// Create the whole absent chain in one append: `"a": { "b": { "leaf": value } }`.
		wrapped := jsonQuote(path[len(path)-1]) + ": " + valueSource
		for k := len(path) - 2; k >= missing; k-- {
			wrapped = jsonQuote(path[k]) + ": { " + wrapped + " }"
		}
		return editResult{text: appendToBody(source, open, close, wrapped+",", indentFor(missing+1)), changed: true}, true
	}

	leaf := path[len(path)-1]
	r, found := findKeyRange(source, open, close, leaf)
	if !found {
		line := jsonQuote(leaf) + ": " + valueSource + ","
		return editResult{text: appendToBody(source, open, close, line, indentFor(len(path))), changed: true}, true
	}
	if source[r.valueStart:r.valueEnd] == valueSource {
		return editResult{text: source, changed: false}, true
	}
	return editResult{text: source[:r.valueStart] + valueSource + source[r.valueEnd:], changed: true}, true
}

// insertIntoArray appends one rendered item to the array at path, creating the array — and any
// missing parent blocks — when absent. Dedup is the caller's job.
func insertIntoArray(source string, path []string, itemSource string) (editResult, bool) {
	if len(path) == 0 {
		return editResult{}, false
	}
	open, close, missing, ok := descend(source, path)
	if !ok {
		return editResult{}, false
	}
	if missing >= 0 {
		wrapped := jsonQuote(path[len(path)-1]) + ": [" + itemSource + "]"
		for k := len(path) - 2; k >= missing; k-- {
			wrapped = jsonQuote(path[k]) + ": { " + wrapped + " }"
		}
		return editResult{text: appendToBody(source, open, close, wrapped+",", indentFor(missing+1)), changed: true}, true
	}

	key := path[len(path)-1]
	r, found := findKeyRange(source, open, close, key)
	if !found {
		line := jsonQuote(key) + ": [" + itemSource + "],"
		return editResult{text: appendToBody(source, open, close, line, indentFor(len(path))), changed: true}, true
	}
	if source[r.valueStart] != '[' {
		return editResult{}, false
	}
	arrOpen := r.valueStart
	arrClose := matchBracket(source, arrOpen)
	if arrClose == -1 {
		return editResult{}, false
	}
	return editResult{
		text:    appendToBody(source, arrOpen, arrClose, itemSource+",", indentFor(len(path)+1)),
		changed: true,
	}, true
}

// findArray resolves path to an array's bracket span. present is false when the path simply is
// not in the file (which callers report as "nothing to remove", not as an error); ok is false
// when the shape is one no splice can edit.
func findArray(source string, path []string) (arrOpen, arrClose int, present, ok bool) {
	if len(path) == 0 {
		return 0, 0, false, false
	}
	open, close, missing, ok := descend(source, path)
	if !ok {
		return 0, 0, false, false
	}
	if missing >= 0 {
		return 0, 0, false, true
	}
	r, found := findKeyRange(source, open, close, path[len(path)-1])
	if !found {
		return 0, 0, false, true
	}
	if source[r.valueStart] != '[' {
		return 0, 0, false, false
	}
	arrOpen = r.valueStart
	arrClose = matchBracket(source, arrOpen)
	if arrClose == -1 {
		return 0, 0, false, false
	}
	return arrOpen, arrClose, true, true
}

// cutSpan removes one element from an array body, taking exactly its own bytes plus one
// separating comma. A span with a trailing comma takes it along; the last (comma-less) item
// takes the PRECEDING comma instead, so the element before it is not left with a dangling one.
func cutSpan(body string, span itemSpan) string {
	var out string
	if span.comma != -1 {
		out = body[:span.start] + body[span.comma+1:]
	} else {
		before := body[:span.start]
		cut := span.start
		if prev := strings.LastIndexByte(before, ','); prev != -1 {
			for _, s := range topLevelItemSpans(before) {
				if s.comma == prev {
					cut = prev
					break
				}
			}
		}
		out = body[:cut] + body[span.end:]
	}
	if strings.TrimSpace(out) == "" {
		return ""
	}
	return out
}

// removeResult reports how many elements a removal actually took out.
type removeResult struct {
	editResult
	removed int
}

// expectMatches applies the caller's `expect` safety net to one element's text.
//
// It is a safety net, not the selector: the element must be an object literal, and any expect
// field the text carries as a string literal must match. A field the element does not carry is
// not checked — the index already pins the element, and refusing on a field the file happens
// not to write would make the wizard unable to edit perfectly ordinary entries.
func expectMatches(elementText string, expect map[string]string) bool {
	stripped := stripComments(elementText)
	if !strings.HasPrefix(strings.TrimLeft(stripped, " \t\r\n"), "{") {
		return false
	}
	for k, v := range expect {
		if got, ok := readField(stripped, k); ok && got != v {
			return false
		}
	}
	return true
}

// removeArrayItemAt removes the array element at index.
//
// Removal is by POSITION, not by content match: content cannot tell two entries apart when
// one's fields are a subset of the other's — `[{ "repo": "x" }, { "repo": "x", "slug": "y" }]`
// — and a content match would delete both. The page knows exactly which row it rendered, so it
// removes by the index of that row; `expect` is checked against whatever actually sits there,
// so a page working from a stale list refuses instead of deleting the wrong entry.
func removeArrayItemAt(source string, path []string, index int, expect map[string]string) (removeResult, bool) {
	arrOpen, arrClose, present, ok := findArray(source, path)
	if !ok {
		return removeResult{}, false
	}
	if !present {
		return removeResult{editResult: editResult{text: source, changed: false}}, true
	}

	body := source[arrOpen+1 : arrClose]
	spans := topLevelItemSpans(body)
	if index < 0 || index >= len(spans) {
		return removeResult{}, false
	}
	span := spans[index]
	if !expectMatches(span.text, expect) {
		return removeResult{}, false
	}
	return removeResult{
		editResult: editResult{text: source[:arrOpen+1] + cutSpan(body, span) + source[arrClose:], changed: true},
		removed:    1,
	}, true
}

// setArrayItemField sets one field of the array element at index, e.g. organizations[1].name.
//
// The edit-in-place primitive: remove-and-re-add was the old workaround, and it loses every
// comment and every hand-written detail in the entry. Only the target field's bytes move — a
// field the element does not have yet is appended inside its braces, an existing one is
// replaced in place so a trailing comment after it survives. `expect` is the same safety net as
// in removeArrayItemAt.
func setArrayItemField(source string, path []string, index int, key, valueSource string, expect map[string]string) (editResult, bool) {
	arrOpen, arrClose, present, ok := findArray(source, path)
	if !ok || !present {
		return editResult{}, false
	}

	body := source[arrOpen+1 : arrClose]
	spans := topLevelItemSpans(body)
	if index < 0 || index >= len(spans) {
		return editResult{}, false
	}
	span := spans[index]
	if !expectMatches(span.text, expect) {
		return editResult{}, false
	}

	// Absolute offsets of this element's braces in the ORIGINAL source, so the edit splices
	// back without re-serialising anything around it.
	braceOpen := arrOpen + 1 + span.start + strings.IndexByte(span.text, '{')
	braceClose := matchBracket(source, braceOpen)
	if braceClose == -1 {
		return editResult{}, false
	}

	r, found := findKeyRange(source, braceOpen, braceClose, key)
	if !found {
		// A new field goes just inside the closing brace, keeping the spacing already used.
		inner := source[braceOpen+1 : braceClose]
		trimmed := strings.TrimRight(inner, " \t\r\n")
		insertion := " " + jsonQuote(key) + ": " + valueSource
		if trimmed != "" && !strings.HasSuffix(trimmed, ",") {
			insertion = "," + insertion
		}
		at := braceOpen + 1 + len(trimmed)
		return editResult{text: source[:at] + insertion + source[at:], changed: true}, true
	}
	if source[r.valueStart:r.valueEnd] == valueSource {
		return editResult{text: source, changed: false}, true
	}
	return editResult{text: source[:r.valueStart] + valueSource + source[r.valueEnd:], changed: true}, true
}
