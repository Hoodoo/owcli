package claims

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Hoodoo/owcli/internal/evidence"
	"github.com/Hoodoo/owcli/internal/okf"
)

// SourceIDPrefix marks OKF sources entries owned by the Claims projection.
// It matches upstream so the two tools reconcile each other's entries.
const SourceIDPrefix = "openwiki-source-"

// machineActor matches verification actors whose events the projection
// owns: owcli and upstream OpenWiki.
var machineActor = regexp.MustCompile(`^(openwiki|owcli)/`)

// SourceID is the deterministic id of a projected source resource.
func SourceID(resource string) string {
	sum := sha256.Sum256([]byte(resource))
	return SourceIDPrefix + hex.EncodeToString(sum[:])[:24]
}

// SyncSources projects each page's evidence into its OKF sources field:
// resources collapse to whole files, owned entries are regenerated in sorted
// order, and entries authored by others are kept first and never duplicated.
// Only grounded pages present in the wiki are touched.
func SyncSources(st *Store, resourcesByPage map[string][]string) error {
	present, err := st.DiscoverPages()
	if err != nil {
		return err
	}
	for _, page := range present {
		resources, ok := resourcesByPage[page]
		if !ok {
			continue
		}
		content, err := st.ReadMarkdown(page)
		if err != nil {
			return err
		}
		repaired, _ := okf.Repair(content, page, "")
		fields, _ := okf.Fields(repaired)
		current := sourceEntries(fields)
		next := mergeSources(current, resources)
		projected := repaired
		if !reflect.DeepEqual(current, next) {
			projected, _ = okf.Repair(okf.SetValue(repaired, "sources", orderedEntries(next, "id", "resource")), page, "")
		}
		if projected != content {
			if err := st.WriteMarkdown(page, projected); err != nil {
				return err
			}
		}
	}
	return nil
}

func sourceEntries(fields map[string]any) []map[string]any {
	list, _ := fields["sources"].([]any)
	out := []map[string]any{}
	for _, it := range list {
		m, ok := it.(map[string]any)
		if r, isStr := m["resource"].(string); ok && isStr && strings.TrimSpace(r) != "" {
			out = append(out, m)
		}
	}
	return out
}

func mergeSources(current []map[string]any, resources []string) []map[string]any {
	out := []map[string]any{}
	retained := map[string]bool{}
	for _, e := range current {
		if id, _ := e["id"].(string); strings.HasPrefix(id, SourceIDPrefix) {
			continue
		}
		out = append(out, e)
		retained[e["resource"].(string)] = true
	}
	whole := map[string]bool{}
	for _, r := range resources {
		if res, err := evidence.Parse(r); err == nil {
			whole[res.WholeFile().String()] = true
		}
	}
	ordered := sortedKeys(whole)
	// Upstream orders these with locale collation; case-insensitive then
	// byte order matches it for repository paths and avoids reordering an
	// upstream wiki's sources.
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := strings.ToLower(ordered[i]), strings.ToLower(ordered[j])
		if a != b {
			return a < b
		}
		return ordered[i] < ordered[j]
	})
	for _, r := range ordered {
		if !retained[r] {
			out = append(out, map[string]any{"id": SourceID(r), "resource": r})
		}
	}
	return out
}

// orderedEntries renders mappings with the given keys first, then the rest
// sorted, so YAML output is stable and conventional.
func orderedEntries(entries []map[string]any, first ...string) []*yaml.Node {
	out := make([]*yaml.Node, 0, len(entries))
	for _, e := range entries {
		n := &yaml.Node{Kind: yaml.MappingNode}
		var keys []string
		for _, k := range first {
			if _, ok := e[k]; ok {
				keys = append(keys, k)
			}
		}
		var rest []string
		for k := range e {
			if !contains(first, k) {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		for _, k := range append(keys, rest...) {
			var v yaml.Node
			if err := v.Encode(e[k]); err != nil {
				continue
			}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &v)
		}
		out = append(out, n)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// SyncVerification projects durable verification into each grounded page's
// OKF verified field: events by other actors are kept, machine events are
// replaced by at most one active event. A bare mapping is normalized to a
// list. Excluded pages are not touched. It returns the original content of
// every page it changed so a caller can roll back.
func SyncVerification(st *Store, active map[string]*Verification, excluded map[string]bool) (map[string]string, error) {
	originals := map[string]string{}
	pages, err := st.DiscoverPages()
	if err != nil {
		return originals, err
	}
	for _, page := range pages {
		if excluded[page] {
			continue
		}
		content, err := st.ReadMarkdown(page)
		if err != nil {
			return originals, err
		}
		repaired, _ := okf.Repair(content, page, "")
		fields, _ := okf.Fields(repaired)
		current := verificationEvents(fields)
		next := []map[string]any{}
		for _, e := range current {
			if !machineActor.MatchString(e["by"].(string)) {
				next = append(next, e)
			}
		}
		if v := active[page]; v != nil {
			next = append(next, map[string]any{"by": v.By, "at": v.At})
		}
		_, isList := fields["verified"].([]any)
		canonicalList := fields["verified"] == nil || isList
		projected := repaired
		if !reflect.DeepEqual(current, next) || !canonicalList {
			projected, _ = okf.Repair(okf.SetValue(repaired, "verified", orderedEntries(next, "by", "at")), page, "")
		}
		if projected == content {
			continue
		}
		if err := st.WriteMarkdown(page, projected); err != nil {
			return originals, err
		}
		originals[page] = content
	}
	return originals, nil
}

func verificationEvents(fields map[string]any) []map[string]any {
	var items []any
	switch v := fields["verified"].(type) {
	case nil:
	case []any:
		items = v
	default:
		items = []any{v}
	}
	out := []map[string]any{}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		by, isStr := m["by"].(string)
		if !isStr || strings.TrimSpace(by) == "" {
			continue
		}
		if at, present := m["at"]; present {
			if s, isStr := at.(string); !isStr || strings.TrimSpace(s) == "" {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// RollbackVerification restores the original content of the given pages.
func RollbackVerification(st *Store, originals map[string]string, pages []string) error {
	for _, p := range pages {
		if c, ok := originals[p]; ok {
			if err := st.WriteMarkdown(p, c); err != nil {
				return err
			}
		}
	}
	return nil
}
