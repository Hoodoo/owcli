// Package okf reads, validates, repairs, and edits Open Knowledge Format
// (OKF) v0.2 concept pages.
//
// Edits are line-preserving: a setter replaces only the lines of the field it
// owns and leaves every other front matter line, including producer extension
// fields, byte-for-byte intact.
package okf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// GeneratedField marks front matter whose type/title owcli derived rather
// than an author wrote. The name matches upstream for format compatibility.
const GeneratedField = "openwiki_generated"

// DefaultConceptType is the type given to pages that lack one.
const DefaultConceptType = "Reference"

// Field names with special validation.
var (
	stringFields         = []string{"type", "title", "description", "resource", "timestamp"}
	optionalStringFields = []string{"description", "resource", "timestamp"}
	statusValues         = []string{"draft", "stable", "deprecated"}
)

// Issue is one validation finding.
type Issue struct {
	Code    string
	Message string
	Line    int // 1-based; 0 when not tied to a line
}

func (i Issue) String() string { return i.Code + ": " + i.Message }

// Split separates a leading front matter block from the body. The block is
// the text between an opening line that is exactly "---" and the next line
// that is exactly "---" (CRLF tolerated). ok is false when there is no
// complete block; body is then the whole content.
func Split(content string) (block, body string, ok bool) {
	first, rest, found := cutLine(content)
	if !found || first != "---" {
		return "", content, false
	}
	var lines []string
	for rest != "" {
		line, next, _ := cutLine(rest)
		if line == "---" {
			return strings.Join(lines, "\n"), next, true
		}
		lines = append(lines, line)
		rest = next
	}
	return "", content, false
}

// cutLine returns the first line without its terminator and the remainder.
// found is false only for empty input.
func cutLine(s string) (line, rest string, found bool) {
	if s == "" {
		return "", "", false
	}
	i := strings.IndexByte(s, '\n')
	if i < 0 {
		return s, "", true
	}
	return strings.TrimSuffix(s[:i], "\r"), s[i+1:], true
}

// mapping is a parsed front matter block: ordered keys and value nodes.
type mapping struct {
	keys   []string
	values map[string]*yaml.Node
}

func (m *mapping) has(k string) bool { _, ok := m.values[k]; return ok }

// parseBlock parses front matter YAML. It rejects non-mapping roots and
// duplicate keys.
func parseBlock(block string) (*mapping, *Issue) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(block), &doc); err != nil {
		return nil, &Issue{Code: "invalid_yaml", Message: err.Error()}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || resolve(doc.Content[0]).Kind != yaml.MappingNode {
		return nil, &Issue{Code: "invalid_yaml_root", Message: "Front matter must be a YAML mapping."}
	}
	root := resolve(doc.Content[0])
	m := &mapping{values: map[string]*yaml.Node{}}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := resolve(root.Content[i])
		if k.Kind != yaml.ScalarNode {
			return nil, &Issue{Code: "invalid_yaml", Message: "front matter keys must be scalars", Line: k.Line + 1}
		}
		if m.has(k.Value) {
			return nil, &Issue{Code: "invalid_yaml", Message: fmt.Sprintf("duplicate key %q", k.Value), Line: k.Line + 1}
		}
		m.keys = append(m.keys, k.Value)
		m.values[k.Value] = resolve(root.Content[i+1])
	}
	return m, nil
}

func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// asString reports a scalar's text when YAML's core schema would read it as
// a string. Unquoted timestamps count as strings there.
func asString(n *yaml.Node) (string, bool) {
	n = resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return "", false
	}
	switch n.ShortTag() {
	case "!!str", "!!timestamp":
		return n.Value, true
	}
	return "", false
}

func nonEmptyString(n *yaml.Node) bool {
	s, ok := asString(n)
	return ok && strings.TrimSpace(s) != ""
}

// mappingField returns the value of key in a mapping node.
func mappingField(n *yaml.Node, key string) (*yaml.Node, bool) {
	n = resolve(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if resolve(n.Content[i]).Value == key {
			return resolve(n.Content[i+1]), true
		}
	}
	return nil, false
}

// isActorEvent checks an OKF {by, at} event: non-empty string by, and an
// optional at that is an ISO 8601 datetime with explicit offset.
func isActorEvent(n *yaml.Node) bool {
	n = resolve(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return false
	}
	by, ok := mappingField(n, "by")
	if !ok || !nonEmptyString(by) {
		return false
	}
	if at, ok := mappingField(n, "at"); ok {
		s, isStr := asString(at)
		return isStr && IsDateTimeWithOffset(s)
	}
	return true
}

// listOrSingle returns the items of a sequence, or the node itself.
func listOrSingle(n *yaml.Node) []*yaml.Node {
	n = resolve(n)
	if n.Kind == yaml.SequenceNode {
		return n.Content
	}
	return []*yaml.Node{n}
}

func isSource(n *yaml.Node) bool {
	r, ok := mappingField(n, "resource")
	return ok && nonEmptyString(r)
}

var isoDateTime = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|[+-](\d{2}):(\d{2}))$`)

// IsDateTimeWithOffset reports whether s is an ISO 8601 datetime with a real
// calendar date and an explicit "Z" or ±hh:mm offset.
func IsDateTimeWithOffset(s string) bool {
	m := isoDateTime.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	n := func(i int) int {
		if m[i] == "" {
			return 0
		}
		v, _ := strconv.Atoi(m[i])
		return v
	}
	year, month, day := n(1), n(2), n(3)
	return month >= 1 && month <= 12 && day >= 1 && day <= daysIn(year, month) &&
		n(4) <= 23 && n(5) <= 59 && n(6) <= 59 && n(7) <= 23 && n(8) <= 59
}

func daysIn(year, month int) int {
	switch month {
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// Validate checks a page's front matter against OKF v0.2. It returns nil for
// a conformant page. Unknown keys are allowed.
func Validate(content string) []Issue {
	if first, _, _ := cutLine(content); first != "---" {
		return []Issue{{Code: "missing_opening_delimiter", Message: "File must begin with `---`.", Line: 1}}
	}
	block, _, ok := Split(content)
	if !ok {
		return []Issue{{Code: "missing_closing_delimiter", Message: "Opening front matter has no closing `---` delimiter."}}
	}
	m, perr := parseBlock(block)
	if perr != nil {
		return []Issue{*perr}
	}
	return validateMapping(m)
}

func validateMapping(m *mapping) []Issue {
	var issues []Issue
	add := func(code, msg string) { issues = append(issues, Issue{Code: code, Message: msg}) }

	if !m.has("type") {
		add("missing_type", "Required field `type` is missing.")
	}
	for _, f := range stringFields {
		if m.has(f) && !nonEmptyString(m.values[f]) {
			add("invalid_"+f, fmt.Sprintf("Field `%s` must be a non-empty string.", f))
		}
	}
	if m.has("tags") && !validTags(m.values["tags"]) {
		add("invalid_tags", "Field `tags` must be a YAML list of non-empty strings.")
	}
	if m.has("generated") && !isActorEvent(m.values["generated"]) {
		add("invalid_generated", "Field `generated` must be a mapping with a non-empty string `by` and an optional `at` ISO 8601 datetime with an explicit offset.")
	}
	if m.has("verified") {
		for _, e := range listOrSingle(m.values["verified"]) {
			if !isActorEvent(e) {
				add("invalid_verified", "Field `verified` must be a `{by, at}` mapping or a list of them, each with a non-empty `by` and any `at` as an ISO 8601 datetime with an explicit offset.")
				break
			}
		}
	}
	if m.has("sources") && !validSources(m.values["sources"]) {
		add("invalid_sources", "Field `sources` must be a YAML list of mappings, each with a non-empty string `resource`.")
	}
	if m.has("status") {
		if s, ok := asString(m.values["status"]); !ok || !contains(statusValues, s) {
			add("invalid_status", "Field `status` must be one of `draft`, `stable`, or `deprecated`.")
		}
	}
	if m.has("stale_after") {
		if s, ok := asString(m.values["stale_after"]); !ok || !IsDateTimeWithOffset(s) {
			add("invalid_stale_after", "Field `stale_after` must be an ISO 8601 datetime with an explicit offset.")
		}
	}
	return issues
}

func validTags(n *yaml.Node) bool {
	if n.Kind != yaml.SequenceNode {
		return false
	}
	for _, t := range n.Content {
		if !nonEmptyString(t) {
			return false
		}
	}
	return true
}

func validSources(n *yaml.Node) bool {
	if n.Kind != yaml.SequenceNode {
		return false
	}
	for _, s := range n.Content {
		if !isSource(s) {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Fields decodes front matter into plain Go values for consumers that only
// read it. ok is false without a parseable mapping block. Timestamps stay
// strings.
func Fields(content string) (map[string]any, bool) {
	block, _, ok := Split(content)
	if !ok {
		return nil, false
	}
	m, perr := parseBlock(block)
	if perr != nil {
		return nil, false
	}
	fields := make(map[string]any, len(m.keys))
	for _, k := range m.keys {
		fields[k] = plain(m.values[k])
	}
	return fields, true
}

// plain converts a node to Go values under YAML's core schema: maps, slices,
// strings, ints, floats, bools, nil. Timestamps are not a core type and stay
// strings.
func plain(n *yaml.Node) any {
	n = resolve(n)
	switch n.Kind {
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			out[resolve(n.Content[i]).Value] = plain(n.Content[i+1])
		}
		return out
	case yaml.SequenceNode:
		out := make([]any, len(n.Content))
		for i, c := range n.Content {
			out[i] = plain(c)
		}
		return out
	case yaml.ScalarNode:
		if s, ok := asString(n); ok {
			return s
		}
		var v any
		if err := n.Decode(&v); err != nil {
			return n.Value
		}
		return v
	}
	return nil
}

// StringField returns a non-empty string field, if present.
func StringField(fields map[string]any, key string) (string, bool) {
	s, ok := fields[key].(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}

// StringList returns the non-empty strings of a list field.
func StringList(fields map[string]any, key string) []string {
	items, _ := fields[key].([]any)
	var out []string
	for _, it := range items {
		if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// Body returns content without its front matter block.
func Body(content string) string {
	_, body, _ := Split(content)
	return body
}

// --- line-preserving edits ---

// quote renders s as a JSON string, which is also a valid YAML double-quoted
// scalar.
func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// SetField sets key to a quoted string scalar.
func SetField(content, key, value string) string {
	return replaceField(content, key, []string{key + ": " + quote(value)})
}

// SetGenerated writes the generated event as a one-line flow mapping. An
// empty at omits it.
func SetGenerated(content, by, at string) string {
	members := "by: " + quote(by)
	if at != "" {
		members += ", at: " + quote(at)
	}
	return replaceField(content, "generated", []string{"generated: { " + members + " }"})
}

// SetValue renders value as block YAML under key, replacing the existing
// field. Nil or an empty slice removes the field.
func SetValue(content, key string, value any) string {
	if isEmptyValue(value) {
		return RemoveField(content, key)
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]any{key: value}); err != nil {
		panic(fmt.Sprintf("okf: encode %s: %v", key, err)) // values are owcli-built; failure is a bug
	}
	_ = enc.Close()
	return replaceField(content, key, strings.Split(strings.TrimRight(b.String(), "\n"), "\n"))
}

func isEmptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case []any:
		return len(x) == 0
	case []string:
		return len(x) == 0
	case []*yaml.Node:
		return len(x) == 0
	case []map[string]any:
		return len(x) == 0
	}
	return false
}

// RemoveField deletes key and its continuation lines. A block left empty is
// removed entirely.
func RemoveField(content, key string) string {
	return replaceField(content, key, nil)
}

// isFieldLine reports whether line starts the top-level field key.
func isFieldLine(line, key string) bool {
	rest, ok := strings.CutPrefix(line, key)
	return ok && strings.HasPrefix(strings.TrimLeft(rest, " \t"), ":")
}

// replaceField swaps the lines of one top-level field. A field spans its key
// line plus following blank or indented lines. A missing field is appended;
// a missing block is created.
func replaceField(content, key string, replacement []string) string {
	block, body, ok := Split(content)
	if !ok {
		if len(replacement) == 0 {
			return content
		}
		return "---\n" + strings.Join(replacement, "\n") + "\n---\n\n" + content
	}
	var lines []string
	if block != "" {
		lines = strings.Split(block, "\n")
	}
	start := -1
	for i, l := range lines {
		if isFieldLine(l, key) {
			start = i
			break
		}
	}
	if start < 0 {
		if len(replacement) == 0 {
			return content
		}
		lines = append(lines, replacement...)
		return "---\n" + strings.Join(lines, "\n") + "\n---\n" + body
	}
	end := start + 1
	for end < len(lines) && (lines[end] == "" || lines[end][0] == ' ' || lines[end][0] == '\t') {
		end++
	}
	next := append(append(append([]string{}, lines[:start]...), replacement...), lines[end:]...)
	if len(next) == 0 {
		return strings.TrimPrefix(strings.TrimPrefix(body, "\r\n"), "\n")
	}
	return "---\n" + strings.Join(next, "\n") + "\n---\n" + body
}

// --- repair ---

var (
	h1         = regexp.MustCompile(`(?m)^#[ \t]+(.+?)[ \t]*$`)
	separators = regexp.MustCompile(`[-_]+`)
)

// DeriveTitle returns the first H1 of body, else a title made from the file
// name ("grounded-claims.md" -> "Grounded claims").
func DeriveTitle(body, filePath string) string {
	if m := h1.FindStringSubmatch(body); m != nil {
		return strings.TrimSpace(m[1])
	}
	base := path.Base(strings.ReplaceAll(filePath, `\`, "/"))
	if strings.EqualFold(path.Ext(base), ".md") {
		base = base[:len(base)-3]
	}
	spaced := strings.TrimSpace(separators.ReplaceAllString(base, " "))
	if spaced == "" {
		return base
	}
	return strings.ToUpper(spaced[:1]) + spaced[1:]
}

// Repair makes a page's front matter conformant with the smallest truthful
// change. Valid front matter is returned unchanged. A parseable mapping is
// fixed field by field; anything else is replaced by a minimal block of type,
// title, and the generated marker.
func Repair(content, filePath, conceptType string) (string, bool) {
	if Validate(content) == nil {
		return content, false
	}
	if strings.TrimSpace(conceptType) == "" {
		conceptType = DefaultConceptType
	}
	block, body, hasBlock := Split(content)
	title := DeriveTitle(body, filePath)
	rebuild := func() (string, bool) {
		out := "---\ntype: " + quote(conceptType) + "\ntitle: " + quote(title) + "\n" + GeneratedField + ": true\n---\n\n" + body
		return out, out != content
	}
	if !hasBlock {
		return rebuild()
	}
	m, perr := parseBlock(block)
	if perr != nil {
		return rebuild()
	}

	out := content
	typeDerived := !nonEmptyString(m.values["type"])
	if typeDerived {
		out = SetField(out, "type", conceptType)
		out = replaceField(out, GeneratedField, []string{GeneratedField + ": true"})
	}
	if (typeDerived && !m.has("title")) || (m.has("title") && !nonEmptyString(m.values["title"])) {
		out = SetField(out, "title", title)
	}
	for _, f := range optionalStringFields {
		if m.has(f) && !nonEmptyString(m.values[f]) {
			out = RemoveField(out, f)
		}
	}
	if m.has("tags") {
		var tags []string
		if n := m.values["tags"]; n.Kind == yaml.SequenceNode {
			for _, t := range n.Content {
				if s, ok := asString(t); ok && strings.TrimSpace(s) != "" {
					tags = append(tags, s)
				}
			}
		}
		out = SetValue(out, "tags", tags)
	}
	if m.has("generated") && !isActorEvent(m.values["generated"]) {
		out = RemoveField(out, "generated")
	}
	if m.has("verified") {
		var keep []*yaml.Node
		for _, e := range listOrSingle(m.values["verified"]) {
			if isActorEvent(e) {
				keep = append(keep, e)
			}
		}
		out = SetValue(out, "verified", keep)
	}
	if m.has("sources") {
		var keep []*yaml.Node
		if n := m.values["sources"]; n.Kind == yaml.SequenceNode {
			for _, s := range n.Content {
				if isSource(s) {
					keep = append(keep, s)
				}
			}
		}
		out = SetValue(out, "sources", keep)
	}
	if m.has("status") {
		if s, ok := asString(m.values["status"]); !ok || !contains(statusValues, s) {
			out = RemoveField(out, "status")
		}
	}
	if m.has("stale_after") {
		if s, ok := asString(m.values["stale_after"]); !ok || !IsDateTimeWithOffset(s) {
			out = RemoveField(out, "stale_after")
		}
	}
	if Validate(out) == nil {
		return out, out != content
	}
	return rebuild()
}
