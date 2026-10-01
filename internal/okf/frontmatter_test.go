package okf

import (
	"reflect"
	"strings"
	"testing"
)

const validPage = `---
type: concept
title: Grounded Claims
description: How claims work.
tags: [claims, evidence]
sources:
  - id: openwiki-source-abc
    resource: repo://src/a.go
generated: { by: "owcli/1.0", at: "2026-09-30T08:10:27.967Z" }
verified:
  - by: owcli/1.0
    at: 2026-09-30T08:10:27.967Z
status: stable
stale_after: 2027-01-01T00:00:00+01:00
x_producer_extension: {keep: me}
---

# Grounded Claims

Body.
`

func codes(issues []Issue) []string {
	var out []string
	for _, i := range issues {
		out = append(out, i.Code)
	}
	return out
}

func TestValidateAcceptsConformantPage(t *testing.T) {
	if issues := Validate(validPage); issues != nil {
		t.Fatalf("unexpected issues: %v", issues)
	}
	single := "---\ntype: t\nverified: {by: human/alice}\n---\n"
	if issues := Validate(single); issues != nil {
		t.Fatalf("bare verified mapping should be valid: %v", issues)
	}
	crlf := "---\r\ntype: t\r\n---\r\nbody\r\n"
	if issues := Validate(crlf); issues != nil {
		t.Fatalf("CRLF page should be valid: %v", issues)
	}
}

func TestValidateIssues(t *testing.T) {
	tests := []struct {
		content string
		want    []string
	}{
		{"# no front matter", []string{"missing_opening_delimiter"}},
		{"---\ntype: t\n", []string{"missing_closing_delimiter"}},
		{"---\ntype: [unclosed\n---\n", []string{"invalid_yaml"}},
		{"---\ntype: a\ntype: b\n---\n", []string{"invalid_yaml"}},
		{"---\n- a\n---\n", []string{"invalid_yaml_root"}},
		{"---\n---\n", []string{"invalid_yaml_root"}},
		{"---\ntitle: x\n---\n", []string{"missing_type"}},
		{"---\ntype: \"  \"\n---\n", []string{"invalid_type"}},
		{"---\ntype: t\ntitle: 42\n---\n", []string{"invalid_title"}},
		{"---\ntype: t\ndescription: null\n---\n", []string{"invalid_description"}},
		{"---\ntype: t\ntags: one\n---\n", []string{"invalid_tags"}},
		{"---\ntype: t\ntags: [a, \"\"]\n---\n", []string{"invalid_tags"}},
		{"---\ntype: t\ngenerated: {at: \"2026-01-01T00:00:00Z\"}\n---\n", []string{"invalid_generated"}},
		{"---\ntype: t\ngenerated: {by: x, at: \"2026-01-01T00:00:00\"}\n---\n", []string{"invalid_generated"}},
		{"---\ntype: t\nverified: [{by: x}, {by: \"\"}]\n---\n", []string{"invalid_verified"}},
		{"---\ntype: t\nsources: [{id: a}]\n---\n", []string{"invalid_sources"}},
		{"---\ntype: t\nsources: {resource: x}\n---\n", []string{"invalid_sources"}},
		{"---\ntype: t\nstatus: retired\n---\n", []string{"invalid_status"}},
		{"---\ntype: t\nstale_after: 2026-02-30T00:00:00Z\n---\n", []string{"invalid_stale_after"}},
		{"---\ntitle: \"\"\nstatus: x\n---\n", []string{"missing_type", "invalid_title", "invalid_status"}},
	}
	for _, tt := range tests {
		if got := codes(Validate(tt.content)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Validate(%q) = %v, want %v", tt.content, got, tt.want)
		}
	}
}

func TestIsDateTimeWithOffset(t *testing.T) {
	good := []string{"2024-02-29T23:59:59Z", "2026-09-30T08:10:27.967Z", "2026-01-01T00:00:00-05:30"}
	bad := []string{"2023-02-29T00:00:00Z", "2026-13-01T00:00:00Z", "2026-01-01T24:00:00Z", "2026-01-01T00:00:00", "2026-01-01", "2026-04-31T00:00:00Z", "2026-01-01T00:00:00+24:00"}
	for _, s := range good {
		if !IsDateTimeWithOffset(s) {
			t.Errorf("%s should be valid", s)
		}
	}
	for _, s := range bad {
		if IsDateTimeWithOffset(s) {
			t.Errorf("%s should be invalid", s)
		}
	}
}

func TestFields(t *testing.T) {
	f, ok := Fields(validPage)
	if !ok {
		t.Fatal("Fields failed")
	}
	if title, _ := StringField(f, "title"); title != "Grounded Claims" {
		t.Errorf("title = %q", title)
	}
	if tags := StringList(f, "tags"); !reflect.DeepEqual(tags, []string{"claims", "evidence"}) {
		t.Errorf("tags = %v", tags)
	}
	if s, ok := f["stale_after"].(string); !ok || !strings.HasPrefix(s, "2027") {
		t.Errorf("timestamps must stay strings, got %T", f["stale_after"])
	}
	if _, ok := Fields("no block"); ok {
		t.Error("Fields without block should fail")
	}
	if !strings.HasPrefix(Body(validPage), "\n# Grounded Claims") {
		t.Errorf("Body = %q", Body(validPage))
	}
}

func TestSetFieldPreservesOtherLines(t *testing.T) {
	in := "---\ntype: t\ntitle: Old\nx_ext:\n  nested: 1\n---\nbody\n"
	got := SetField(in, "title", `New: "quoted" <b>`)
	want := "---\ntype: t\ntitle: \"New: \\\"quoted\\\" <b>\"\nx_ext:\n  nested: 1\n---\nbody\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if Validate(got) != nil {
		t.Error("result should validate")
	}
	f, _ := Fields(got)
	if f["title"] != `New: "quoted" <b>` {
		t.Errorf("round trip title = %q", f["title"])
	}
}

func TestSetFieldAppendsAndCreatesBlock(t *testing.T) {
	if got := SetField("---\ntype: t\n---\nb", "title", "T"); got != "---\ntype: t\ntitle: \"T\"\n---\nb" {
		t.Errorf("append: %q", got)
	}
	if got := SetField("# Body\n", "type", "t"); got != "---\ntype: \"t\"\n---\n\n# Body\n" {
		t.Errorf("create: %q", got)
	}
}

func TestFieldPrefixIsNotConfused(t *testing.T) {
	in := "---\ntype: t\ntitle_extra: keep\ntitle: x\n---\n"
	got := SetField(in, "title", "y")
	if !strings.Contains(got, "title_extra: keep\ntitle: \"y\"") {
		t.Errorf("got %q", got)
	}
}

func TestSetGenerated(t *testing.T) {
	in := "---\ntype: t\ngenerated:\n  by: old\n  at: 2020-01-01T00:00:00Z\ntitle: T\n---\n"
	got := SetGenerated(in, "owcli/1.0", "2026-10-01T00:00:00Z")
	want := "---\ntype: t\ngenerated: { by: \"owcli/1.0\", at: \"2026-10-01T00:00:00Z\" }\ntitle: T\n---\n"
	if got != want {
		t.Fatalf("got\n%s", got)
	}
	if Validate(got) != nil {
		t.Error("should validate")
	}
	if got := SetGenerated("---\ntype: t\n---\n", "a", ""); !strings.Contains(got, "generated: { by: \"a\" }") {
		t.Errorf("no-at form: %q", got)
	}
}

func TestSetValueAndRemove(t *testing.T) {
	in := "---\ntype: t\nsources: []\nz: 1\n---\nb"
	got := SetValue(in, "sources", []map[string]string{{"resource": "repo://a.go"}})
	want := "---\ntype: t\nsources:\n  - resource: repo://a.go\nz: 1\n---\nb"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	if got := SetValue(in, "sources", []string(nil)); got != "---\ntype: t\nz: 1\n---\nb" {
		t.Errorf("empty value should remove: %q", got)
	}
	if got := RemoveField("---\ntype: t\n---\n\nbody", "type"); got != "body" {
		t.Errorf("removing last field should drop block: %q", got)
	}
	if got := RemoveField("body", "type"); got != "body" {
		t.Errorf("no block: %q", got)
	}
}

func TestRepairLeavesValidPageUntouched(t *testing.T) {
	got, changed := Repair(validPage, "concepts/x.md", "")
	if changed || got != validPage {
		t.Fatal("valid page must be byte-for-byte unchanged")
	}
}

func TestRepairSurgical(t *testing.T) {
	in := "---\ntitle: \"\"\ndescription: 3\ntags: [a, 7, \"\"]\nstatus: bogus\nstale_after: soon\ngenerated: {by: \"\"}\nverified:\n  - by: ok\n  - by: \"\"\nsources:\n  - resource: repo://a.go\n    id: s1\n  - id: orphan\nx_ext: keep # comment\n---\n# Real Title\n\nbody\n"
	got, changed := Repair(in, "dir/page.md", "")
	if !changed {
		t.Fatal("expected change")
	}
	if issues := Validate(got); issues != nil {
		t.Fatalf("repaired page invalid: %v\n%s", issues, got)
	}
	f, _ := Fields(got)
	checks := map[string]any{
		"type":         DefaultConceptType,
		"title":        "Real Title",
		GeneratedField: true,
		"x_ext":        "keep",
	}
	for k, v := range checks {
		if f[k] != v {
			t.Errorf("%s = %v, want %v", k, f[k], v)
		}
	}
	for _, k := range []string{"description", "status", "stale_after", "generated"} {
		if _, ok := f[k]; ok {
			t.Errorf("%s should be removed", k)
		}
	}
	if tags := StringList(f, "tags"); !reflect.DeepEqual(tags, []string{"a"}) {
		t.Errorf("tags = %v (7 is an int, not a string)", tags)
	}
	if v := f["verified"].([]any); len(v) != 1 {
		t.Errorf("verified = %v", v)
	}
	if s := f["sources"].([]any); len(s) != 1 || s[0].(map[string]any)["id"] != "s1" {
		t.Errorf("sources = %v", s)
	}
	if !strings.Contains(got, "x_ext: keep # comment\n") {
		t.Error("extension line must survive byte-for-byte")
	}
	if !strings.HasSuffix(got, "---\n# Real Title\n\nbody\n") {
		t.Error("body must be untouched")
	}
}

func TestRepairRebuildsUnparseable(t *testing.T) {
	for _, in := range []string{
		"---\ntype: [oops\nx_ext: lost\n---\nbody\n",
		"no front matter at all\n",
		"---\n---\nbody\n",
	} {
		got, changed := Repair(in, "concepts/grounded-claims.md", "Guide")
		if !changed || Validate(got) != nil {
			t.Fatalf("Repair(%q) = %q", in, got)
		}
		f, _ := Fields(got)
		if f["type"] != "Guide" || f["title"] != "Grounded claims" || f[GeneratedField] != true {
			t.Errorf("Repair(%q) fields = %v", in, f)
		}
	}
}

func TestDeriveTitle(t *testing.T) {
	cases := map[[2]string]string{
		{"intro\n#  Heading One  \n", "x.md"}:   "Heading One",
		{"##  not h1\n", "a/b/my_page-name.MD"}: "My page name",
		{"", `win\dir\file.txt`}:                "File.txt",
		{"", "---.md"}:                          "---",
	}
	for in, want := range cases {
		if got := DeriveTitle(in[0], in[1]); got != want {
			t.Errorf("DeriveTitle(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
