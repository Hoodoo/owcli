package evidence

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpstreamSidecarCompat resolves every evidence entry recorded in an
// upstream wiki's .claims sidecars and expects identical version tokens.
//
//	OWCLI_UPSTREAM_DIR         checkout whose openwiki/.claims to read
//	OWCLI_UPSTREAM_SOURCE_DIR  checkout of the commit those claims were made at
//	                           (a page manifest gitHead); defaults to the former
//
// Only pages whose manifest gitHead equals the source checkout's HEAD are
// compared, since other pages were verified against different content.
func TestUpstreamSidecarCompat(t *testing.T) {
	dir := os.Getenv("OWCLI_UPSTREAM_DIR")
	if dir == "" {
		t.Skip("OWCLI_UPSTREAM_DIR not set")
	}
	src := os.Getenv("OWCLI_UPSTREAM_SOURCE_DIR")
	if src == "" {
		src = dir
	}
	src, _ = filepath.Abs(src)
	r, err := NewRepoResolver(src, nil)
	if err != nil {
		t.Fatal(err)
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
	data, err := os.ReadFile(filepath.Join(dir, "openwiki", ".page-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}

	var total, same, retained, files, ranges, skipped int
	claims := filepath.Join(dir, "openwiki", ".claims")
	err = filepath.WalkDir(claims, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(claims, p)
		page := "/openwiki/" + strings.TrimSuffix(filepath.ToSlash(rel), ".json") + ".md"
		if manifest.Pages[page].GitHead != strings.TrimSpace(string(head)) {
			skipped++ // claims were verified against another commit
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var sidecar struct {
			Claims []struct {
				Evidence []Evidence `json:"evidence"`
			} `json:"claims"`
		}
		if err := json.Unmarshal(data, &sidecar); err != nil {
			return err
		}
		for _, c := range sidecar.Claims {
			for _, e := range c.Evidence {
				total++
				if strings.HasPrefix(e.Version, FileVersionPrefix) {
					files++
				} else {
					ranges++
				}
				canon, err := Canonical(e.Resource)
				if err != nil || canon != e.Resource {
					t.Errorf("%s: resource %q not canonical (%q, %v)", p, e.Resource, canon, err)
				}
				// Fresh resolution must reproduce the stored token exactly.
				res, err := r.Resolve(e.Resource, "")
				if err != nil {
					t.Errorf("%s: %v", e.Resource, err)
					continue
				}
				if res != nil && res.Evidence.Version == e.Version && res.Evidence.Resource == e.Resource {
					same++
					continue
				}
				// A retained claim keeps the token minted when it was first
				// cited; relocation against that token must reproduce it.
				res, err = r.Resolve(e.Resource, e.Version)
				if err == nil && res != nil && res.Evidence.Version == e.Version && res.Evidence.Resource == e.Resource {
					retained++
					continue
				}
				got := "<unresolved>"
				if res != nil {
					got = res.Evidence.Resource
				}
				t.Errorf("%s: token not reproduced (resolved to %s)", e.Resource, got)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d evidence entries: %d identical when freshly minted, %d reproduced from retained tokens (%d whole-file, %d line-range; %d sidecars from other commits skipped)",
		total, same, retained, files, ranges, skipped)
	if total == 0 {
		t.Error("no evidence compared")
	}
}
