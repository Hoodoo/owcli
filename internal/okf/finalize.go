package okf

// FinalizeOptions configures the post-authoring passes.
type FinalizeOptions struct {
	Provenance     ProvenanceSnapshot // taken before authoring
	Now            string             // OKF timestamp for new generated stamps
	Producer       string             // default generated actor
	ProducerByPage map[string]string  // per-page actor overrides
	ConceptType    string             // fallback type for repaired pages
	// ClaimSources, when set, projects Claims evidence into sources. It runs
	// after links and before provenance (the claims package supplies it).
	ClaimSources func() error
}

// FinalizeReport collects the informational reports of the passes.
type FinalizeReport struct {
	Mermaid  MermaidReport
	Metadata MetadataReport
	Links    LinkReport
}

// Finalize runs the deterministic post-authoring passes in their fixed order:
// mermaid validation, index synchronization, link validation, claim sources,
// and generated provenance. Index sync follows mermaid so it sees final
// front matter; links follow indexes so they see final hrefs; sources and
// provenance read bodies last, after all other body edits have settled.
func (w Wiki) Finalize(opts FinalizeOptions) (FinalizeReport, error) {
	var r FinalizeReport
	var err error
	if r.Mermaid, err = w.ValidateMermaid(); err != nil {
		return r, err
	}
	if r.Metadata, err = w.SyncIndexes(opts.ConceptType); err != nil {
		return r, err
	}
	if r.Links, err = w.ValidateLinks(); err != nil {
		return r, err
	}
	if opts.ClaimSources != nil {
		if err := opts.ClaimSources(); err != nil {
			return r, err
		}
	}
	return r, w.FinalizeProvenance(opts.Provenance, opts.Now, opts.Producer, opts.ProducerByPage)
}
