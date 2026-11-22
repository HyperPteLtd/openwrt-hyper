// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package uci reads UCI config files (/etc/config/*). Writes go through the uci CLI so
// that rpcd, LuCI and uci-defaults see the same staged changes.
package uci

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
)

// Section is one `config <type> [name]` block. Anonymous sections get "@type[i]".
type Section struct {
	Type    string
	Name    string
	Options map[string]string
	Lists   map[string][]string
}

// Get returns an option, or the first list item when the key is a list.
func (s *Section) Get(key string) string {
	if s == nil {
		return ""
	}
	if v, ok := s.Options[key]; ok {
		return v
	}
	if l := s.Lists[key]; len(l) > 0 {
		return l[0]
	}
	return ""
}

// List returns a list, or a one-item list for a plain option.
func (s *Section) List(key string) []string {
	if s == nil {
		return nil
	}
	if l, ok := s.Lists[key]; ok {
		return l
	}
	if v, ok := s.Options[key]; ok {
		return []string{v}
	}
	return nil
}

// Config is one parsed file, sections in file order.
type Config struct {
	Sections []*Section
}

// Section returns the named section, or nil.
func (c *Config) Section(name string) *Section {
	for _, s := range c.Sections {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// Get is a shorthand for Section(section).Get(key).
func (c *Config) Get(section, key string) string { return c.Section(section).Get(key) }

// OfType returns every section of a type, in file order.
func (c *Config) OfType(typ string) []*Section {
	var out []*Section
	for _, s := range c.Sections {
		if s.Type == typ {
			out = append(out, s)
		}
	}
	return out
}

// Load parses a UCI file.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// Parse reads UCI syntax: config/option/list lines, '…' or "…" quoting, # comments.
func Parse(r io.Reader) (*Config, error) {
	c := &Config{}
	count := map[string]int{}
	var cur *Section
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		words, err := split(sc.Text())
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if len(words) == 0 {
			continue
		}
		switch words[0] {
		case "package":
		case "config":
			if len(words) < 2 || len(words) > 3 {
				return nil, fmt.Errorf("line %d: bad config line", n)
			}
			cur = &Section{Type: words[1], Options: map[string]string{}, Lists: map[string][]string{}}
			if len(words) == 3 {
				cur.Name = words[2]
			} else {
				cur.Name = fmt.Sprintf("@%s[%d]", words[1], count[words[1]])
			}
			count[words[1]]++
			c.Sections = append(c.Sections, cur)
		case "option", "list":
			if cur == nil || len(words) != 3 {
				return nil, fmt.Errorf("line %d: bad %s line", n, words[0])
			}
			if words[0] == "option" {
				cur.Options[words[1]] = words[2]
			} else {
				cur.Lists[words[1]] = append(cur.Lists[words[1]], words[2])
			}
		default:
			return nil, fmt.Errorf("line %d: unknown keyword %q", n, words[0])
		}
	}
	return c, sc.Err()
}

func split(line string) ([]string, error) {
	var words []string
	var b strings.Builder
	in, quote := false, byte(0)
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			} else if ch == '\\' && quote == '"' && i+1 < len(line) {
				i++
				b.WriteByte(line[i])
			} else {
				b.WriteByte(ch)
			}
		case ch == '\'' || ch == '"':
			quote, in = ch, true
		case ch == '#':
			i = len(line)
		case ch == ' ' || ch == '\t':
			if in {
				words = append(words, b.String())
				b.Reset()
				in = false
			}
		case ch == '\\' && i+1 < len(line):
			i++
			b.WriteByte(line[i])
			in = true
		default:
			b.WriteByte(ch)
			in = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if in {
		words = append(words, b.String())
	}
	return words, nil
}

// Ensure returns the named section, adding it with typ when missing.
func (c *Config) Ensure(name, typ string) *Section {
	if s := c.Section(name); s != nil {
		return s
	}
	s := &Section{Type: typ, Name: name, Options: map[string]string{}, Lists: map[string][]string{}}
	c.Sections = append(c.Sections, s)
	return s
}

// Set stores an option (replacing a list of the same name).
func (s *Section) Set(key, value string) {
	delete(s.Lists, key)
	s.Options[key] = value
}

// SetList stores a list; an empty list deletes the key.
func (s *Section) SetList(key string, values []string) {
	delete(s.Options, key)
	if len(values) == 0 {
		delete(s.Lists, key)
		return
	}
	s.Lists[key] = append([]string(nil), values...)
}

// Delete removes an option or list.
func (s *Section) Delete(key string) {
	delete(s.Options, key)
	delete(s.Lists, key)
}

// Bytes renders the config in UCI syntax. Comments are not kept.
func (c *Config) Bytes() []byte {
	var b strings.Builder
	for i, s := range c.Sections {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("config " + s.Type)
		if !strings.HasPrefix(s.Name, "@") {
			b.WriteString(" " + quote(s.Name))
		}
		b.WriteByte('\n')
		for _, k := range sortedKeys(s.Options) {
			b.WriteString("\toption " + k + " " + quote(s.Options[k]) + "\n")
		}
		for _, k := range sortedKeys(s.Lists) {
			for _, v := range s.Lists[k] {
				b.WriteString("\tlist " + k + " " + quote(v) + "\n")
			}
		}
	}
	return []byte(b.String())
}

// Save writes the config atomically with mode 0600.
func (c *Config) Save(path string) error {
	return atomicfile.WriteFile(path, c.Bytes(), 0o600)
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
