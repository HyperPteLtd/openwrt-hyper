package model

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Readable writes the characters go-yaml escapes in double-quoted scalars, those outside the
// Basic Multilingual Plane such as emoji ("\U0001F680"), as themselves, which YAML allows. It
// finds the double-quoted scalars by parsing the YAML, so that text like an escape in another
// scalar (a plain password "x\U0001F600y", a line of a block scalar) stays as written, and it
// leaves everything else as the encoder wrote it.
func Readable(text []byte) []byte {
	if !bytes.Contains(text, []byte(`\U`)) {
		return text
	}
	scalars, err := astral(text)
	if err != nil || len(scalars) == 0 {
		return text
	}
	lines := lineStarts(text)
	out := make([]byte, 0, len(text))
	last := 0
	for _, scalar := range scalars {
		at := offset(text, lines, scalar.Line, scalar.Column)
		if at < last || at >= len(text) || text[at] != '"' {
			continue
		}
		out = append(out, text[last:at]...)
		end, readable := unescape(out, text, at)
		// The characters written as themselves must be the parser's; otherwise the scalar
		// stays as the encoder wrote it.
		if end < 0 || !sameAstral(readable[len(out):], scalar.Value) {
			out = out[:len(out)-(at-last)]
			continue
		}
		out, last = readable, end
	}
	return append(out, text[last:]...)
}

// astral returns the double-quoted scalars that hold characters outside the Basic Multilingual
// Plane, in the order they are written.
func astral(text []byte) ([]*yaml.Node, error) {
	var out []*yaml.Node
	var walk func(node *yaml.Node)
	walk = func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode && node.Style&yaml.DoubleQuotedStyle != 0 {
			for _, r := range node.Value {
				if r > 0xFFFF {
					out = append(out, node)
					break
				}
			}
		}
		for _, child := range node.Content {
			walk(child)
		}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(text))
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); errors.Is(err, io.EOF) {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		walk(&document)
	}
}

// sameAstral reports whether the scalar as written holds, in order, the graphic characters
// outside the Basic Multilingual Plane of its value: those unescape writes as themselves.
func sameAstral(written []byte, value string) bool {
	for {
		var want, got rune = -1, -1
		for index, r := range value {
			if r > 0xFFFF && unicode.IsGraphic(r) {
				want, value = r, value[index+utf8.RuneLen(r):]
				break
			}
		}
		for len(written) > 0 {
			r, size := utf8.DecodeRune(written)
			written = written[size:]
			if r > 0xFFFF && unicode.IsGraphic(r) {
				got = r
				break
			}
		}
		if want != got {
			return false
		}
		if want < 0 {
			return true
		}
	}
}

// lineStarts returns where each line starts, with go-yaml's line breaks: CRLF, CR, LF, NEL,
// LS and PS.
func lineStarts(text []byte) []int {
	starts := []int{0}
	for index := 0; index < len(text); {
		r, size := utf8.DecodeRune(text[index:])
		switch {
		case r == '\r' && index+1 < len(text) && text[index+1] == '\n':
			size = 2
			fallthrough
		case r == '\r', r == '\n', r == '\u0085', r == ' ', r == ' ':
			starts = append(starts, index+size)
		}
		index += size
	}
	return starts
}

// offset turns go-yaml's line and column, which count characters from 1, into a byte offset.
func offset(text []byte, lines []int, line, column int) int {
	if line < 1 || line > len(lines) {
		return -1
	}
	at := lines[line-1]
	for ; column > 1 && at < len(text); column-- {
		_, size := utf8.DecodeRune(text[at:])
		at += size
	}
	return at
}

// unescape appends the double-quoted scalar at text[at] to out, with its \U escapes of graphic
// characters written as the characters, and returns where the scalar ends, or -1.
func unescape(out, text []byte, at int) (int, []byte) {
	out = append(out, '"')
	for index := at + 1; index < len(text); index++ {
		switch c := text[index]; c {
		case '"':
			return index + 1, append(out, c)
		case '\\':
			if index+1 >= len(text) {
				return -1, out
			}
			if text[index+1] == 'U' && index+10 <= len(text) {
				if r, err := strconv.ParseUint(string(text[index+2:index+10]), 16, 32); err == nil &&
					utf8.ValidRune(rune(r)) && unicode.IsGraphic(rune(r)) {
					out = utf8.AppendRune(out, rune(r))
					index += 9
					continue
				}
			}
			// Another escape: copy it whole, so that an escaped quote or backslash stays one.
			out = append(out, c, text[index+1])
			index++
		default:
			out = append(out, c)
		}
	}
	return -1, out
}
