package config

// Converting frznforge.config.ts is a one-way door: whatever this drops, the user writes again
// by hand. The file is roughly 60% comments and those comments are the configuration's
// documentation — the reason `//` and `/* */` are in the JSONC dialect at all (see jsonc.go).
// So this converter is built around keeping them: the source is walked token by token and every
// byte that is not JavaScript-specific is re-emitted VERBATIM, comments and blank lines and
// indentation included. Only four things actually change:
//
//   - the `import` / `export default defineConfig(` / `);` wrapper is dropped, leaving the bare
//     object. Its braces are already at the right indentation, so nothing is re-laid-out;
//   - identifier keys are quoted, and every string literal is decoded and re-quoted with JSON
//     escaping, so `'don\'t'` becomes `"don't"`;
//   - numeric expressions fold to literals (`512 * 1024` → `524288`), with a trailing comment
//     recording what was written where the result is not self-evident;
//   - anything else — a call, a variable, a template placeholder, a spread — is an ERROR naming
//     the line. A migration that guesses is worse than one that stops: a silently wrong
//     `maxBlobBytes` is a truncated site nobody notices for a month.
//
// The scanner is string- and comment-aware for the same reason StripJSONC is: `https://` inside
// a string is not a comment, `\'` does not end a string, and a `{` inside a comment is not an
// object.

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// TSFilename is the TypeScript config `frznforge config migrate` reads.
const TSFilename = "frznforge.config.ts"

// MigrateTS converts frznforge.config.ts source into frznforge.config.jsonc source.
//
// The result is checked with the real reader (StripJSONC + encoding/json) before it is
// returned, so a converter bug surfaces here rather than as an unreadable config file.
func MigrateTS(src []byte) ([]byte, error) {
	text := string(TrimBOM(src))
	toks, err := scanTS(text)
	if err != nil {
		return nil, err
	}
	// Every newline this converter adds of its own — between header comments, at the end — uses
	// the file's existing ending, so a config written on Windows does not come back with mixed
	// line endings and a whole-file diff.
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	m := &migrator{src: text, toks: toks, eol: eol}
	out, err := m.run()
	if err != nil {
		return nil, err
	}
	var probe any
	if err := json.Unmarshal(StripJSONC([]byte(out)), &probe); err != nil {
		return nil, fmt.Errorf("the converted config is not valid JSONC (%w) — that is a bug in the converter, not in your config; please report the file that triggered it", err)
	}
	return []byte(out), nil
}

// CountCommentLines reports how many lines of src carry any part of a comment. It runs the same
// scanner, so a `//` inside a URL is not counted — which is the whole point of measuring
// comment retention across a migration with it.
func CountCommentLines(src []byte) (int, error) {
	toks, err := scanTS(string(TrimBOM(src)))
	if err != nil {
		return 0, err
	}
	lines := map[int]bool{}
	for _, t := range toks {
		if t.kind == tokLineComment || t.kind == tokBlockComment {
			for l := t.line; l <= t.endLine; l++ {
				lines[l] = true
			}
		}
	}
	return len(lines), nil
}

/* ---- scanner ------------------------------------------------------------- */

type tokKind uint8

const (
	tokWS tokKind = iota
	tokLineComment
	tokBlockComment
	tokString
	tokNumber
	tokIdent
	tokPunct
)

type token struct {
	kind tokKind
	// text is exactly the source bytes, which is what lets comments and layout be re-emitted
	// without reconstructing them.
	text string
	// value is the decoded contents of a string literal (escapes resolved, quotes removed).
	value string
	num   float64
	// isInt tracks whether a number came from an integer literal, so folding can refuse a
	// result that no longer fits exactly in a float64.
	isInt   bool
	line    int
	endLine int
	pos     int
}

// scanTS splits TypeScript/JSONC source into tokens. It knows nothing about JavaScript beyond
// what a config file can contain: whitespace, comments, literals, identifiers and punctuation.
// A regex literal is not recognised — `/` only ever appears here as a comment opener or as
// division inside a folded expression — and anything unexpected becomes an error at the line.
func scanTS(src string) ([]token, error) {
	var toks []token
	line := 1
	for i := 0; i < len(src); {
		start, startLine := i, line
		c := src[i]
		switch {
		case isSpace(c):
			for i < len(src) && isSpace(src[i]) {
				if src[i] == '\n' {
					line++
				}
				i++
			}
			toks = append(toks, token{kind: tokWS, text: src[start:i], line: startLine, endLine: line, pos: start})

		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			// Leave a CRLF's \r to the following whitespace token, so a CRLF file keeps its
			// line endings instead of quietly becoming mixed.
			end := i
			if end > start && src[end-1] == '\r' {
				end--
			}
			i = end
			toks = append(toks, token{kind: tokLineComment, text: src[start:end], line: startLine, endLine: startLine, pos: start})

		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			closed := false
			for i < len(src) {
				if src[i] == '\n' {
					line++
				}
				if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
					i += 2
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("line %d: this /* block comment is never closed — add a */", startLine)
			}
			toks = append(toks, token{kind: tokBlockComment, text: src[start:i], line: startLine, endLine: line, pos: start})

		case c == '\'' || c == '"' || c == '`':
			next, value, err := scanString(src, i, startLine)
			if err != nil {
				return nil, err
			}
			line += strings.Count(src[i:next], "\n")
			toks = append(toks, token{kind: tokString, text: src[start:next], value: value, line: startLine, endLine: line, pos: start})
			i = next

		case isDigit(c) || (c == '.' && i+1 < len(src) && isDigit(src[i+1])):
			next, num, isInt, err := scanNumber(src, i, startLine)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tokNumber, text: src[start:next], num: num, isInt: isInt, line: startLine, endLine: startLine, pos: start})
			i = next

		case isIdentStart(c):
			for i < len(src) && isIdentPart(src[i]) {
				i++
			}
			toks = append(toks, token{kind: tokIdent, text: src[start:i], line: startLine, endLine: startLine, pos: start})

		default:
			i++
			toks = append(toks, token{kind: tokPunct, text: src[start:i], line: startLine, endLine: startLine, pos: start})
		}
	}
	return toks, nil
}

// scanString consumes a quoted literal starting at i and returns the index after it plus the
// decoded value. Template literals are accepted only when they hold no `${}` placeholder: a
// placeholder needs the JavaScript runtime the JSONC config no longer has.
func scanString(src string, i, line int) (int, string, error) {
	quote := src[i]
	var b strings.Builder
	for j := i + 1; j < len(src); {
		c := src[j]
		switch {
		case c == quote:
			return j + 1, b.String(), nil

		case c == '\n' && quote != '`':
			return 0, "", fmt.Errorf("line %d: this string is missing its closing %c", line, quote)

		case c == '$' && quote == '`' && j+1 < len(src) && src[j+1] == '{':
			return 0, "", fmt.Errorf("line %d: `${…}` needs JavaScript to evaluate — replace the template literal with the finished string", line)

		case c == '\\':
			if j+1 >= len(src) {
				return 0, "", fmt.Errorf("line %d: the file ends inside a string", line)
			}
			next, r, keep, err := decodeEscape(src, j, line)
			if err != nil {
				return 0, "", err
			}
			if keep {
				b.WriteRune(r)
			}
			j = next

		default:
			b.WriteByte(c)
			j++
		}
	}
	return 0, "", fmt.Errorf("line %d: this string is missing its closing %c", line, quote)
}

// decodeEscape resolves one backslash escape at i. keep is false for a line continuation, which
// produces no character at all.
func decodeEscape(src string, i, line int) (next int, r rune, keep bool, err error) {
	c := src[i+1]
	switch c {
	case 'n':
		return i + 2, '\n', true, nil
	case 't':
		return i + 2, '\t', true, nil
	case 'r':
		return i + 2, '\r', true, nil
	case 'b':
		return i + 2, '\b', true, nil
	case 'f':
		return i + 2, '\f', true, nil
	case 'v':
		return i + 2, '\v', true, nil
	case '0':
		// Only the bare NUL escape; \01 is a legacy octal escape and not worth guessing at.
		if i+2 < len(src) && isDigit(src[i+2]) {
			return 0, 0, false, fmt.Errorf("line %d: octal escapes are not supported — write the character or a \\uXXXX escape", line)
		}
		return i + 2, 0, true, nil
	case '\n':
		return i + 2, 0, false, nil
	case '\r':
		if i+2 < len(src) && src[i+2] == '\n' {
			return i + 3, 0, false, nil
		}
		return i + 2, 0, false, nil
	case 'x':
		if i+4 > len(src) {
			return 0, 0, false, fmt.Errorf("line %d: truncated \\x escape", line)
		}
		n, convErr := strconv.ParseUint(src[i+2:i+4], 16, 32)
		if convErr != nil {
			return 0, 0, false, fmt.Errorf("line %d: %q is not a valid \\xHH escape", line, src[i:i+4])
		}
		return i + 4, rune(n), true, nil
	case 'u':
		return decodeUnicodeEscape(src, i, line)
	default:
		// \' \" \` \\ \/ and anything else stand for the character itself, exactly as
		// JavaScript reads them.
		r, size := utf8.DecodeRuneInString(src[i+1:])
		return i + 1 + size, r, true, nil
	}
}

// decodeUnicodeEscape handles both \uXXXX and \u{XXXXX}, joining a surrogate pair into the one
// rune it stands for — otherwise an emoji in a description would come out as two broken halves.
func decodeUnicodeEscape(src string, i, line int) (int, rune, bool, error) {
	if i+2 < len(src) && src[i+2] == '{' {
		end := strings.IndexByte(src[i+3:], '}')
		if end < 0 {
			return 0, 0, false, fmt.Errorf("line %d: unterminated \\u{…} escape", line)
		}
		n, err := strconv.ParseUint(src[i+3:i+3+end], 16, 32)
		if err != nil || n > utf8.MaxRune {
			return 0, 0, false, fmt.Errorf("line %d: %q is not a valid code point", line, src[i:i+4+end])
		}
		return i + 4 + end, rune(n), true, nil
	}
	if i+6 > len(src) {
		return 0, 0, false, fmt.Errorf("line %d: truncated \\u escape", line)
	}
	n, err := strconv.ParseUint(src[i+2:i+6], 16, 32)
	if err != nil {
		return 0, 0, false, fmt.Errorf("line %d: %q is not a valid \\uXXXX escape", line, src[i:i+6])
	}
	r := rune(n)
	if r >= 0xD800 && r <= 0xDBFF && i+12 <= len(src) && src[i+6] == '\\' && src[i+7] == 'u' {
		if low, err := strconv.ParseUint(src[i+8:i+12], 16, 32); err == nil && low >= 0xDC00 && low <= 0xDFFF {
			return i + 12, 0x10000 + (r-0xD800)<<10 + (rune(low) - 0xDC00), true, nil
		}
	}
	return i + 6, r, true, nil
}

// scanNumber consumes a numeric literal, including hex/binary/octal forms and `_` separators,
// which a hand-written config is allowed to use even though JSON is not.
func scanNumber(src string, i, line int) (int, float64, bool, error) {
	j := i
	if src[j] == '0' && j+1 < len(src) {
		base := 0
		switch src[j+1] {
		case 'x', 'X':
			base = 16
		case 'b', 'B':
			base = 2
		case 'o', 'O':
			base = 8
		}
		if base != 0 {
			j += 2
			start := j
			for j < len(src) && (isHexDigit(src[j]) || src[j] == '_') {
				j++
			}
			digits := strings.ReplaceAll(src[start:j], "_", "")
			n, err := strconv.ParseInt(digits, base, 64)
			if err != nil {
				return 0, 0, false, fmt.Errorf("line %d: %q is not a valid number", line, src[i:j])
			}
			return j, float64(n), true, nil
		}
	}
	isInt := true
	for j < len(src) && (isDigit(src[j]) || src[j] == '_') {
		j++
	}
	if j < len(src) && src[j] == '.' {
		isInt = false
		j++
		for j < len(src) && (isDigit(src[j]) || src[j] == '_') {
			j++
		}
	}
	if j < len(src) && (src[j] == 'e' || src[j] == 'E') {
		isInt = false
		j++
		if j < len(src) && (src[j] == '+' || src[j] == '-') {
			j++
		}
		for j < len(src) && isDigit(src[j]) {
			j++
		}
	}
	text := strings.TrimSuffix(strings.ReplaceAll(src[i:j], "_", ""), ".")
	if j < len(src) && src[j] == 'n' { // BigInt literal
		j++
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, 0, false, fmt.Errorf("line %d: %q is not a number frznforge can convert — write it as a plain decimal", line, src[i:j])
	}
	return j, v, isInt, nil
}

func isDigit(c byte) bool    { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool { return isDigit(c) || (c|0x20) >= 'a' && (c|0x20) <= 'f' }
func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c|0x20) >= 'a' && (c|0x20) <= 'z' || c >= utf8.RuneSelf
}
func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) }

/* ---- conversion ---------------------------------------------------------- */

// The state of the container being converted. JSON's grammar is small enough that tracking
// "what may come next" by hand is shorter, and gives far better errors, than a parse tree.
const (
	stKey = iota
	stColon
	stValue
	stComma
)

type frame struct {
	array bool
	state int
	// line is where the container opened, so an unclosed one can point at its own brace.
	line int
}

type migrator struct {
	src  string
	toks []token
	i    int
	// eol is the line ending the source file uses, for the newlines this converter adds itself.
	eol string
	out strings.Builder
	// wrapped records that a defineConfig( call was opened and still needs its ).
	wrapped bool
	stack   []frame
	// note is a folded expression waiting to be recorded as a comment at the end of the
	// current output line; noteInComment says a line comment already claimed that spot.
	note          string
	noteInComment bool
	// holdNote suppresses the flush while a multi-line block comment is being emitted. Without
	// it the note lands between the comment's first and second lines — inside the author's
	// prose, where it is both invisible and an edit to a comment this converter promised to
	// copy verbatim.
	holdNote bool
}

func (m *migrator) run() (string, error) {
	prologue, err := m.consumeHead()
	if err != nil {
		return "", err
	}
	for _, c := range prologue {
		m.out.WriteString(c)
		m.out.WriteString(m.eol)
	}
	if len(prologue) > 0 {
		m.out.WriteString(m.eol)
	}
	if err := m.convert(); err != nil {
		return "", err
	}
	if err := m.consumeTail(); err != nil {
		return "", err
	}
	m.flushNote()
	return strings.TrimRight(m.out.String(), " \t\r\n") + m.eol, nil
}

// consumeHead skips the import/export wrapper and returns the comments that stood in front of
// the object. They are the file's header — the "what this file is" paragraph — so they are kept
// and re-emitted above the object rather than dropped with the code they happened to precede.
func (m *migrator) consumeHead() ([]string, error) {
	var comments []string
	for m.i < len(m.toks) {
		t := m.toks[m.i]
		switch t.kind {
		case tokWS:
			// A blank line between two header comments separated two thoughts; keep one.
			if strings.Count(t.text, "\n") > 1 && len(comments) > 0 && comments[len(comments)-1] != "" {
				comments = append(comments, "")
			}
			m.i++
		case tokLineComment, tokBlockComment:
			comments = append(comments, t.text)
			m.i++
		case tokIdent:
			switch t.text {
			case "import":
				if err := m.skipStatement(); err != nil {
					return nil, err
				}
			case "export", "default":
				m.i++
			default:
				// The identity wrapper: defineConfig( … ). Any name is accepted — what matters
				// is that an object literal follows it.
				next, ok := m.peekSignificant()
				if !ok || m.toks[next].kind != tokPunct || m.toks[next].text != "(" {
					return nil, errAtf(t, "cannot convert `%s` — a config file is `export default defineConfig({ … })` and nothing else; move any other code out of it", t.text)
				}
				m.i = next + 1
				m.wrapped = true
			}
		case tokPunct:
			if t.text == "{" {
				for len(comments) > 0 && comments[len(comments)-1] == "" {
					comments = comments[:len(comments)-1]
				}
				return comments, nil
			}
			return nil, errAtf(t, "expected the config object to start here, found %q", t.text)
		default:
			return nil, errAtf(t, "expected the config object, found %q", t.text)
		}
	}
	return nil, fmt.Errorf("no config object found — %s should hold `export default defineConfig({ … })`", TSFilename)
}

// peekSignificant returns the index of the next token that is not whitespace or a comment.
func (m *migrator) peekSignificant() (int, bool) {
	for j := m.i + 1; j < len(m.toks); j++ {
		switch m.toks[j].kind {
		case tokWS, tokLineComment, tokBlockComment:
		default:
			return j, true
		}
	}
	return 0, false
}

// skipStatement drops an `import …` line. It is the only statement a config file may carry
// besides the export, and it has no meaning once the config is data.
//
// The semicolon is optional: `import { defineConfig } from './x'` with no `;` is valid
// TypeScript and is what a Prettier config with `semi: false` produces, so a newline after the
// module specifier ends the statement the way JavaScript's automatic semicolon insertion does.
// Refusing it would block the migration on a file that is perfectly convertible.
func (m *migrator) skipStatement() error {
	line := m.toks[m.i].line
	var lastSig token
	for m.i < len(m.toks) {
		t := m.toks[m.i]
		if t.kind == tokPunct && t.text == ";" {
			m.i++
			return nil
		}
		// An import ends with its module specifier, so a line break after that string is the
		// end of the statement.
		if t.kind == tokWS && strings.Contains(t.text, "\n") && lastSig.kind == tokString {
			return nil
		}
		if t.kind != tokWS && t.kind != tokLineComment && t.kind != tokBlockComment {
			lastSig = t
		}
		m.i++
	}
	if lastSig.kind == tokString {
		return nil
	}
	return fmt.Errorf("line %d: this import statement is never terminated with a ;", line)
}

// consumeTail accepts the `);` that closes the wrapper, keeping any comment that trails it.
func (m *migrator) consumeTail() error {
	closed := !m.wrapped
	for m.i < len(m.toks) {
		t := m.toks[m.i]
		switch t.kind {
		case tokWS:
			m.i++
		case tokLineComment, tokBlockComment:
			m.emit(m.eol)
			m.emit(t.text)
			m.i++
		case tokPunct:
			switch t.text {
			case ")":
				if closed {
					return errAtf(t, "unexpected ) after the config object")
				}
				closed = true
			case ";":
			default:
				return errAtf(t, "unexpected %q after the config object — a config file holds one object and nothing else", t.text)
			}
			m.i++
		default:
			return errAtf(t, "unexpected `%s` after the config object — a config file holds one object and nothing else", t.text)
		}
	}
	if !closed {
		return fmt.Errorf("the defineConfig( call is never closed — add a )")
	}
	return nil
}

// convert walks the object literal, rewriting only what JSON cannot express and copying
// everything else — comments, blank lines, indentation — byte for byte.
func (m *migrator) convert() error {
	open := m.toks[m.i]
	m.emit("{")
	m.i++
	m.stack = []frame{{state: stKey, line: open.line}}

	for len(m.stack) > 0 {
		if m.i >= len(m.toks) {
			f := m.stack[len(m.stack)-1]
			return fmt.Errorf("line %d: this %s is never closed", f.line, containerName(f.array))
		}
		t := m.toks[m.i]
		switch t.kind {
		case tokWS:
			m.emit(t.text)
			m.i++
			continue
		case tokLineComment:
			m.emitComment(t)
			m.i++
			continue
		case tokBlockComment:
			m.emitComment(t)
			m.i++
			continue
		}
		var err error
		if m.top().array {
			err = m.stepArray(t)
		} else {
			err = m.stepObject(t)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *migrator) stepObject(t token) error {
	switch m.top().state {
	case stKey:
		switch {
		case t.kind == tokPunct && t.text == "}":
			m.emit("}")
			m.i++
			m.pop()
		case t.kind == tokIdent:
			m.emit(jsonQuote(t.text))
			m.setState(stColon)
			m.i++
		case t.kind == tokString:
			m.emit(jsonQuote(t.value))
			m.setState(stColon)
			m.i++
		case t.kind == tokNumber:
			// JSON has no numeric keys; quoting one is what JavaScript means by it anyway.
			m.emit(jsonQuote(t.text))
			m.setState(stColon)
			m.i++
		case t.kind == tokPunct && t.text == ".":
			return errAtf(t, "`...` spread needs JavaScript to evaluate — write the settings it would have merged in")
		case t.kind == tokPunct && t.text == "[":
			return errAtf(t, "a computed key needs JavaScript to evaluate — write the setting name out")
		default:
			return errAtf(t, "expected a setting name, found %q", t.text)
		}
	case stColon:
		if t.kind == tokPunct && t.text == ":" {
			m.emit(":")
			m.setState(stValue)
			m.i++
			return nil
		}
		// `{ title }` shorthand and `key() {}` methods both land here.
		return errAtf(t, "expected `:` after the setting name, found %q — every setting needs an explicit value", t.text)
	case stValue:
		return m.value()
	case stComma:
		switch {
		case t.kind == tokPunct && t.text == ",":
			m.emit(",")
			m.setState(stKey)
			m.i++
		case t.kind == tokPunct && t.text == "}":
			m.emit("}")
			m.i++
			m.pop()
		default:
			return errAtf(t, "expected `,` or `}` after a value, found %q", t.text)
		}
	}
	return nil
}

func (m *migrator) stepArray(t token) error {
	switch m.top().state {
	case stValue:
		if t.kind == tokPunct && t.text == "]" {
			m.emit("]")
			m.i++
			m.pop()
			return nil
		}
		return m.value()
	case stComma:
		switch {
		case t.kind == tokPunct && t.text == ",":
			m.emit(",")
			m.setState(stValue)
			m.i++
		case t.kind == tokPunct && t.text == "]":
			m.emit("]")
			m.i++
			m.pop()
		default:
			return errAtf(t, "expected `,` or `]` in this list, found %q", t.text)
		}
	}
	return nil
}

func (m *migrator) value() error {
	t := m.toks[m.i]
	switch {
	case t.kind == tokPunct && t.text == "{":
		m.emit("{")
		m.i++
		m.push(frame{state: stKey, line: t.line})
	case t.kind == tokPunct && t.text == "[":
		m.emit("[")
		m.i++
		m.push(frame{array: true, state: stValue, line: t.line})
	case t.kind == tokString:
		m.emit(jsonQuote(t.value))
		m.i++
		m.setState(stComma)
	case t.kind == tokIdent:
		switch t.text {
		case "true", "false", "null":
			m.emit(t.text)
			m.i++
			m.setState(stComma)
		case "undefined":
			return errAtf(t, "`undefined` has no JSONC equivalent — delete the setting to fall back to its default")
		default:
			return errAtf(t, "cannot convert `%s`: a JSONC config holds literals only, so this value has to be written out", t.text)
		}
	case t.kind == tokNumber, t.kind == tokPunct && (t.text == "(" || t.text == "-" || t.text == "+"):
		return m.foldNumber()
	case t.kind == tokPunct && t.text == ".":
		return errAtf(t, "`...` spread needs JavaScript to evaluate — write the values it would have merged in")
	default:
		return errAtf(t, "expected a value, found %q", t.text)
	}
	return nil
}

// foldNumber collapses a numeric expression to the literal it evaluates to.
//
// `512 * 1024` reads better than `524288` in a hand-written file, which is exactly why the
// original expression is kept as a trailing comment: the reader loses nothing, and the file
// gains a value JSON can hold.
func (m *migrator) foldNumber() error {
	first := m.toks[m.i]
	var expr, inner, pending []token
	depth := 0
	// last is where the expression really ended. Everything after it — the whitespace before
	// the comma, a comment on the next line — is rewound and re-emitted by the caller, so
	// folding never eats the layout around it.
	last := m.i

gather:
	for m.i < len(m.toks) {
		t := m.toks[m.i]
		switch t.kind {
		case tokWS:
			m.i++
			continue
		case tokLineComment, tokBlockComment:
			pending = append(pending, t)
			m.i++
			continue
		case tokNumber:
		case tokPunct:
			switch t.text {
			case "(":
				depth++
			case ")":
				if depth == 0 {
					break gather
				}
				depth--
			case ",", "}", "]":
				if depth == 0 {
					break gather
				}
				return errAtf(t, "unbalanced parentheses in this expression")
			case "+", "-", "*", "/", "%":
			default:
				return errAtf(t, "cannot convert %q inside a numeric expression — write the number out", t.text)
			}
		default:
			return errAtf(t, "cannot convert `%s` inside a numeric expression — only numbers fold, so write the value out", t.text)
		}
		expr = append(expr, t)
		// A comment seen before this token was inside the expression, not after it.
		inner = append(inner, pending...)
		pending = nil
		last = m.i
		m.i++
	}
	m.i = last + 1

	if len(expr) == 0 {
		return errAtf(first, "expected a number")
	}
	p := &exprParser{toks: expr}
	v, isInt, err := p.expr()
	if err != nil {
		return err
	}
	if p.i != len(expr) {
		return errAtf(expr[p.i], "cannot convert %q here — the expression already ended", expr[p.i].text)
	}
	out, err := formatNumber(first, v, isInt)
	if err != nil {
		return err
	}
	m.emit(out)

	end := expr[len(expr)-1]
	raw := strings.Join(strings.Fields(m.src[first.pos:end.pos+len(end.text)]), " ")
	if raw != out {
		// Two folds can share a line (`a: 512 * 1024, b: 2 * 3`). Both notes belong to that
		// line's single trailing comment, so the second joins the first instead of replacing
		// it — overwriting would drop the first expression with no trace.
		if m.note != "" {
			m.note += "; " + raw
		} else {
			m.note = raw
		}
	}
	// A comment written *inside* the expression has no place left to sit once the expression is
	// one literal, so it follows the value rather than being dropped.
	for _, c := range inner {
		m.emit(" ")
		m.emitComment(c)
	}
	m.setState(stComma)
	return nil
}

// formatNumber renders a folded value, refusing anything a float64 no longer holds exactly —
// a byte cap that is off by one is worse than a migration that stops and says so.
func formatNumber(at token, v float64, isInt bool) (string, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", errAtf(at, "this expression does not evaluate to a number")
	}
	if isInt && math.Abs(v) > 1<<53 {
		return "", errAtf(at, "this number is too large to convert exactly — write the value you want directly")
	}
	return strconv.FormatFloat(v, 'f', -1, 64), nil
}

/* ---- numeric expressions -------------------------------------------------- */

// exprParser evaluates the subset of arithmetic a config file plausibly contains: literals,
// + - * / %, unary sign and parentheses. isInt rides along so an integer result can be told
// from a rounded one.
type exprParser struct {
	toks []token
	i    int
}

func (p *exprParser) peek() (token, bool) {
	if p.i >= len(p.toks) {
		return token{}, false
	}
	return p.toks[p.i], true
}

func (p *exprParser) expr() (float64, bool, error) {
	v, isInt, err := p.term()
	if err != nil {
		return 0, false, err
	}
	for {
		t, ok := p.peek()
		if !ok || t.kind != tokPunct || (t.text != "+" && t.text != "-") {
			return v, isInt, nil
		}
		p.i++
		rv, rInt, err := p.term()
		if err != nil {
			return 0, false, err
		}
		if t.text == "+" {
			v += rv
		} else {
			v -= rv
		}
		isInt = isInt && rInt
	}
}

func (p *exprParser) term() (float64, bool, error) {
	v, isInt, err := p.unary()
	if err != nil {
		return 0, false, err
	}
	for {
		t, ok := p.peek()
		if !ok || t.kind != tokPunct || (t.text != "*" && t.text != "/" && t.text != "%") {
			return v, isInt, nil
		}
		p.i++
		rv, rInt, err := p.unary()
		if err != nil {
			return 0, false, err
		}
		switch t.text {
		case "*":
			v *= rv
		case "/":
			if rv == 0 {
				return 0, false, errAtf(t, "division by zero")
			}
			v /= rv
			rInt = rInt && v == math.Trunc(v)
		case "%":
			if rv == 0 {
				return 0, false, errAtf(t, "division by zero")
			}
			v = math.Mod(v, rv)
		}
		isInt = isInt && rInt
	}
}

func (p *exprParser) unary() (float64, bool, error) {
	t, ok := p.peek()
	if !ok {
		return 0, false, fmt.Errorf("a numeric expression ends early")
	}
	switch {
	case t.kind == tokPunct && (t.text == "-" || t.text == "+"):
		p.i++
		v, isInt, err := p.unary()
		if t.text == "-" {
			v = -v
		}
		return v, isInt, err
	case t.kind == tokPunct && t.text == "(":
		p.i++
		v, isInt, err := p.expr()
		if err != nil {
			return 0, false, err
		}
		next, ok := p.peek()
		if !ok || next.kind != tokPunct || next.text != ")" {
			return 0, false, errAtf(t, "this ( is never closed")
		}
		p.i++
		return v, isInt, nil
	case t.kind == tokNumber:
		p.i++
		return t.num, t.isInt, nil
	}
	return 0, false, errAtf(t, "expected a number, found %q", t.text)
}

/* ---- emitting ------------------------------------------------------------- */

func (m *migrator) top() *frame    { return &m.stack[len(m.stack)-1] }
func (m *migrator) setState(s int) { m.stack[len(m.stack)-1].state = s }
func (m *migrator) push(f frame)   { m.stack = append(m.stack, f) }

// pop closes a container; the value it was is now a completed value in its parent.
func (m *migrator) pop() {
	m.stack = m.stack[:len(m.stack)-1]
	if len(m.stack) > 0 {
		m.setState(stComma)
	}
}

// emit writes output, flushing a pending fold comment at the end of whatever line it lands on
// so the note reads as a trailing comment rather than splitting the value from its comma.
func (m *migrator) emit(s string) {
	for len(s) > 0 {
		nl := strings.IndexByte(s, '\n')
		if nl < 0 {
			m.out.WriteString(s)
			return
		}
		cut := nl
		if cut > 0 && s[cut-1] == '\r' {
			cut--
		}
		m.out.WriteString(s[:cut])
		m.flushNote()
		m.out.WriteString(s[cut : nl+1])
		s = s[nl+1:]
	}
}

// emitComment writes a comment token, holding back any pending fold note for the duration so
// the note cannot be written into the middle of a multi-line block comment.
func (m *migrator) emitComment(t token) {
	if t.kind == tokLineComment && m.note != "" {
		m.noteInComment = true
	}
	held := m.holdNote
	m.holdNote = t.kind == tokBlockComment
	m.emit(t.text)
	m.holdNote = held
}

func (m *migrator) flushNote() {
	if m.note == "" || m.holdNote {
		return
	}
	if m.noteInComment {
		m.out.WriteString(" (" + m.note + ")")
	} else {
		m.out.WriteString(" // " + m.note)
	}
	m.note, m.noteInComment = "", false
}

// jsonQuote renders a Go string as a JSON string. encoding/json is not used because it escapes
// <, > and & — a config full of URLs and prose would come out unreadable, and the file is meant
// to be edited by hand.
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

func containerName(array bool) string {
	if array {
		return "list"
	}
	return "block"
}

// errAtf is the only error shape this file produces: the line, then what to do about it.
func errAtf(t token, format string, args ...any) error {
	return fmt.Errorf("line %d: %s", t.line, fmt.Sprintf(format, args...))
}
