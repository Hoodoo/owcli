package claims

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owcli/internal/evidence"
	"owcli/internal/ignore"
	"owcli/internal/store"
)

// TestUpstreamClaimsCompat loads every sidecar of an upstream wiki with the
// strict validator and runs preflight against the commit the claims were
// verified at. Pages verified at that commit must have no issues.
// See the evidence package for the environment variables.
func TestUpstreamClaimsCompat(t *testing.T) {
	dir := os.Getenv("OWCLI_UPSTREAM_DIR")
	src := os.Getenv("OWCLI_UPSTREAM_SOURCE_DIR")
	if dir == "" || src == "" {
		t.Skip("OWCLI_UPSTREAM_DIR and OWCLI_UPSTREAM_SOURCE_DIR not set")
	}
	dir, _ = filepath.Abs(dir)
	src, _ = filepath.Abs(src)
	st := NewStore(store.Layout{WikiRoot: filepath.Join(dir, "openwiki")})
	r, err := evidence.NewRepoResolver(src, ignore.Parse(""))
	if err != nil {
		t.Fatal(err)
	}
	pf, err := RunPreflight(st, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Persisted) == 0 || len(pf.Orphans) != 0 {
		t.Fatalf("persisted=%d orphans=%v", len(pf.Persisted), pf.Orphans)
	}

	head, err := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Pages map[string]struct {
			GitHead string `json:"gitHead"`
		} `json:"pages"`
	}
	data, _ := os.ReadFile(filepath.Join(dir, "openwiki", ".page-manifest.json"))
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	other := 0
	for _, is := range pf.Issues {
		if manifest.Pages[is.Page].GitHead == strings.TrimSpace(string(head)) {
			t.Errorf("unexpected %s issue on %s (claim %s): %v", is.Kind, is.Page, is.ClaimID, is.Resources)
		} else {
			other++
		}
	}
	t.Logf("%d sidecars loaded; %d issues, all on pages verified at other commits", len(pf.Persisted), other)

	if _, err := NewSession(r, pf.Persisted, pf.Issues, pf.Orphans, nil); err != nil {
		t.Fatalf("session over upstream state: %v", err)
	}
}
