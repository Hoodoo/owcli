package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hoodoo/owcli/internal/store"
)

// workspaceResult is what a workspace subcommand produced: a JSON value and
// its plain-text rendering.
type workspaceResult struct {
	value any
	text  string
}

// workspaceCmd runs fn and prints its result as text, or as JSON with
// --json. With --json, errors print as {"error":{"code","message"}};
// a wrong request (unknown workspace, not a member, ...) is invalid_input.
func workspaceCmd(asJSON *bool, fn func(dirs store.Dirs, args []string) (workspaceResult, error)) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		dirs, err := store.DefaultDirs()
		var res workspaceResult
		if err == nil {
			res, err = fn(dirs, args)
		}
		out := cmd.OutOrStdout()
		if err != nil {
			if !*asJSON {
				return err
			}
			code := "error"
			switch {
			case errors.Is(err, store.ErrWorkspace):
				code = "invalid_input"
			case errors.Is(err, store.ErrUnbound):
				code = "not_found"
			}
			_ = writeJSON(out, map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
			return ErrReported
		}
		if *asJSON {
			return writeJSON(out, res.value)
		}
		_, err = fmt.Fprint(out, res.text)
		return err
	}
}

func newWorkspaceCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Group repository wikis into named workspaces for shared search",
		Long: `A workspace is a named set of repository wikis. Searching from a member
repository covers every wiki in its workspace, and each result names the wiki
to pass to "owcli read --wiki". A repository can be in several workspaces;
choose the one its searches use with "owcli workspace use". Members need not
have a wiki of their own to search their workspace.

The registry is $XDG_CONFIG_HOME/owcli/workspaces.json. Repositories are
given as paths (default: the current directory) and resolved to their Git
roots; their wikis are found through owcli's bindings, in-repo or external.

  owcli workspace create <name> [repo...]
  owcli workspace add <workspace> <repo...>
  owcli workspace remove <workspace> <repo...>
  owcli workspace delete <workspace>
  owcli workspace list
  owcli workspace wikis <workspace>
  owcli workspace use <workspace>
  owcli workspace current
  owcli workspace clear

Workspaces are named by ID or by name, ignoring case.`,
	}
	cmd.PersistentFlags().BoolVar(&asJSON, "json", false, "print JSON")
	sub := func(use, short string, args cobra.PositionalArgs, fn func(store.Dirs, []string) (workspaceResult, error)) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, Args: args, RunE: workspaceCmd(&asJSON, fn)}
	}
	cmd.AddCommand(
		sub("create <name> [repo...]", "Create a workspace, optionally with member repositories", cobra.MinimumNArgs(1), workspaceCreate),
		sub("add <workspace> <repo...>", "Add repositories to a workspace", cobra.MinimumNArgs(2), workspaceAdd),
		sub("remove <workspace> <repo...>", "Remove repositories from a workspace", cobra.MinimumNArgs(2), workspaceRemove),
		sub("delete <workspace>", "Delete a workspace (member wikis are untouched)", cobra.ExactArgs(1), workspaceDelete),
		sub("list", "List all workspaces and their members", cobra.NoArgs, workspaceList),
		sub("wikis <workspace>", "Show a workspace's wikis and whether each can be searched", cobra.ExactArgs(1), workspaceWikis),
		sub("use <workspace>", "Make a workspace the one this repository searches", cobra.ExactArgs(1), workspaceUse),
		sub("current", "Show this repository's workspaces and its active one", cobra.NoArgs, workspaceCurrent),
		sub("clear", "Forget this repository's active workspace", cobra.NoArgs, workspaceClear),
	)
	return cmd
}

// repoRoots resolves paths to canonical Git roots.
func repoRoots(paths []string) ([]string, error) {
	var roots []string
	for _, p := range paths {
		r, err := store.RepoRoot(p)
		if err != nil {
			return nil, err
		}
		roots = append(roots, r)
	}
	return roots, nil
}

// draftIndex finds the draft of the workspace ref names.
func draftIndex(reg store.WorkspaceRegistry, drafts []store.WorkspaceDraft, ref string) (int, error) {
	ws, err := reg.FindWorkspace(ref)
	if err != nil {
		return 0, err
	}
	for i, d := range drafts {
		if d.ID == ws.ID {
			return i, nil
		}
	}
	return 0, fmt.Errorf("workspace %s vanished from the registry", ws.ID)
}

// saveAndShow saves drafts and renders the workspace with the given name.
func saveAndShow(dirs store.Dirs, drafts []store.WorkspaceDraft, name, verb string) (workspaceResult, error) {
	reg, err := dirs.SaveWorkspaces(drafts)
	if err != nil {
		return workspaceResult{}, err
	}
	ws, err := reg.FindWorkspace(name)
	if err != nil {
		return workspaceResult{}, err
	}
	res, err := showWorkspace(dirs, reg, ws)
	if err != nil {
		return workspaceResult{}, err
	}
	res.text = verb + " " + res.text
	return res, nil
}

func workspaceCreate(dirs store.Dirs, args []string) (workspaceResult, error) {
	reg, err := dirs.LoadWorkspaces()
	if err != nil {
		return workspaceResult{}, err
	}
	name := strings.TrimSpace(args[0])
	if ws, err := reg.FindWorkspace(name); err == nil {
		return workspaceResult{}, fmt.Errorf("%w: workspace %s (%s) already exists; use `owcli workspace add`", store.ErrWorkspace, ws.Name, ws.ID)
	}
	roots, err := repoRoots(args[1:])
	if err != nil {
		return workspaceResult{}, err
	}
	drafts := append(reg.WorkspaceDrafts(), store.WorkspaceDraft{Name: name, Roots: roots})
	return saveAndShow(dirs, drafts, name, "created")
}

func workspaceAdd(dirs store.Dirs, args []string) (workspaceResult, error) {
	reg, err := dirs.LoadWorkspaces()
	if err != nil {
		return workspaceResult{}, err
	}
	drafts := reg.WorkspaceDrafts()
	i, err := draftIndex(reg, drafts, args[0])
	if err != nil {
		return workspaceResult{}, err
	}
	roots, err := repoRoots(args[1:])
	if err != nil {
		return workspaceResult{}, err
	}
	drafts[i].Roots = append(drafts[i].Roots, roots...) // duplicates collapse on save
	return saveAndShow(dirs, drafts, drafts[i].ID, "updated")
}

func workspaceRemove(dirs store.Dirs, args []string) (workspaceResult, error) {
	reg, err := dirs.LoadWorkspaces()
	if err != nil {
		return workspaceResult{}, err
	}
	drafts := reg.WorkspaceDrafts()
	i, err := draftIndex(reg, drafts, args[0])
	if err != nil {
		return workspaceResult{}, err
	}
	drop := map[string]bool{}
	for _, p := range args[1:] {
		// A member whose repository is gone can still be removed by its path.
		root, err := store.RepoRoot(p)
		if err != nil {
			if root, err = filepath.Abs(p); err != nil {
				return workspaceResult{}, err
			}
		}
		drop[root] = true
	}
	var kept []string
	for _, r := range drafts[i].Roots {
		if drop[r] {
			delete(drop, r)
			continue
		}
		kept = append(kept, r)
	}
	if len(drop) > 0 {
		var missing []string
		for r := range drop {
			missing = append(missing, r)
		}
		sort.Strings(missing)
		return workspaceResult{}, fmt.Errorf("%w: not a member of workspace %s: %s", store.ErrWorkspace, drafts[i].Name, strings.Join(missing, ", "))
	}
	drafts[i].Roots = kept
	return saveAndShow(dirs, drafts, drafts[i].ID, "updated")
}

func workspaceDelete(dirs store.Dirs, args []string) (workspaceResult, error) {
	reg, err := dirs.LoadWorkspaces()
	if err != nil {
		return workspaceResult{}, err
	}
	drafts := reg.WorkspaceDrafts()
	i, err := draftIndex(reg, drafts, args[0])
	if err != nil {
		return workspaceResult{}, err
	}
	gone := drafts[i]
	if _, err := dirs.SaveWorkspaces(append(drafts[:i], drafts[i+1:]...)); err != nil {
		return workspaceResult{}, err
	}
	return workspaceResult{
		value: map[string]any{"deleted": map[string]string{"id": gone.ID, "name": gone.Name}},
		text:  fmt.Sprintf("deleted workspace %s (%s); member wikis are untouched\n", gone.Name, gone.ID),
	}, nil
}

type memberView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Root    string `json:"root"`
	WikiDir string `json:"wikiDir,omitempty"`
	Problem string `json:"problem,omitempty"`
}

type workspaceView struct {
	store.WorkspaceSummary
	Wikis []memberView `json:"wikis"`
}

func viewOf(dirs store.Dirs, reg store.WorkspaceRegistry, ws store.Workspace) (workspaceView, error) {
	members, err := dirs.ResolveMembers(reg, ws)
	if err != nil {
		return workspaceView{}, err
	}
	v := workspaceView{WorkspaceSummary: store.WorkspaceSummary{ID: ws.ID, Name: ws.Name, WikiCount: len(ws.Wikis)}, Wikis: []memberView{}}
	for _, m := range members {
		v.Wikis = append(v.Wikis, memberView{ID: m.Wiki.ID, Name: m.Wiki.Name, Root: m.Wiki.Root, WikiDir: m.WikiDir, Problem: m.Problem})
	}
	return v, nil
}

func (v workspaceView) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "workspace %s (%s): %d wiki(s)\n", v.Name, v.ID, v.WikiCount)
	for _, m := range v.Wikis {
		status := m.WikiDir
		if m.Problem != "" {
			status = "not searchable: " + m.Problem
		}
		fmt.Fprintf(&b, "  %s  %s\n      %s\n", m.ID, m.Root, status)
	}
	return b.String()
}

func showWorkspace(dirs store.Dirs, reg store.WorkspaceRegistry, ws store.Workspace) (workspaceResult, error) {
	v, err := viewOf(dirs, reg, ws)
	if err != nil {
		return workspaceResult{}, err
	}
	return workspaceResult{value: map[string]any{"workspace": v}, text: v.text()}, nil
}

func workspaceList(dirs store.Dirs, _ []string) (workspaceResult, error) {
	reg, err := dirs.LoadWorkspaces()
	if err != nil {
		return workspaceResult{}, err
	}
	views := []workspaceView{}
	var b strings.Builder
	for _, ws := range reg.Workspaces {
		v, err := viewOf(dirs, reg, ws)
		if err != nil {
			return workspaceResult{}, err
		}
		views = append(views, v)
		b.WriteString(v.text())
	}
	if len(views) == 0 {
		b.WriteString("no workspaces; create one with `owcli workspace create <name> [repo...]`\n")
	}
	return workspaceResult{value: map[string]any{"workspaces": views}, text: b.String()}, nil
}

func workspaceWikis(dirs store.Dirs, args []string) (workspaceResult, error) {
	reg, err := dirs.LoadWorkspaces()
	if err != nil {
		return workspaceResult{}, err
	}
	ws, err := reg.FindWorkspace(args[0])
	if err != nil {
		return workspaceResult{}, err
	}
	return showWorkspace(dirs, reg, ws)
}

func workspaceUse(dirs store.Dirs, args []string) (workspaceResult, error) {
	ws, err := dirs.SetActiveWorkspace(".", args[0])
	if err != nil {
		return workspaceResult{}, err
	}
	sum := store.WorkspaceSummary{ID: ws.ID, Name: ws.Name, WikiCount: len(ws.Wikis)}
	return workspaceResult{value: map[string]any{"activeWorkspace": sum}, text: fmt.Sprintf("searches from this repository now use workspace %s (%s)\n", ws.Name, ws.ID)}, nil
}

func workspaceCurrent(dirs store.Dirs, _ []string) (workspaceResult, error) {
	root, err := store.RepoRoot(".")
	if err != nil {
		return workspaceResult{}, err
	}
	reg, err := dirs.LoadWorkspaces()
	if err != nil {
		return workspaceResult{}, err
	}
	value := map[string]any{"workspaces": []store.WorkspaceSummary{}}
	w, ok := reg.WikiByRoot(root)
	if !ok {
		value["wiki"] = store.WikiIdentity{Name: filepath.Base(root)}
		return workspaceResult{value: value, text: fmt.Sprintf("%s is in no workspace; searches cover its own wiki\n", root)}, nil
	}
	value["wiki"] = store.WikiIdentity{ID: w.ID, Name: w.Name}
	var sums []store.WorkspaceSummary
	var b strings.Builder
	active, hasActive := reg.ActiveFor(w.ID)
	fmt.Fprintf(&b, "%s is wiki %s in:\n", root, w.ID)
	for _, ws := range reg.Workspaces {
		for _, id := range ws.Wikis {
			if id != w.ID {
				continue
			}
			sums = append(sums, store.WorkspaceSummary{ID: ws.ID, Name: ws.Name, WikiCount: len(ws.Wikis)})
			mark := " "
			if hasActive && ws.ID == active {
				mark = "*"
			}
			fmt.Fprintf(&b, " %s %s  %s (%d wikis)\n", mark, ws.ID, ws.Name, len(ws.Wikis))
		}
	}
	value["workspaces"] = sums
	if hasActive {
		value["activeWorkspace"] = active
		b.WriteString("searches use the workspace marked *\n")
	} else if len(sums) > 1 {
		b.WriteString("no active workspace: searches ask for one; set it with `owcli workspace use <workspace>`\n")
	}
	return workspaceResult{value: value, text: b.String()}, nil
}

func workspaceClear(dirs store.Dirs, _ []string) (workspaceResult, error) {
	removed, err := dirs.ClearActiveWorkspace(".")
	if err != nil {
		return workspaceResult{}, err
	}
	text := "no active workspace was set\n"
	if removed {
		text = "active workspace cleared\n"
	}
	return workspaceResult{value: map[string]any{"cleared": removed}, text: text}, nil
}
