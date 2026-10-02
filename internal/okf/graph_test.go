package okf

import (
	"strings"
	"testing"
)

func TestPageLinks(t *testing.T) {
	known := map[string]bool{"/openwiki/quickstart.md": true, "/openwiki/concepts/a.md": true, "/openwiki/concepts/b.md": true}
	content := strings.Join([]string{
		"See [A](concepts/a.md#intro), [A again](concepts/a.md), and [B](./concepts/b.md \"title\").",
		"![diagram](concepts/b.md) [web](https://example.com) [self](quickstart.md) [dir](concepts/)",
		"[missing](concepts/c.md) [source](../internal/x.go) [anchor](#top) [abs](/openwiki/concepts/a.md)",
		"<!-- openwiki: broken internal link -->",
	}, "\n")
	got := strings.Join(PageLinks("/openwiki/quickstart.md", content, known), " ")
	if got != "/openwiki/concepts/a.md /openwiki/concepts/b.md" {
		t.Fatalf("links = %s", got)
	}
	if got := PageLinks("/openwiki/concepts/a.md", "[up](../quickstart.md) [sib](b.md)", known); strings.Join(got, " ") != "/openwiki/quickstart.md /openwiki/concepts/b.md" {
		t.Fatalf("relative links = %v", got)
	}
}
