// Package config reads the optional config file of shoulder. The file is
// TOML. shoulder reads the part of TOML that the file needs: comments, the
// [keys] table, and settings with a string, a boolean or an array of
// strings as the value.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Binding is one setting of the [keys] table: an action and its keys.
type Binding struct {
	Action string
	Keys   []string
	Line   int
}

// Config is the content of the config file. The settings before the first
// table are the defaults of the flags. A nil pointer is a setting that the
// file does not set.
type Config struct {
	Path string
	Keys []Binding

	Since    *time.Duration
	All      *bool
	Signs    *bool
	Splits   *bool
	HideDone *bool
	Editor   string
}

// settings lists the names of the settings before the first table.
var settings = []string{"since", "all", "signs", "splits", "hide_done", "editor"}

// Path returns the path of the config file. It reports true when the user
// set the path with SHOULDER_CONFIG.
func Path() (string, bool) {
	if p := os.Getenv("SHOULDER_CONFIG"); p != "" {
		return p, true
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		h, _ := os.UserHomeDir()
		dir = filepath.Join(h, ".config")
	}
	return filepath.Join(dir, "shoulder", "config.toml"), false
}

// Load reads the config file. A missing file gives an empty config, unless
// SHOULDER_CONFIG names it.
func Load() (Config, error) {
	p, named := Path()
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !named {
			return Config{Path: p}, nil
		}
		return Config{Path: p}, err
	}
	c, err := Parse(b)
	c.Path = p
	if err != nil {
		return c, fmt.Errorf("%s: %w", p, err)
	}
	return c, nil
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Parse reads the content of a config file. It ignores a UTF-8 byte order
// mark at the start.
func Parse(b []byte) (Config, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	var c Config
	table := ""
	seen := map[string]int{}
	for i, line := range strings.Split(string(b), "\n") {
		n := i + 1
		s := strings.TrimSpace(line)
		if s == "" || s[0] == '#' {
			continue
		}
		if s[0] == '[' {
			end := strings.IndexByte(s, ']')
			if end < 0 || strings.HasPrefix(s, "[[") || !isComment(s[end+1:]) {
				return c, fmt.Errorf("line %d: write a table header as [name]", n)
			}
			table = strings.TrimSpace(s[1:end])
			if table != "keys" {
				return c, fmt.Errorf("line %d: unknown table [%s]. The only table is [keys]", n, table)
			}
			continue
		}
		name, rest, ok := strings.Cut(s, "=")
		name = strings.TrimSpace(name)
		if !ok || !bareKey.MatchString(name) {
			return c, fmt.Errorf(`line %d: write a setting as name = "value"`, n)
		}
		if prev, ok := seen[table+"."+name]; ok {
			return c, fmt.Errorf("line %d: %s is already set on line %d", n, name, prev)
		}
		seen[table+"."+name] = n
		rest = strings.TrimSpace(rest)
		if table == "keys" {
			vals, err := values(rest)
			if err != nil {
				return c, fmt.Errorf("line %d: %s: %v", n, name, err)
			}
			c.Keys = append(c.Keys, Binding{Action: name, Keys: vals, Line: n})
			continue
		}
		if err := c.set(name, rest); err != nil {
			return c, fmt.Errorf("line %d: %s", n, err)
		}
	}
	return c, nil
}

// set stores one setting from before the first table.
func (c *Config) set(name, raw string) error {
	flag := func(p **bool) error {
		v, rest, ok := boolean(raw)
		if !ok || !isComment(rest) {
			return fmt.Errorf("%s: write true or false", name)
		}
		*p = &v
		return nil
	}
	switch name {
	case "all":
		return flag(&c.All)
	case "signs":
		return flag(&c.Signs)
	case "splits":
		return flag(&c.Splits)
	case "hide_done":
		return flag(&c.HideDone)
	case "since", "editor":
		if raw == "" {
			return fmt.Errorf("%s: the value is missing", name)
		}
		v, rest, err := str(raw)
		switch {
		case err != nil:
			return fmt.Errorf("%s: %v", name, err)
		case !isComment(rest):
			return fmt.Errorf("%s: there is text after the value", name)
		}
		if name == "editor" {
			c.Editor = v
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf(`since: write a duration such as "30m" or "6h"`)
		}
		c.Since = &d
		return nil
	}
	return fmt.Errorf("unknown setting %q. The settings are %s, and the [keys] table", name, strings.Join(settings, ", "))
}

// boolean reads true or false at the start of s and returns the rest of s.
func boolean(s string) (bool, string, bool) {
	for _, w := range []string{"true", "false"} {
		if strings.HasPrefix(s, w) {
			rest := s[len(w):]
			if rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '#' {
				return w == "true", rest, true
			}
		}
	}
	return false, s, false
}

// values reads a string, or an array of strings on one line, and the
// comment after it.
func values(s string) ([]string, error) {
	if s == "" {
		return nil, errors.New("the value is missing")
	}
	if s[0] != '[' {
		v, rest, err := str(s)
		if err != nil {
			return nil, err
		}
		if !isComment(rest) {
			return nil, errors.New("there is text after the value")
		}
		return []string{v}, nil
	}
	out := []string{}
	s = strings.TrimSpace(s[1:])
	for {
		if s == "" {
			return nil, errors.New("the array has no closing ]")
		}
		if s[0] == ']' {
			break
		}
		v, rest, err := str(s)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		s = strings.TrimSpace(rest)
		if s != "" && s[0] == ',' {
			s = strings.TrimSpace(s[1:])
			continue
		}
		if s == "" {
			return nil, errors.New("the array has no closing ]")
		}
		if s[0] != ']' {
			return nil, errors.New("put a comma between the values of the array")
		}
	}
	if !isComment(s[1:]) {
		return nil, errors.New("there is text after the value")
	}
	return out, nil
}

// str reads one quoted string at the start of s and returns the rest of s.
func str(s string) (string, string, error) {
	switch s[0] {
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", "", errors.New("the string has no closing quote")
		}
		return s[1 : 1+end], s[2+end:], nil
	case '"':
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				i++
			case '"':
				v, err := strconv.Unquote(s[:i+1])
				if err != nil {
					return "", "", errors.New("the string has an escape that is not valid")
				}
				return v, s[i+1:], nil
			}
		}
		return "", "", errors.New("the string has no closing quote")
	}
	return "", "", errors.New(`put the value in quotes, for example "n"`)
}

func isComment(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || s[0] == '#'
}
