package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/Hoodoo/owcli/internal/llm"
)

// Tool is something the model can call.
type Tool interface {
	Def() llm.ToolDef
	Call(ctx context.Context, input json.RawMessage) (Result, error)
}

// Result is a tool's answer. Stop ends the loop after this batch of calls.
type Result struct {
	Text string
	Stop bool
}

// funcTool adapts a typed function to Tool.
type funcTool[T any] struct {
	def llm.ToolDef
	fn  func(ctx context.Context, in T) (Result, error)
}

func (f funcTool[T]) Def() llm.ToolDef { return f.def }

func (f funcTool[T]) Call(ctx context.Context, raw json.RawMessage) (Result, error) {
	var in T
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{}, fmt.Errorf("invalid input for %s: %v", f.def.Name, err)
	}
	return f.fn(ctx, in)
}

// NewTool builds a Tool from a name, description, JSON schema, and handler.
func NewTool[T any](name, description, schema string, fn func(ctx context.Context, in T) (Result, error)) Tool {
	return funcTool[T]{def: llm.ToolDef{Name: name, Description: description, Schema: json.RawMessage(schema)}, fn: fn}
}

// Output limits keep tool results within a sensible context budget.
const (
	defaultReadLines = 2000
	maxLineChars     = 2000
	maxGlobResults   = 500
	maxGrepMatches   = 200
	maxListEntries   = 1000
	maxGitLogEntries = 50
)

// ReadTools returns the read-only tools over a workspace.
func ReadTools(w *Workspace) []Tool {
	return []Tool{
		NewTool("ls", "List a directory. Paths are repository-relative; the wiki is under openwiki/. Use \"\" or \".\" for the repository root.",
			`{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`,
			func(_ context.Context, in struct{ Path string }) (Result, error) {
				entries, err := w.List(in.Path)
				if err != nil {
					return Result{}, err
				}
				var b strings.Builder
				for i, e := range entries {
					if i == maxListEntries {
						fmt.Fprintf(&b, "... %d more entries\n", len(entries)-i)
						break
					}
					if e.IsDir {
						fmt.Fprintf(&b, "%s/\n", e.Path)
					} else {
						fmt.Fprintf(&b, "%s (%d bytes)\n", e.Path, e.Size)
					}
				}
				if b.Len() == 0 {
					return Result{Text: "(empty directory)"}, nil
				}
				return Result{Text: b.String()}, nil
			}),
		NewTool("glob", "Find files by glob pattern, e.g. \"**/*.go\" or \"src/**/test_*.py\". * stays within a path segment; ** spans segments.",
			`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string","description":"directory to search (default: repository root)"}},"required":["pattern"],"additionalProperties":false}`,
			func(_ context.Context, in struct{ Pattern, Path string }) (Result, error) {
				re, err := globRegexp(in.Pattern)
				if err != nil {
					return Result{}, err
				}
				base := strings.Trim(in.Path, "/")
				var out []string
				err = w.Walk(in.Path, func(e Entry) error {
					rel := strings.TrimPrefix(strings.TrimPrefix(e.Path, base), "/")
					if re.MatchString(rel) || re.MatchString(e.Path) {
						out = append(out, e.Path)
					}
					return nil
				})
				if err != nil {
					return Result{}, err
				}
				return Result{Text: limitLines(out, maxGlobResults, "no files match")}, nil
			}),
		NewTool("grep", "Search file contents with an RE2 regular expression. Returns path:line: text.",
			`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string","description":"directory or file to search (default: repository root)"},"glob":{"type":"string","description":"only files matching this glob"},"ignore_case":{"type":"boolean"}},"required":["pattern"],"additionalProperties":false}`,
			func(_ context.Context, in struct {
				Pattern, Path, Glob string
				IgnoreCase          bool `json:"ignore_case"`
			}) (Result, error) {
				expr := in.Pattern
				if in.IgnoreCase {
					expr = "(?i)" + expr
				}
				re, err := regexp.Compile(expr)
				if err != nil {
					return Result{}, fmt.Errorf("invalid pattern: %v", err)
				}
				var fileRe *regexp.Regexp
				if in.Glob != "" {
					if fileRe, err = globRegexp(in.Glob); err != nil {
						return Result{}, err
					}
				}
				var out []string
				search := func(p string) {
					text, err := w.ReadFile(p)
					if err != nil {
						return
					}
					for i, line := range strings.Split(text, "\n") {
						if len(out) > maxGrepMatches {
							return
						}
						if re.MatchString(line) {
							out = append(out, fmt.Sprintf("%s:%d: %s", p, i+1, clip(line)))
						}
					}
				}
				if t, err := w.resolve(in.Path, false); err == nil && in.Path != "" && !isDirTarget(t) {
					search(in.Path)
				} else {
					err := w.Walk(in.Path, func(e Entry) error {
						if len(out) > maxGrepMatches {
							return nil
						}
						if fileRe != nil && !fileRe.MatchString(e.Path) && !fileRe.MatchString(baseName(e.Path)) {
							return nil
						}
						if e.Size <= maxFileBytes {
							search(e.Path)
						}
						return nil
					})
					if err != nil {
						return Result{}, err
					}
				}
				return Result{Text: limitLines(out, maxGrepMatches, "no matches")}, nil
			}),
		NewTool("read_file", "Read a text file with line numbers. Use offset (1-based line) and limit for long files.",
			`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`,
			func(_ context.Context, in struct {
				Path          string
				Offset, Limit int
			}) (Result, error) {
				text, err := w.ReadFile(in.Path)
				if err != nil {
					return Result{}, err
				}
				return Result{Text: numbered(text, in.Offset, in.Limit)}, nil
			}),
		NewTool("git_log", "Show recent commits (one line each), optionally only those touching a path.",
			`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":50}},"additionalProperties":false}`,
			func(ctx context.Context, in struct {
				Path  string
				Limit int
			}) (Result, error) {
				n := in.Limit
				if n <= 0 || n > maxGitLogEntries {
					n = 20
				}
				args := []string{"-C", w.Repo, "log", "--no-color", "--date=short", "--format=%h %ad %s", "-n", strconv.Itoa(n)}
				if in.Path != "" {
					t, err := w.resolve(in.Path, false)
					if err != nil || t.inWiki {
						return Result{}, deniedf("git_log path must be a repository path")
					}
					args = append(args, "--", t.virtual)
				}
				out, err := exec.CommandContext(ctx, "git", args...).Output()
				if err != nil {
					return Result{}, fmt.Errorf("git log failed: %v", err)
				}
				if len(out) == 0 {
					return Result{Text: "no commits"}, nil
				}
				return Result{Text: string(out)}, nil
			}),
	}
}

// WriteTools returns tools that edit the workspace's writable pages.
func WriteTools(w *Workspace) []Tool {
	return []Tool{
		NewTool("write_file", "Create or replace your assigned wiki page with complete Markdown content.",
			`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`,
			func(_ context.Context, in struct{ Path, Content string }) (Result, error) {
				if err := w.WriteFile(in.Path, in.Content); err != nil {
					return Result{}, err
				}
				return Result{Text: fmt.Sprintf("wrote %s (%d bytes)", in.Path, len(in.Content))}, nil
			}),
		NewTool("edit_file", "Replace an exact string in your assigned wiki page. old_string must match exactly once unless replace_all is true.",
			`{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["path","old_string","new_string"],"additionalProperties":false}`,
			func(_ context.Context, in struct {
				Path       string
				Old        string `json:"old_string"`
				New        string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			}) (Result, error) {
				text, err := w.ReadFile(in.Path)
				if err != nil {
					return Result{}, err
				}
				n := strings.Count(text, in.Old)
				switch {
				case in.Old == "":
					return Result{}, fmt.Errorf("old_string must not be empty")
				case n == 0:
					return Result{}, fmt.Errorf("old_string not found in %s", in.Path)
				case n > 1 && !in.ReplaceAll:
					return Result{}, fmt.Errorf("old_string occurs %d times in %s; add context or set replace_all", n, in.Path)
				}
				if in.ReplaceAll {
					text = strings.ReplaceAll(text, in.Old, in.New)
				} else {
					text = strings.Replace(text, in.Old, in.New, 1)
				}
				if err := w.WriteFile(in.Path, text); err != nil {
					return Result{}, err
				}
				return Result{Text: fmt.Sprintf("edited %s (%d replacement(s))", in.Path, map[bool]int{true: n, false: 1}[in.ReplaceAll])}, nil
			}),
	}
}

func isDirTarget(t target) bool {
	fi, err := os.Stat(t.abs)
	return err == nil && fi.IsDir()
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func clip(line string) string {
	line = strings.TrimRight(line, "\r")
	if len(line) > maxLineChars {
		return line[:maxLineChars] + "…"
	}
	return line
}

func numbered(text string, offset, limit int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = defaultReadLines
	}
	if offset > len(lines) {
		return fmt.Sprintf("(file has %d lines)", len(lines))
	}
	end := min(len(lines), offset-1+limit)
	var b strings.Builder
	for i := offset - 1; i < end; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, clip(lines[i]))
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "... %d more lines; continue with offset %d\n", len(lines)-end, end+1)
	}
	return b.String()
}

func limitLines(lines []string, max int, empty string) string {
	if len(lines) == 0 {
		return empty
	}
	if len(lines) > max {
		return strings.Join(lines[:max], "\n") + "\n... more results truncated; narrow the search"
	}
	return strings.Join(lines, "\n")
}

// globRegexp compiles a path glob: * within a segment, ** across segments,
// ? one character, {a,b} alternatives.
func globRegexp(g string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	depth := 0
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c == '*' && i+1 < len(g) && g[i+1] == '*':
			if i+2 < len(g) && g[i+2] == '/' {
				b.WriteString("(?:.*/)?")
				i += 2
			} else {
				b.WriteString(".*")
				i++
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '{':
			depth++
			b.WriteString("(?:")
		case c == '}' && depth > 0:
			depth--
			b.WriteString(")")
		case c == ',' && depth > 0:
			b.WriteString("|")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("invalid glob %q: %v", g, err)
	}
	return re, nil
}
