package worktreeui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/tui"
)

func TestCreateNamingAndPath(t *testing.T) {
	for _, tc := range []struct{ branch, want string }{
		{"widget-studio/agent-admin", "widget-studio-agent-admin"},
		{"DEMO-1601-widget-preset-ui", "demo-1601-widget-preset-ui"},
		{"__A///B--", "a-b"}, {"한글", "worktree"}, {"", "worktree"},
	} {
		if got := PathSlug(tc.branch); got != tc.want {
			t.Errorf("slug %q = %q, want %q", tc.branch, got, tc.want)
		}
	}
	for _, tc := range []struct {
		base string
		used []string
		want string
	}{
		{"refs/heads/widget-studio/dev", []string{"widget-studio/dev"}, "widget-studio/dev-2"},
		{"refs/remotes/origin/main", []string{"main", "main-2"}, "main-3"},
		{"refs/heads/work-2", []string{"work-2", "work-3"}, "work-4"},
		{"feature/local", nil, "feature/local"},
		{"refs/heads/origin/main", []string{"origin/main"}, "origin/main-2"},
		{"refs/heads/work-2", nil, "work-2"},
	} {
		taken := map[string]bool{}
		for _, name := range tc.used {
			taken[name] = true
		}
		if got := AutoName(tc.base, func(name string) bool { return taken[name] }); got != tc.want {
			t.Errorf("auto %q = %q, want %q", tc.base, got, tc.want)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := PathPreview(filepath.Join(home, ".herdr", "worktrees"), "mfe", "widget-studio/dev"); got != filepath.Join("~", ".herdr", "worktrees", "mfe", "widget-studio-dev") {
		t.Fatalf("home preview: %q", got)
	}
	root := filepath.Join(home+"-other", "worktrees")
	if got := PathPreview(root, "mfe", "X"); got != filepath.Join(root, "mfe", "x") {
		t.Fatalf("home prefix boundary: %q", got)
	}
}

func createSample() CreateModel {
	return CreateModel{RepoName: "mfe", Name: "widget-studio/dev-2", NameSelected: true, PathRoot: "/worktrees", Taken: map[string]bool{"widget-studio/dev": true, "main": true}, Candidates: []Candidate{
		{Label: "widget-studio/dev", Ref: "refs/heads/widget-studio/dev", Kind: CandidateCurrent, Status: "↓3 behind"},
		{Label: "team/upstream/main", Ref: "refs/remotes/team/upstream/main", Kind: CandidateUpstream, Fetch: gitrepo.Upstream{Remote: "team/upstream", RemoteRef: "refs/heads/main", TrackingRef: "refs/remotes/team/upstream/main"}, Status: "fetching…"},
	}}
}

func TestUpdateCreate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		keys     []tui.Key
		wantName string
		edited   bool
		selected int
		focus    FocusArea
		action   CreateAction
	}{
		{"typing replaces selection", []tui.Key{key('x')}, "x", true, 0, FocusName, CreateNone},
		{"backspace removes selection", []tui.Key{{Kind: tui.KeyBackspace}}, "", true, 0, FocusName, CreateNone},
		{"base changes automatic name", []tui.Key{{Kind: tui.KeyTab}, {Kind: tui.KeyDown}}, "main-2", false, 1, FocusBase, CreateNone},
		{"edited name stays", []tui.Key{key('x'), {Kind: tui.KeyTab}, {Kind: tui.KeyDown}}, "x", true, 1, FocusBase, CreateNone},
		{"tab back", []tui.Key{{Kind: tui.KeyTab}, {Kind: tui.KeyTab}}, "widget-studio/dev-2", false, 0, FocusName, CreateNone},
		{"submit", []tui.Key{{Kind: tui.KeyEnter}}, "widget-studio/dev-2", false, 0, FocusName, CreateSubmit},
		{"cancel", []tui.Key{{Kind: tui.KeyEsc}}, "widget-studio/dev-2", false, 0, FocusName, CreateCancel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := createSample()
			var action CreateAction
			for _, k := range tc.keys {
				m, action = UpdateCreate(m, k)
			}
			if m.Name != tc.wantName || m.UserEdited != tc.edited || m.Selected != tc.selected || m.Focus != tc.focus || action != tc.action {
				t.Fatalf("model/action: %+v %v", m, action)
			}
		})
	}
	m := createSample()
	m.Name = ""
	if got, action := UpdateCreate(m, tui.Key{Kind: tui.KeyEnter}); action != CreateNone || got.Message == "" {
		t.Fatal("empty name must not submit")
	}
	m = createSample()
	m.Name = "가나"
	m.NameSelected = false
	if got, _ := UpdateCreate(m, tui.Key{Kind: tui.KeyBackspace}); got.Name != "가" {
		t.Fatalf("UTF-8 backspace: %q", got.Name)
	}
	m.Busy = true
	if got, action := UpdateCreate(m, key('x')); got.Name != m.Name || action != CreateNone {
		t.Fatal("busy edits")
	}
	if _, action := UpdateCreate(m, tui.Key{Kind: tui.KeyCtrlC}); action != CreateCancel {
		t.Fatal("busy cancel must reach controller")
	}
}

func TestRenderCreate(t *testing.T) {
	m := createSample()
	lines := RenderCreate(m, 80, 24)
	if len(lines) != 24 {
		t.Fatalf("rows: %d", len(lines))
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"New worktree", "mfe", "Branch", "widget-studio/dev-2", "/worktrees/mfe/widget-studio-dev-2", "Base", "current · ↓3 behind", "upstream · fetching…", "Tab switch · Enter create · Esc cancel"} {
		if !strings.Contains(plain(all), want) {
			t.Errorf("missing %q in\n%s", want, plain(all))
		}
	}
	if !strings.Contains(all, tui.Reverse(m.Name)) {
		t.Fatal("selected name must be reversed")
	}
	for _, line := range lines {
		if tui.Width(line) > 80 {
			t.Fatalf("line overflow: %q", line)
		}
	}
	for _, size := range [][2]int{{0, 0}, {1, 1}, {8, 3}, {20, 8}, {64, 18}} {
		for _, line := range RenderCreate(m, size[0], size[1]) {
			if tui.Width(line) > size[0] {
				t.Fatalf("small overflow %v: %q", size, line)
			}
		}
	}
}
