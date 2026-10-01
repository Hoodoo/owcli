package store

import "fmt"

const manifestSchemaVersion = 1

// ManifestEntry records the run that last completed a page and the source
// checkpoint it was generated against.
type ManifestEntry struct {
	PageVersion       string `json:"pageVersion"`
	CompletedBy       string `json:"completedBy,omitempty"`
	CompletedRunID    string `json:"completedRunId,omitempty"`
	GitHead           string `json:"gitHead,omitempty"`
	SourceFingerprint string `json:"sourceFingerprint,omitempty"`
}

// Manifest maps canonical page identifiers to completion records.
type Manifest struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Pages         map[string]ManifestEntry `json:"pages"`
}

// NewManifest returns an empty manifest.
func NewManifest() Manifest {
	return Manifest{SchemaVersion: manifestSchemaVersion, Pages: map[string]ManifestEntry{}}
}

// LoadManifest reads the page manifest; a missing file is an empty manifest.
func (l Layout) LoadManifest() (Manifest, error) {
	path := l.ManifestPath()
	m := NewManifest()
	found, err := ReadJSON(path, &m)
	if err != nil || !found {
		return m, err
	}
	if m.SchemaVersion != manifestSchemaVersion {
		return Manifest{}, invalid(path, "unsupported schemaVersion %d", m.SchemaVersion)
	}
	if m.Pages == nil {
		m.Pages = map[string]ManifestEntry{}
	}
	for id, e := range m.Pages {
		if _, ok := PageRel(id); !ok {
			return Manifest{}, invalid(path, "invalid page %q", id)
		}
		if e.PageVersion == "" {
			return Manifest{}, invalid(path, "page %q has no pageVersion", id)
		}
	}
	return m, nil
}

// SaveManifest atomically writes the page manifest.
func (l Layout) SaveManifest(m Manifest) error {
	m.SchemaVersion = manifestSchemaVersion
	if m.Pages == nil {
		m.Pages = map[string]ManifestEntry{}
	}
	return WriteJSONAtomic(l.ManifestPath(), m)
}

// Run outcomes recorded in LastUpdate.Status.
const (
	StatusComplete    = "complete"
	StatusInterrupted = "interrupted"
)

// LastUpdate describes the most recent init or update run.
type LastUpdate struct {
	UpdatedAt string `json:"updatedAt"`
	Command   string `json:"command"` // "init" or "update"
	GitHead   string `json:"gitHead,omitempty"`
	Model     string `json:"model"`
	Status    string `json:"status"`
	Language  string `json:"language,omitempty"`
}

// LoadLastUpdate reads last-run metadata. It returns nil when no run has
// finished yet. A record without a status predates that field and counts as
// complete.
func (l Layout) LoadLastUpdate() (*LastUpdate, error) {
	path := l.LastUpdatePath()
	var u LastUpdate
	found, err := ReadJSON(path, &u)
	if err != nil || !found {
		return nil, err
	}
	if u.Status == "" {
		u.Status = StatusComplete
	}
	if err := u.validate(); err != nil {
		return nil, invalid(path, "%v", err)
	}
	return &u, nil
}

// SaveLastUpdate atomically writes last-run metadata.
func (l Layout) SaveLastUpdate(u LastUpdate) error {
	if err := u.validate(); err != nil {
		return err
	}
	return WriteJSONAtomic(l.LastUpdatePath(), u)
}

func (u LastUpdate) validate() error {
	switch {
	case u.UpdatedAt == "":
		return fmt.Errorf("updatedAt is required")
	case u.Command != "init" && u.Command != "update":
		return fmt.Errorf("command must be init or update, got %q", u.Command)
	case u.Model == "":
		return fmt.Errorf("model is required")
	case u.Status != StatusComplete && u.Status != StatusInterrupted:
		return fmt.Errorf("status must be complete or interrupted, got %q", u.Status)
	}
	return nil
}
