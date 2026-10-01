package okf

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// GeneratedEvent is the OKF generated {by, at} event.
type GeneratedEvent struct {
	By string `json:"by"`
	At string `json:"at,omitempty"`
}

// ProvenanceEntry is one page's pre-authoring state. The JSON shape matches
// what run checkpoints persist.
type ProvenanceEntry struct {
	Page      string          `json:"page"`
	BodyHash  string          `json:"bodyHash"`
	Generated *GeneratedEvent `json:"generated,omitempty"`
}

// ProvenanceSnapshot records every concept's body hash and prior generated
// event before authoring, sorted by page.
type ProvenanceSnapshot []ProvenanceEntry

func (s ProvenanceSnapshot) lookup(page string) (ProvenanceEntry, bool) {
	i := sort.Search(len(s), func(i int) bool { return s[i].Page >= page })
	if i < len(s) && s[i].Page == page {
		return s[i], true
	}
	return ProvenanceEntry{}, false
}

// SnapshotProvenance captures the provenance baseline of every concept.
func (w Wiki) SnapshotProvenance() (ProvenanceSnapshot, error) {
	pages, err := w.ConceptPages()
	if err != nil {
		return nil, err
	}
	out := ProvenanceSnapshot{}
	for _, p := range pages {
		content, err := w.Read(p)
		if err != nil {
			return nil, err
		}
		out = append(out, ProvenanceEntry{Page: p, BodyHash: bodyHash(content), Generated: readGenerated(content)})
	}
	return out, nil
}

// FinalizeProvenance stamps generated on pages whose body is new or changed
// since the snapshot (with producerByPage overriding producer per page), and
// restores the prior event on unchanged bodies, so rewrites that only touch
// front matter never advance it. Unreadable or unwritable pages are skipped:
// provenance is best-effort metadata.
func (w Wiki) FinalizeProvenance(snap ProvenanceSnapshot, now, producer string, producerByPage map[string]string) error {
	if strings.TrimSpace(producer) == "" {
		return errors.New("generated provenance requires a non-empty producer actor")
	}
	for p, actor := range producerByPage {
		if strings.TrimSpace(actor) == "" {
			return errors.New("generated provenance requires a non-empty producer actor for " + p)
		}
	}
	pages, err := w.ConceptPages()
	if err != nil {
		return err
	}
	for _, p := range pages {
		content, err := w.Read(p)
		if err != nil {
			continue
		}
		prior, known := snap.lookup(p)
		var candidate string
		if !known || prior.BodyHash != bodyHash(content) {
			actor := producer
			if a, ok := producerByPage[p]; ok {
				actor = a
			}
			candidate = RemoveField(SetGenerated(content, actor, now), "timestamp")
			candidate = strings.TrimRight(candidate, "\r\n") + "\n"
		} else {
			candidate = restoreGenerated(content, prior.Generated)
		}
		if out, _ := Repair(candidate, p, ""); out != content {
			_ = w.Write(p, out)
		}
	}
	return nil
}

func bodyHash(content string) string {
	sum := sha256.Sum256([]byte(Body(content)))
	return hex.EncodeToString(sum[:])
}

func readGenerated(content string) *GeneratedEvent {
	block, _, ok := Split(content)
	if !ok {
		return nil
	}
	m, perr := parseBlock(block)
	if perr != nil || !m.has("generated") {
		return nil
	}
	n := m.values["generated"]
	if n.Kind != yaml.MappingNode {
		return nil
	}
	by, ok := mappingField(n, "by")
	if !ok || !nonEmptyString(by) {
		return nil
	}
	ev := &GeneratedEvent{By: by.Value}
	if at, ok := mappingField(n, "at"); ok {
		if !nonEmptyString(at) {
			return nil
		}
		ev.At = at.Value
	}
	return ev
}

func restoreGenerated(content string, prior *GeneratedEvent) string {
	if prior == nil {
		return RemoveField(content, "generated")
	}
	if cur := readGenerated(content); cur != nil && *cur == *prior {
		return content
	}
	return SetGenerated(content, prior.By, prior.At)
}
