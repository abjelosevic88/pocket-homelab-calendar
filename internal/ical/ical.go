// Package ical reads and writes iCalendar (RFC 5545) text and maps VTODOs to
// tasks and tasks to the events of the calendar mirror. It keeps every
// property it does not understand, so a task written by Apple Reminders or
// Thunderbird survives an edit made here.
package ical

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Param is one property parameter (TZID=Europe/Sarajevo).
type Param struct {
	Name   string
	Values []string
}

// Property is one content line, unfolded and unescaped where text.
type Property struct {
	Name   string
	Params []Param
	Value  string // raw value as on the line (escaped for TEXT); use Text() for the text
}

// Param returns the first value of a parameter ("" when absent).
func (p *Property) Param(name string) string {
	name = strings.ToUpper(name)
	for _, pp := range p.Params {
		if pp.Name == name && len(pp.Values) > 0 {
			return pp.Values[0]
		}
	}
	return ""
}

// SetParam sets a parameter (replacing an existing one).
func (p *Property) SetParam(name, value string) {
	name = strings.ToUpper(name)
	for i := range p.Params {
		if p.Params[i].Name == name {
			p.Params[i].Values = []string{value}
			return
		}
	}
	p.Params = append(p.Params, Param{Name: name, Values: []string{value}})
}

// Text returns the value with TEXT escapes undone.
func (p *Property) Text() string { return Unescape(p.Value) }

// Component is BEGIN:X … END:X with its properties and children in order.
type Component struct {
	Name     string
	Props    []Property
	Children []*Component
}

// Get returns the first property with that name (nil when absent).
func (c *Component) Get(name string) *Property {
	name = strings.ToUpper(name)
	for i := range c.Props {
		if c.Props[i].Name == name {
			return &c.Props[i]
		}
	}
	return nil
}

// Value returns the first property's raw value ("" when absent).
func (c *Component) Value(name string) string {
	if p := c.Get(name); p != nil {
		return p.Value
	}
	return ""
}

// Text returns the first property's unescaped text.
func (c *Component) Text(name string) string {
	if p := c.Get(name); p != nil {
		return p.Text()
	}
	return ""
}

// GetAll returns every property with that name.
func (c *Component) GetAll(name string) []Property {
	name = strings.ToUpper(name)
	var out []Property
	for _, p := range c.Props {
		if p.Name == name {
			out = append(out, p)
		}
	}
	return out
}

// Set replaces every property of that name with one (keeping its place).
func (c *Component) Set(p Property) {
	p.Name = strings.ToUpper(p.Name)
	idx := -1
	kept := c.Props[:0]
	for _, q := range c.Props {
		if q.Name == p.Name {
			if idx < 0 {
				idx = len(kept)
				kept = append(kept, p)
			}
			continue
		}
		kept = append(kept, q)
	}
	if idx < 0 {
		kept = append(kept, p)
	}
	c.Props = kept
}

// SetText sets a TEXT property (escaping the value).
func (c *Component) SetText(name, value string, params ...Param) {
	c.Set(Property{Name: name, Params: params, Value: Escape(value)})
}

// SetRaw sets a non-text property.
func (c *Component) SetRaw(name, value string, params ...Param) {
	c.Set(Property{Name: name, Params: params, Value: value})
}

// Remove drops every property of that name.
func (c *Component) Remove(name string) {
	name = strings.ToUpper(name)
	kept := c.Props[:0]
	for _, q := range c.Props {
		if q.Name != name {
			kept = append(kept, q)
		}
	}
	c.Props = kept
}

// Child returns the first child component of that name.
func (c *Component) Child(name string) *Component {
	name = strings.ToUpper(name)
	for _, ch := range c.Children {
		if ch.Name == name {
			return ch
		}
	}
	return nil
}

// ChildrenNamed returns every child component of that name.
func (c *Component) ChildrenNamed(name string) []*Component {
	name = strings.ToUpper(name)
	var out []*Component
	for _, ch := range c.Children {
		if ch.Name == name {
			out = append(out, ch)
		}
	}
	return out
}

// RemoveChildren drops every child of that name.
func (c *Component) RemoveChildren(name string) {
	name = strings.ToUpper(name)
	kept := c.Children[:0]
	for _, ch := range c.Children {
		if ch.Name != name {
			kept = append(kept, ch)
		}
	}
	c.Children = kept
}

// ErrNoComponent is returned when the text holds no component at all.
var ErrNoComponent = errors.New("no iCalendar component")

// Parse reads one top-level component (normally VCALENDAR). Lines are
// unfolded; CRLF and LF both accepted; a BOM is skipped.
func Parse(s string) (*Component, error) {
	s = strings.TrimPrefix(s, "\ufeff")
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
			continue
		}
		lines = append(lines, line)
	}
	var stack []*Component
	var root *Component
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		p, err := parseLine(line)
		if err != nil {
			return nil, err
		}
		switch p.Name {
		case "BEGIN":
			c := &Component{Name: strings.ToUpper(strings.TrimSpace(p.Value))}
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.Children = append(top.Children, c)
			} else if root == nil {
				root = c
			}
			stack = append(stack, c)
		case "END":
			if len(stack) == 0 {
				return nil, fmt.Errorf("END:%s without BEGIN", p.Value)
			}
			stack = stack[:len(stack)-1]
		default:
			if len(stack) == 0 {
				continue // junk before BEGIN
			}
			top := stack[len(stack)-1]
			top.Props = append(top.Props, p)
		}
	}
	if root == nil {
		return nil, ErrNoComponent
	}
	return root, nil
}

func parseLine(line string) (Property, error) {
	// NAME(;PARAM=VALUE)*:VALUE — parameter values may be quoted and contain ':'.
	var p Property
	i := 0
	inQuote := false
	for i < len(line) {
		ch := line[i]
		if ch == '"' {
			inQuote = !inQuote
		}
		if !inQuote && (ch == ';' || ch == ':') {
			break
		}
		i++
	}
	if i >= len(line) {
		return p, fmt.Errorf("malformed content line %q", truncate(line, 60))
	}
	p.Name = strings.ToUpper(strings.TrimSpace(line[:i]))
	for line[i] == ';' {
		j := i + 1
		inQuote = false
		for j < len(line) {
			ch := line[j]
			if ch == '"' {
				inQuote = !inQuote
			}
			if !inQuote && (ch == ';' || ch == ':') {
				break
			}
			j++
		}
		if j >= len(line) {
			return p, fmt.Errorf("malformed parameter in %q", truncate(line, 60))
		}
		kv := line[i+1 : j]
		name, val, _ := strings.Cut(kv, "=")
		vals := splitParamValues(val)
		p.Params = append(p.Params, Param{Name: strings.ToUpper(strings.TrimSpace(name)), Values: vals})
		i = j
	}
	p.Value = line[i+1:]
	return p, nil
}

func splitParamValues(v string) []string {
	var out []string
	cur := strings.Builder{}
	inQuote := false
	for _, r := range v {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	out = append(out, cur.String())
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// String serialises the component with CRLF line ends and lines folded at
// 75 octets, as the RFC asks.
func (c *Component) String() string {
	var b strings.Builder
	c.write(&b)
	return b.String()
}

func (c *Component) write(b *strings.Builder) {
	fold(b, "BEGIN:"+c.Name)
	for _, p := range c.Props {
		fold(b, p.line())
	}
	for _, ch := range c.Children {
		ch.write(b)
	}
	fold(b, "END:"+c.Name)
}

func (p Property) line() string {
	var b strings.Builder
	b.WriteString(p.Name)
	for _, pp := range p.Params {
		b.WriteByte(';')
		b.WriteString(pp.Name)
		b.WriteByte('=')
		for i, v := range pp.Values {
			if i > 0 {
				b.WriteByte(',')
			}
			if strings.ContainsAny(v, ":;,") {
				b.WriteByte('"')
				b.WriteString(v)
				b.WriteByte('"')
			} else {
				b.WriteString(v)
			}
		}
	}
	b.WriteByte(':')
	b.WriteString(p.Value)
	return b.String()
}

func fold(b *strings.Builder, line string) {
	max := 75
	for len(line) > max {
		cut := max
		// never cut a UTF-8 sequence
		for cut > 0 && line[cut]&0xC0 == 0x80 {
			cut--
		}
		b.WriteString(line[:cut])
		b.WriteString("\r\n ")
		line = line[cut:]
		max = 74 // the continuation's leading space counts
	}
	b.WriteString(line)
	b.WriteString("\r\n")
}

// Escape makes a TEXT value: backslash, semicolon, comma and newlines.
func Escape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

// Unescape undoes Escape.
func Unescape(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			case '\\', ';', ',':
				b.WriteByte(s[i])
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ETag is a strong entity tag for an iCalendar text.
func ETag(ics string) string {
	sum := sha256.Sum256([]byte(ics))
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// NewCalendar returns an empty VCALENDAR with the usual header.
func NewCalendar() *Component {
	c := &Component{Name: "VCALENDAR"}
	c.SetRaw("VERSION", "2.0")
	c.SetRaw("PRODID", "-//Pocket Homelab//Pocket Calendar//EN")
	c.SetRaw("CALSCALE", "GREGORIAN")
	return c
}
