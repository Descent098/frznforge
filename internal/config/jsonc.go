package config

import "bytes"

// StripJSONC turns JSON-with-comments into plain JSON that encoding/json accepts.
//
// The config file is the one place a frznforge site is *authored*, and the file it replaces
// (frznforge.config.ts) was roughly 60% comments — those comments ARE the configuration
// documentation. So the format has to keep them, and reading it has to be something small
// enough to trust: this is the whole reader.
//
// The dialect, decided once and documented in docs/user/configuration.md:
//
//   - `//` to end of line, and `/* … */` spanning lines.
//   - A trailing comma before `}` or `]` is allowed. Hand-edited lists grow and shrink, and
//     JSON's refusal here is the single most common way a hand-written config fails to parse.
//   - Nothing else. No unquoted keys, no single quotes, no NaN. It is JSON plus the two things
//     a person editing a file by hand actually needs.
//
// Two properties matter more than they look:
//
//   - It is string-aware. A `//` inside a string — every `https://` in the file — must survive,
//     and an escaped quote (`\"`) must not end the string early. Getting this wrong corrupts
//     configs in a way that only shows up on the one line that happens to contain a URL.
//   - Removed bytes are replaced with spaces, and newlines inside block comments are kept, so
//     byte offsets and line numbers in a decoder error still point at the right place in the
//     file the user wrote.
func StripJSONC(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)

	const (
		code = iota
		inString
		inLine
		inBlock
	)
	state := code
	escaped := false

	blank := func(i int) {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}

	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case code:
			switch {
			case c == '"':
				state = inString
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				state = inLine
				blank(i)
				blank(i + 1)
				i++
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state = inBlock
				blank(i)
				blank(i + 1)
				i++
			}
		case inString:
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				state = code
			}
		case inLine:
			if c == '\n' {
				state = code
			} else {
				blank(i)
			}
		case inBlock:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				blank(i)
				blank(i + 1)
				i++
				state = code
			} else {
				blank(i)
			}
		}
	}

	return stripTrailingCommas(out)
}

// stripTrailingCommas blanks a comma that is followed only by whitespace and then `}` or `]`.
// Runs after comment removal so `[1, /* two */]` is handled, and is string-aware for the same
// reason StripJSONC is: a comma inside a string is data.
func stripTrailingCommas(src []byte) []byte {
	inString, escaped := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c != ',' {
			continue
		}
		j := i + 1
		for j < len(src) && isSpace(src[j]) {
			j++
		}
		if j < len(src) && (src[j] == '}' || src[j] == ']') {
			src[i] = ' '
		}
	}
	return src
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// HasBOM reports whether src starts with a UTF-8 byte order mark, which an editor on Windows
// will happily add and encoding/json will refuse to parse.
func HasBOM(src []byte) bool { return bytes.HasPrefix(src, []byte{0xEF, 0xBB, 0xBF}) }

// TrimBOM removes a leading UTF-8 byte order mark if present.
func TrimBOM(src []byte) []byte {
	if HasBOM(src) {
		return src[3:]
	}
	return src
}
