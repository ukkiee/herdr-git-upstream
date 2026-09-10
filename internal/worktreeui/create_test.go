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
		{"base changes automatic name", []tui.Key{{Kind: tui.KeyTab}, {Kind: tui.KeyEnter}, {Kind: tui.KeyDown}, {Kind: tui.KeyEnter}}, "main-2", false, 1, FocusBase, CreateNone},
		{"edited name stays", []tui.Key{key('x'), {Kind: tui.KeyTab}, {Kind: tui.KeyEnter}, {Kind: tui.KeyDown}, {Kind: tui.KeyEnter}}, "x", true, 1, FocusBase, CreateNone},
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

func TestUpdateCreateDropdownDefersSelection(t *testing.T) {
	m := createSample()
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyTab})
	m, action := UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
	if action != CreateNone || !m.MenuOpen {
		t.Fatalf("Base Enter must open the menu: %+v %v", m, action)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyDown})
	if m.Selected != 0 || m.Name != "widget-studio/dev-2" || m.MenuIndex != 1 {
		t.Fatalf("highlight changed committed base/name: %+v", m)
	}
	m, action = UpdateCreate(m, tui.Key{Kind: tui.KeyEsc})
	if action != CreateNone || m.MenuOpen || m.Selected != 0 {
		t.Fatalf("Esc must discard menu selection only: %+v %v", m, action)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyDown})
	m, action = UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
	if action != CreateNone || m.MenuOpen || m.Selected != 1 || m.Name != "main-2" {
		t.Fatalf("Enter must commit only the highlighted base: %+v %v", m, action)
	}
	m.Candidates[1].Status = "up to date"
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyTab})
	if _, action = UpdateCreate(m, tui.Key{Kind: tui.KeyEnter}); action != CreateSubmit {
		t.Fatal("Name Enter must still create")
	}
	if _, action = UpdateCreate(m, tui.Key{Kind: tui.KeyEsc}); action != CreateCancel {
		t.Fatal("Esc with closed menu must cancel popup")
	}
}

func TestCreateDropdownReconcilesFetchResults(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic name", true: "edited name"}[edited], func(t *testing.T) {
			m := createSample()
			m.UserEdited = edited
			m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyTab})
			m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
			m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyDown})
			chosen := m.Candidates[1]
			chosen.Status = "up to date"
			// A completed default lookup inserts another candidate before the highlight.
			m.Candidates = []Candidate{m.Candidates[0], {Label: "origin/develop", Ref: "refs/remotes/origin/develop", Kind: CandidateDefault}, chosen}
			m.Taken = map[string]bool{"widget-studio/dev": true, "main": true, "main-2": true}
			m = m.autoName()
			if m.MenuIndex != 2 || m.Selected != 0 || m.Name != "widget-studio/dev-2" {
				t.Fatalf("fetch changed committed base or lost highlight: %+v", m)
			}
			m, action := UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
			wantName := "main-3"
			if edited {
				wantName = "widget-studio/dev-2"
			}
			if action != CreateNone || m.MenuOpen || m.Candidates[m.Selected].Ref != chosen.Ref || m.Name != wantName {
				t.Fatalf("Enter committed a stale index: %+v %v", m, action)
			}
			m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
			m.Candidates = m.Candidates[:1]
			m.Selected = 0
			m = m.autoName()
			if m.MenuOpen || m.Message == "" {
				t.Fatalf("removed highlighted ref must close menu with a message: %+v", m)
			}
		})
	}
}

func TestCreateDropdownSingleCandidateAndTab(t *testing.T) {
	m := createSample()
	m.Candidates = m.Candidates[:1]
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyTab})
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
	if !m.MenuOpen {
		t.Fatal("one candidate still opens the dropdown")
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyDown})
	if m.MenuIndex != 0 {
		t.Fatal("single candidate highlight must stay in bounds")
	}
	if _, action := UpdateCreate(m, tui.Key{Kind: tui.KeyCtrlC}); action != CreateCancel {
		t.Fatal("Ctrl-C cancels the popup")
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyTab})
	if m.MenuOpen || m.Focus != FocusName || m.Selected != 0 {
		t.Fatalf("Tab must close menu and focus name: %+v", m)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyTab})
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyDown})
	if m.MenuOpen || m.Selected != 0 {
		t.Fatal("closed dropdown arrows must not change selection")
	}
}

func TestRenderCreate(t *testing.T) {
	m := createSample()
	lines := RenderCreate(m, 80, 24)
	if len(lines) != 24 {
		t.Fatalf("rows: %d", len(lines))
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"New worktree", "mfe", "Branch", "widget-studio/dev-2", "/worktrees/mfe/widget-studio-dev-2", "Base", "current · ↓3 behind", "▾", "Tab switch · Enter create · Esc cancel"} {
		if !strings.Contains(plain(all), want) {
			t.Errorf("missing %q in\n%s", want, plain(all))
		}
	}
	if strings.Contains(plain(all), "team/upstream/main") {
		t.Fatal("collapsed Base must not show other candidates")
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

func TestRenderCreateDropdownAndViewport(t *testing.T) {
	m := createSample()
	m.RepoName = strings.Repeat("long-repo-", 10)
	for i := 0; i < 12; i++ {
		m.Candidates = append(m.Candidates, Candidate{Label: "feature/" + strings.Repeat("long-", 12) + string(rune('a'+i)), Ref: "refs/heads/" + string(rune('a'+i)), Kind: CandidateMergeTarget, Status: "fetching…"})
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyTab})
	closed := plain(strings.Join(RenderCreate(m, 62, 16), "\n"))
	if !strings.Contains(closed, "Enter open") || strings.Contains(closed, "team/upstream/main") {
		t.Fatalf("closed Base footer/list: %s", closed)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
	for i := 0; i < 20; i++ {
		m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyDown})
	}
	for _, size := range [][2]int{{62, 16}, {64, 18}, {32, 12}, {20, 8}} {
		lines := RenderCreate(m, size[0], size[1])
		if len(lines) != size[1] {
			t.Fatalf("row overflow: %v", size)
		}
		for _, line := range lines {
			if tui.Width(line) > size[0] {
				t.Fatalf("width overflow %v: %q", size, line)
			}
		}
	}
	open := plain(strings.Join(RenderCreate(m, 62, 16), "\n"))
	for _, want := range []string{"Enter select", "Esc close", "▴", "▸", "fetching…"} {
		if !strings.Contains(open, want) {
			t.Fatalf("missing %q: %s", want, open)
		}
	}
	if !strings.Contains(strings.Join(RenderCreate(m, 62, 16), "\n"), "\x1b[7m") {
		t.Fatal("highlighted menu candidate must be reversed")
	}
	if narrow := plain(strings.Join(RenderCreate(m, 32, 12), "\n")); !strings.Contains(narrow, "feature/") {
		t.Fatalf("narrow dropdown must retain candidate identity: %s", narrow)
	}
}
