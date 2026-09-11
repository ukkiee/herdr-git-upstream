package worktreeui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

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
	return CreateModel{RepoName: "mfe", Name: "widget-studio/dev-2", NameCursor: len("widget-studio/dev-2"), PathRoot: "/worktrees", Taken: map[string]bool{"widget-studio/dev": true, "main": true}, Candidates: []Candidate{
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
		{"typing appends to name", []tui.Key{key('x')}, "widget-studio/dev-2x", true, 0, FocusName, CreateNone},
		{"backspace removes last character", []tui.Key{{Kind: tui.KeyBackspace}}, "widget-studio/dev-", true, 0, FocusName, CreateNone},
		{"base changes automatic name", []tui.Key{{Kind: tui.KeyTab}, {Kind: tui.KeyEnter}, {Kind: tui.KeyDown}, {Kind: tui.KeyEnter}}, "main-2", false, 1, FocusBase, CreateNone},
		{"edited name stays", []tui.Key{key('x'), {Kind: tui.KeyTab}, {Kind: tui.KeyEnter}, {Kind: tui.KeyDown}, {Kind: tui.KeyEnter}}, "widget-studio/dev-2x", true, 1, FocusBase, CreateNone},
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

func TestCreateEditsNameAtArrowCursor(t *testing.T) {
	m := createSample()
	keys, _ := tui.ParseKeys([]byte("\x1b[D\x1b[D한\x1b[C\x7f"))
	for _, k := range keys {
		m, _ = UpdateCreate(m, k)
	}
	if m.Name != "widget-studio/dev한2" || !m.UserEdited {
		t.Fatalf("middle insertion and backspace: %q", m.Name)
	}
	view := strings.Join(RenderCreate(m, 64, 18), "\n")
	if !strings.Contains(view, "dev한"+tui.Reverse("2")+"]") {
		t.Fatalf("cursor must mark the next character: %s", view)
	}
}

func TestCreateNameFieldHasNoCursorPadding(t *testing.T) {
	m := createSample()
	m.Name, m.NameCursor = "main-2", len("main-2")
	for _, focus := range []FocusArea{FocusName, FocusBase} {
		m.Focus = focus
		view := plain(strings.Join(RenderCreate(m, 64, 18), "\n"))
		if !strings.Contains(view, "Branch  [main-2]") {
			t.Errorf("focus %v leaves cursor padding inside the name: %s", focus, view)
		}
	}
}

func TestCreateBasePickerIsCompact(t *testing.T) {
	m := createSample()
	m.Candidates[0].Label = "main"
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyTab})
	closed := plain(strings.Join(RenderCreate(m, 64, 18), "\n"))
	if !strings.Contains(closed, "Base    [main ▾]") {
		t.Errorf("closed Base must fit its label: %s", closed)
	}
	for i := 0; i < 10; i++ {
		label := "feature/task-" + strconv.Itoa(i)
		m.Candidates = append(m.Candidates, Candidate{Label: label, Ref: "refs/heads/" + label, Kind: CandidateLocal, Status: "up to date"})
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
	view := plain(strings.Join(RenderCreate(m, 64, 24), "\n"))
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Search") && !strings.Contains(line, "Base") {
			t.Errorf("search must share the Base menu header: %s", line)
		}
		if start := strings.Index(line, "┌"); start >= 0 && !strings.Contains(line, "New worktree") {
			end := strings.LastIndex(line, "┐")
			if end >= 0 && tui.Width(line[start:end+len("┐")]) > 38 {
				t.Errorf("Base menu exceeds 38 cells: %s", line)
			}
		}
	}
	if strings.Count(view, "feature/task-") > 2 {
		t.Errorf("menu should show at most four candidates: %s", view)
	}
	if !strings.Contains(view, "current · ↓3 behind") || strings.Contains(view, "upstream · fetching") {
		t.Errorf("only highlighted candidate details should appear: %s", view)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyDown})
	view = plain(strings.Join(RenderCreate(m, 64, 18), "\n"))
	if !strings.Contains(view, "upstream · fetching…") || strings.Contains(view, "current · ↓3 behind") || m.Selected != 0 {
		t.Errorf("details must follow the highlight without committing the base: %s", view)
	}
}

func TestCreateCompactPickerPreservesBranchSuffix(t *testing.T) {
	m := createSample()
	for _, suffix := range []string{"first", "second"} {
		label := "feature/" + strings.Repeat("긴이름/", 10) + suffix
		m.Candidates = append(m.Candidates, Candidate{Label: label, Ref: "refs/heads/" + label, Kind: CandidateLocal})
	}
	keys, _ := tui.ParseKeys([]byte("\t\r긴이름"))
	for _, k := range keys {
		m, _ = UpdateCreate(m, k)
	}
	view := plain(strings.Join(RenderCreate(m, 64, 18), "\n"))
	for _, want := range []string{"feature/", "…", "first", "second"} {
		if !strings.Contains(view, want) {
			t.Fatalf("long candidates lose their identity %q: %s", want, view)
		}
	}
	for _, line := range RenderCreate(m, 32, 12) {
		if tui.Width(line) > 32 {
			t.Fatalf("compact Unicode label overflows: %s", line)
		}
	}
}

func TestCreateSearchSelectsFromThousandBranches(t *testing.T) {
	m := createSample()
	for i := 0; i < 1200; i++ {
		label := "feature/task-" + strconv.Itoa(i)
		m.Candidates = append(m.Candidates, Candidate{Label: label, Ref: "refs/heads/" + label, Kind: CandidateLocal, Status: "up to date"})
	}
	keys, _ := tui.ParseKeys([]byte("\t\rTASK-119"))
	for _, k := range keys {
		m, _ = UpdateCreate(m, k)
	}
	view := plain(strings.Join(RenderCreate(m, 64, 18), "\n"))
	for _, want := range []string{"Base    ┌ / TASK-119", "1/11", "1202", "feature/task-119"} {
		if !strings.Contains(view, want) {
			t.Fatalf("search missing %q: %s", want, view)
		}
	}
	if strings.Contains(view, "feature/task-118") || m.Selected != 0 || m.Name != "widget-studio/dev-2" {
		t.Fatalf("search must only narrow uncommitted choices: %s", view)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyDown})
	m, action := UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
	if action != CreateNone || m.MenuOpen || m.Name != "feature/task-1190" || m.Candidates[m.Selected].Ref != "refs/heads/feature/task-1190" {
		t.Fatalf("selected wrong filtered branch: name=%q selected=%d action=%v", m.Name, m.Selected, action)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyTab})
	if _, action := UpdateCreate(m, tui.Key{Kind: tui.KeyEnter}); action != CreateSubmit {
		t.Fatal("filtered base must be usable for creation")
	}
}

func TestCreateSearchHighlightsMatches(t *testing.T) {
	for _, tc := range []struct {
		name, label, query string
		matches            []string
	}{
		{"case insensitive", "feature/Task-task", "TASK", []string{"Task", "task"}},
		{"Korean", "feature/한글-한글", "한글", []string{"한글", "한글"}},
		{"case conversion changes byte width", "feature/KELVIN", "kel", []string{"KEL"}},
		{"overlapping matches", "feature/banana", "ana", []string{"anana"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := createSample()
			m.Candidates = []Candidate{
				{Label: tc.label, Ref: "refs/heads/first", Kind: CandidateLocal},
				{Label: tc.label + "-2", Ref: "refs/heads/second", Kind: CandidateLocal},
			}
			keys, _ := tui.ParseKeys([]byte("\t\r" + tc.query))
			for _, k := range keys {
				m, _ = UpdateCreate(m, k)
			}
			view := strings.Join(RenderCreate(m, 64, 18), "\n")
			counts := map[string]int{}
			for _, match := range tc.matches {
				counts[match] += 2 // Both the selected and the unselected candidate contain it.
			}
			for match, want := range counts {
				if got := strings.Count(view, tui.Bold(tui.Fg(match, tui.Cyan))); got != want {
					t.Errorf("match emphasis for %q: got %d, want %d in %s", match, got, want, view)
				}
			}
			for _, line := range strings.Split(view, "\n") {
				if !strings.Contains(plain(line), tc.label) {
					continue
				}
				if !strings.Contains(line, "\x1b[36m") {
					t.Errorf("selected and unselected candidates must both emphasize matches: %s", line)
				}
				if strings.Contains(plain(line), "▸") && !strings.Contains(line, "\x1b[7m▸") {
					t.Errorf("match emphasis must preserve row selection: %s", line)
				}
			}
			m.MenuQuery = ""
			if strings.Contains(strings.Join(RenderCreate(m, 64, 18), "\n"), "\x1b[36m") {
				t.Fatal("empty query must not emphasize labels")
			}
		})
	}
}

func TestCreateSearchHighlightSurvivesEllipsis(t *testing.T) {
	m := createSample()
	label := strings.Repeat("x", 14) + "NEEDLE" + strings.Repeat("y", 20) + "needle-tail"
	m.Candidates = []Candidate{{Label: label, Ref: "refs/heads/long", Kind: CandidateLocal}}
	keys, _ := tui.ParseKeys([]byte("\t\rneedle"))
	for _, k := range keys {
		m, _ = UpdateCreate(m, k)
	}
	view := strings.Join(RenderCreate(m, 64, 18), "\n")
	for _, want := range []string{tui.Bold(tui.Fg("NE", tui.Cyan)) + "…", tui.Bold(tui.Fg("needle", tui.Cyan)) + "-tail"} {
		if !strings.Contains(view, want) {
			t.Errorf("visible part of original match must remain emphasized: %q in %s", want, view)
		}
	}
	for _, cols := range []int{20, 32, 62, 64} {
		for _, line := range RenderCreate(m, cols, 16) {
			if tui.Width(line) > cols || !utf8.ValidString(line) {
				t.Fatalf("highlight corrupts width or UTF-8 at %d columns: %q", cols, line)
			}
		}
	}
}

func TestCreateCursorBoundsAndUnicode(t *testing.T) {
	for _, tc := range []struct{ name, input, want, caret string }{
		{"left boundary", strings.Repeat("\x1b[D", 30) + "\x7fX", "Xwidget-studio/dev-2", "w"},
		{"right boundary", strings.Repeat("\x1b[C", 30) + "X", "widget-studio/dev-2X", "]"},
		{"unicode deletion", "한글\x1b[D\x7f", "widget-studio/dev-2글", "글"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := createSample()
			keys, _ := tui.ParseKeys([]byte(tc.input))
			for _, k := range keys {
				m, _ = UpdateCreate(m, k)
			}
			if m.Name != tc.want || !strings.Contains(strings.Join(RenderCreate(m, 64, 18), "\n"), tui.Reverse(tc.caret)) {
				t.Fatalf("name/cursor after %s: %q", tc.name, m.Name)
			}
		})
	}
}

func TestCreateSearchNoMatchesRecoveryAndCancel(t *testing.T) {
	m := createSample()
	m.Candidates = append(m.Candidates, Candidate{Label: "feature/한글", Ref: "refs/heads/feature/한글", Kind: CandidateLocal, Status: "up to date"})
	keys, _ := tui.ParseKeys([]byte("\t\r한글X\x1b[B\x1b[A\r"))
	for _, k := range keys {
		var action CreateAction
		m, action = UpdateCreate(m, k)
		if action != CreateNone {
			t.Fatalf("empty search must not submit or cancel: %v", action)
		}
	}
	// A background fetch must not close an empty search or choose a hidden row.
	m = m.autoName()
	view := plain(strings.Join(RenderCreate(m, 64, 18), "\n"))
	if !m.MenuOpen || m.Selected != 0 || m.Name != "widget-studio/dev-2" || !strings.Contains(view, "No matching branches") || !strings.Contains(view, "0/0") {
		t.Fatalf("empty search: %s", view)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyBackspace})
	view = plain(strings.Join(RenderCreate(m, 64, 18), "\n"))
	if !strings.Contains(view, "feature/한글") || !strings.Contains(view, "1/1") {
		t.Fatalf("Backspace must restore matches: %s", view)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyBackspace})
	view = plain(strings.Join(RenderCreate(m, 64, 18), "\n"))
	if !strings.Contains(view, "Base    ┌ / 한 ") || strings.Contains(view, "/ 한글") {
		t.Fatalf("search Backspace must remove a whole Unicode character: %s", view)
	}
	m, action := UpdateCreate(m, tui.Key{Kind: tui.KeyEsc})
	if action != CreateNone || m.MenuOpen || m.Selected != 0 {
		t.Fatalf("Esc must discard search only: %+v", m)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
	view = plain(strings.Join(RenderCreate(m, 64, 18), "\n"))
	if !strings.Contains(view, "1/3") || !strings.Contains(view, "team/upstream/main") {
		t.Fatalf("reopening must restore all candidates: %s", view)
	}
}

func TestCreateSearchKeepsRefAcrossAsyncInsertion(t *testing.T) {
	m := createSample()
	keys, _ := tui.ParseKeys([]byte("\x1b[D\t\rMAIN"))
	for _, k := range keys {
		m, _ = UpdateCreate(m, k)
	}
	chosen := m.Candidates[1]
	m.Candidates = []Candidate{m.Candidates[0], {Label: "main", Ref: "refs/heads/main", Kind: CandidateLocal, Status: "up to date"}, chosen}
	m = m.autoName()
	view := plain(strings.Join(RenderCreate(m, 64, 18), "\n"))
	if !m.MenuOpen || !strings.Contains(view, "2/2") || m.NameCursor != len("widget-studio/dev-") || m.UserEdited {
		t.Fatalf("async insertion changed search or cursor: %s cursor=%d", view, m.NameCursor)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
	if m.Candidates[m.Selected].Ref != chosen.Ref || m.Name != "main-2" {
		t.Fatalf("async insertion changed selected base: %+v", m)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyTab})
	m, _ = UpdateCreate(m, key('X'))
	if m.Name != "main-2X" {
		t.Fatalf("new automatic name must start with cursor at end: %q", m.Name)
	}
}

func TestRenderCreateKeepsMovingCursorVisible(t *testing.T) {
	m := createSample()
	m.Name = strings.Repeat("긴이름/", 20) + "tail-2"
	m.NameCursor = utf8.RuneCountInString(m.Name)
	for i := 0; i <= utf8.RuneCountInString(m.Name); i++ {
		for _, cols := range []int{20, 32, 62, 64} {
			lines := RenderCreate(m, cols, 16)
			caret := "]"
			if m.NameCursor < utf8.RuneCountInString(m.Name) {
				caret = string([]rune(m.Name)[m.NameCursor])
			}
			if !strings.Contains(strings.Join(lines, "\n"), tui.Reverse(caret)) {
				t.Fatalf("cursor hidden at %d columns, position %d", cols, m.NameCursor)
			}
			for _, line := range lines {
				if tui.Width(line) > cols {
					t.Fatalf("cursor scroll overflows %d columns: %s", cols, line)
				}
			}
		}
		m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyLeft})
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
	if !strings.Contains(all, m.Name+tui.Reverse("]")) || strings.Contains(all, tui.Reverse(m.Name)) {
		t.Fatal("end cursor must use the closing bracket without an extra space")
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

func TestRenderCreateShowsCompleteCreationWarning(t *testing.T) {
	m := createSample()
	m.Message = "cannot protect the selected base: a previous creation of this branch has not completed; choose another name"
	for _, size := range [][2]int{{64, 18}, {62, 16}} {
		view := plain(strings.Join(RenderCreate(m, size[0], size[1]), "\n"))
		for _, want := range []string{"cannot protect the selected base:", "has not completed;", "choose another name", "Enter create", "Esc cancel"} {
			if !strings.Contains(view, want) {
				t.Fatalf("warning clipped at %v, missing %q:\n%s", size, want, view)
			}
		}
	}
}

func TestCreateCanReadLongMessageAndReturnWithoutSubmitting(t *testing.T) {
	m := createSample()
	m.Cols, m.Rows = 32, 12
	m.Message = "creation failed:\n" + strings.Repeat("long diagnostic with 한글 ", 25) + "\nchoose another name"
	view := plain(strings.Join(RenderCreate(m, m.Cols, m.Rows), "\n"))
	if !strings.Contains(view, "F1") {
		t.Fatalf("overflow must offer full details: %s", view)
	}
	m, action := UpdateCreate(m, tui.Key{Kind: tui.KeyF1})
	if action != CreateNone || !m.MessageOpen {
		t.Fatal("F1 must open the complete message")
	}
	for i := 0; i < 100; i++ {
		m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyDown})
	}
	view = plain(strings.Join(RenderCreate(m, m.Cols, m.Rows), "\n"))
	if !strings.Contains(view, "choose another name") || !strings.Contains(view, "Enter/Esc back") {
		t.Fatalf("cannot read recovery instruction: %s", view)
	}
	m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyUp})
	up := plain(strings.Join(RenderCreate(m, m.Cols, m.Rows), "\n"))
	if view == up {
		t.Fatal("scrolling up must respond immediately even after reaching the end")
	}
	for _, k := range []tui.KeyKind{tui.KeyEnter, tui.KeyEsc, tui.KeyF1} {
		closed, action := UpdateCreate(m, tui.Key{Kind: k})
		if action != CreateNone || closed.MessageOpen || closed.Name != m.Name || closed.Selected != m.Selected || closed.Message != m.Message {
			t.Fatalf("closing details must preserve form and never submit/cancel: %+v %v", closed, action)
		}
	}
	if _, action := UpdateCreate(m, tui.Key{Kind: tui.KeyCtrlC}); action != CreateCancel {
		t.Fatal("Ctrl-C must still cancel")
	}
}

func TestRenderCreateMessageWidthsAndHardBreaks(t *testing.T) {
	m := createSample()
	m.MessageOpen = true
	m.Message = strings.Repeat("긴경로/", 40) + "\nchoose another name\n\x1b[31mfailed\x1b[0m"
	for _, size := range [][2]int{{64, 18}, {62, 16}, {32, 12}, {20, 8}} {
		m.Cols, m.Rows = size[0], size[1]
		m.MessageOffset = 0
		var seen string
		for i := 0; i < 150; i++ {
			lines := RenderCreate(m, size[0], size[1])
			for _, line := range lines {
				if tui.Width(line) > size[0] {
					t.Fatalf("message overflows %v: %q", size, line)
				}
			}
			seen += plain(strings.Join(lines, "\n"))
			m, _ = UpdateCreate(m, tui.Key{Kind: tui.KeyDown})
		}
		if !strings.Contains(seen, "choose another") || !strings.Contains(seen, "name") || !strings.Contains(seen, "failed") {
			t.Fatalf("message content lost at %v", size)
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
	for _, want := range []string{"Enter select", "Esc close", "Search branches", "▸", "fetching…", "14/14"} {
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

func TestRenderCreateKeepsEndCursorVisible(t *testing.T) {
	m := createSample()
	m.Name = strings.Repeat("긴이름/", 20) + "tail-2"
	m.NameCursor = utf8.RuneCountInString(m.Name)
	for _, cols := range []int{32, 62, 64} {
		lines := RenderCreate(m, cols, 16)
		all := strings.Join(lines, "\n")
		if !strings.Contains(all, "tail-2"+tui.Reverse("]")) {
			t.Fatalf("end cursor is not visible at %d columns: %s", cols, all)
		}
		for _, line := range lines {
			if tui.Width(line) > cols {
				t.Fatalf("name overflows %d columns: %s", cols, line)
			}
		}
	}
}
