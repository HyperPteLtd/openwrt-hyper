// Package body reads a subscription's bytes as text, whatever the server wrapped them in:
// byte-order marks, UTF-16, Base64 in its variants, JSON with comments.
package body

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Clean returns the body as UTF-8 text without a byte-order mark.
func Clean(data []byte) []byte {
	switch {
	case bytes.HasPrefix(data, []byte("\xef\xbb\xbf")):
		return data[3:]
	case bytes.HasPrefix(data, []byte("\xff\xfe")):
		return utf16Text(data[2:], false)
	case bytes.HasPrefix(data, []byte("\xfe\xff")):
		return utf16Text(data[2:], true)
	}
	return data
}

func utf16Text(data []byte, bigEndian bool) []byte {
	units := make([]uint16, len(data)/2)
	for index := range units {
		if bigEndian {
			units[index] = uint16(data[2*index])<<8 | uint16(data[2*index+1])
		} else {
			units[index] = uint16(data[2*index+1])<<8 | uint16(data[2*index])
		}
	}
	return []byte(string(utf16.Decode(units)))
}

var errNotBase64 = errors.New("not Base64")

// Base64 decodes Base64 in either alphabet, with or without padding, wrapped in lines, or as
// padded chunks one after another.
func Base64(text string) ([]byte, error) {
	text = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		case '-':
			return '+'
		case '_':
			return '/'
		}
		return r
	}, text)
	if decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(text, "=")); err == nil {
		return decoded, nil
	}
	// Chunks encoded one by one, each with its padding.
	if trimmed := strings.TrimRight(text, "="); strings.Contains(trimmed, "=") {
		var out []byte
		for chunk := range strings.SplitAfterSeq(text, "=") {
			chunk = strings.TrimLeft(chunk, "=")
			if chunk = strings.TrimRight(chunk, "="); chunk == "" {
				continue
			}
			decoded, err := base64.RawStdEncoding.DecodeString(chunk)
			if err != nil {
				return nil, errNotBase64
			}
			out = append(out, decoded...)
		}
		return out, nil
	}
	return nil, errNotBase64
}

// Base64Text decodes a whole body that is Base64 of text, as most providers serve link lists.
func Base64Text(data []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 8 {
		return nil, false
	}
	for _, b := range trimmed {
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		case b == '+', b == '/', b == '=', b == '-', b == '_', b == '\r', b == '\n', b == ' ', b == '\t':
		default:
			return nil, false
		}
	}
	decoded, err := Base64(string(trimmed))
	if err != nil || !Text(decoded) {
		return nil, false
	}
	return Clean(decoded), true
}

// Text reports bytes that read as text: valid UTF-8 without control characters but line breaks
// and tabs.
func Text(data []byte) bool {
	if !utf8.Valid(data) || len(bytes.TrimSpace(data)) == 0 {
		return false
	}
	for _, r := range string(data) {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

// JSONC removes the comments and trailing commas sing-box and many editors allow in JSON.
func JSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString, escaped := false, false
	for index := 0; index < len(data); index++ {
		c := data[index]
		if inString {
			out = append(out, c)
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
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && index+1 < len(data) && data[index+1] == '/':
			for index < len(data) && data[index] != '\n' {
				index++
			}
			out = append(out, '\n')
		case c == '/' && index+1 < len(data) && data[index+1] == '*':
			end := bytes.Index(data[index+2:], []byte("*/"))
			if end < 0 {
				return out
			}
			index += end + 3
			out = append(out, ' ')
		case c == '#' && lineStart(out):
			// A shell-style comment line, which some hand-edited configs carry.
			for index < len(data) && data[index] != '\n' {
				index++
			}
			out = append(out, '\n')
		case c == '}' || c == ']':
			// A comma before a closing bracket is dropped.
			trimmed := bytes.TrimRight(out, " \t\r\n")
			if len(trimmed) > 0 && trimmed[len(trimmed)-1] == ',' {
				out = append(trimmed[:len(trimmed)-1], out[len(trimmed):]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

func lineStart(out []byte) bool {
	trimmed := bytes.TrimRight(out, " \t")
	return len(trimmed) == 0 || trimmed[len(trimmed)-1] == '\n'
}

// WebPage reports an HTML or XML page, which servers send for errors, logins and challenges.
func WebPage(data []byte) bool {
	head := bytes.ToLower(bytes.TrimSpace(data))
	if len(head) > 1024 {
		head = head[:1024]
	}
	if len(head) == 0 || head[0] != '<' {
		return false
	}
	for _, tag := range []string{"<!doctype", "<html", "<head", "<body", "<?xml", "<!--", "<title", "<script", "<meta", "<div", "<center", "<h1", "<p>", "<pre"} {
		if bytes.HasPrefix(head, []byte(tag)) || bytes.Contains(head, []byte("<html")) {
			return true
		}
	}
	return false
}

// Message returns the gist of a short text a server sent instead of a subscription, such as
// "token expired", or "" when the body is not one.
func Message(data []byte) string {
	text := strings.TrimSpace(string(data))
	if text == "" || len(text) > 4096 || !Text([]byte(text)) || blob(text) {
		return ""
	}
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(line)
	if runes := []rune(line); len(runes) > 120 {
		line = string(runes[:117]) + "..."
	}
	return line
}

// blob reports a long run of Base64 or hex, which is data rather than a message.
func blob(text string) bool {
	if len(text) < 32 {
		return false
	}
	for _, r := range text {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/', r == '=', r == '-', r == '_', r == '\n', r == '\r':
		default:
			return false
		}
	}
	return true
}
