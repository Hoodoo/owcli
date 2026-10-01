package okf

import (
	"path"
	"sort"
	"strings"
)

// Index headings. owcli writes English navigation chrome only.
const (
	filesHeading       = "Files"
	directoriesHeading = "Directories"
)

// MetadataReport lists pages whose metadata the deterministic passes leave
// as-is by design. It is informational and never fails a run.
type MetadataReport struct {
	GeneratedPages          []string // carry openwiki_generated: true
	MissingDescriptionPages []string // indexed without a usable description
}

type indexLink struct {
	href, label, description string
}

// SyncIndexes writes an index.md for every visible directory, listing
// concept files (title and description from front matter) and
// subdirectories, sorted by href. Each concept is normalized on the way. An
// index is rewritten only when its content changes; the root index carries
// okf_version "0.2".
func (w Wiki) SyncIndexes(conceptType string) (MetadataReport, error) {
	var report MetadataReport
	dirs, err := w.dirs()
	if err != nil {
		return report, err
	}
	if len(dirs) == 0 {
		return report, nil
	}
	root := dirs[len(dirs)-1].id // dirs lists deepest first; the root is last
	for _, d := range dirs {
		var files, subdirs []indexLink
		for _, name := range d.subdirs {
			subdirs = append(subdirs, indexLink{href: encodeComponent(name) + "/", label: name})
		}
		for _, name := range d.files {
			if !isConceptName(name) {
				continue
			}
			id := d.id + "/" + name
			content, err := w.normalize(id, conceptType)
			if err != nil {
				return report, err
			}
			fields, _ := Fields(content)
			title, ok := StringField(fields, "title")
			if !ok {
				title = strings.TrimSuffix(name, path.Ext(name))
			}
			desc, hasDesc := StringField(fields, "description")
			files = append(files, indexLink{href: encodeComponent(name), label: title, description: desc})
			rel := strings.TrimPrefix(id, root+"/")
			if fields[GeneratedField] == true {
				report.GeneratedPages = append(report.GeneratedPages, rel)
			}
			if !hasDesc {
				report.MissingDescriptionPages = append(report.MissingDescriptionPages, rel)
			}
		}
		rendered := renderIndex(files, subdirs, d.id == root)
		indexID := d.id + "/index.md"
		if existing, err := w.Read(indexID); err == nil && existing == rendered {
			continue
		}
		if err := w.Write(indexID, rendered); err != nil {
			return report, err
		}
	}
	sort.Strings(report.GeneratedPages)
	sort.Strings(report.MissingDescriptionPages)
	return report, nil
}

func renderIndex(files, subdirs []indexLink, isRoot bool) string {
	var sections []string
	if s := renderLinks(filesHeading, files, true); s != "" {
		sections = append(sections, s)
	}
	if s := renderLinks(directoriesHeading, subdirs, false); s != "" {
		sections = append(sections, s)
	}
	body := strings.Join(sections, "\n\n")
	if body == "" {
		body = "# " + filesHeading
	}
	if isRoot {
		return "---\nokf_version: \"0.2\"\n---\n\n" + body + "\n"
	}
	return body + "\n"
}

func renderLinks(heading string, links []indexLink, withDescription bool) string {
	if len(links) == 0 {
		return ""
	}
	sort.SliceStable(links, func(i, j int) bool { return collate(links[i].href, links[j].href) })
	items := make([]string, len(links))
	for i, l := range links {
		item := "- [" + escapeLabel(l.label) + "](" + l.href + ")"
		if withDescription && l.description != "" {
			item += " - " + l.description
		}
		items[i] = item
	}
	return "# " + heading + "\n\n" + strings.Join(items, "\n")
}

// collate orders case-insensitively, then by code units, approximating the
// locale-aware ordering upstream uses for index entries.
func collate(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`).Replace(s)
}

// encodeComponent percent-encodes like JavaScript's encodeURIComponent.
func encodeComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(hex2(c)))
		}
	}
	return b.String()
}

func hex2(c byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[c>>4], digits[c&15]})
}
