package worktreeui

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"herdr-git-upstream/internal/judge"
	"herdr-git-upstream/internal/tui"
)

// sample은 판정 넷이 하나씩 든 정렬된 모델이다. 그리기와 키 시험의 바탕이다.
func sample() Model {
	return Model{
		RepoName: "mfe",
		Rows: []Row{
			{Path: "/wt/add-widget", Branch: "add-widget", Verdict: judge.Safe, Detail: "gone, merged", OpenWorkspaceID: "w2"},
			{Path: "/wt/design-qa-3", Branch: "design-qa-3", Verdict: judge.Review, Detail: "↓12, conflicts"},
			{Path: "/wt/agent-admin", Branch: "agent-admin", Verdict: judge.Keep, Detail: "up to date"},
			{Path: "/mfe", Branch: "widget-studio/dev", Verdict: judge.Blocked, Detail: "main checkout", IsMain: true, OpenWorkspaceID: "w1"},
		},
		Fetch:      FetchDone,
		FetchedAgo: "3s ago",
	}
}

var csi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// plain은 꾸밈을 뗀 글자만 남긴다. 자리와 글자는 이것으로, 꾸밈은 따로 본다.
func plain(s string) string {
	return csi.ReplaceAllString(s, "")
}

// 고정 크기에서 그림(docs/PLAN.md)대로 줄이 나오는지 본다. 제목, 빈 줄, 행, 빈 줄, 마지막 줄.
func TestRenderLayout(t *testing.T) {
	m := sample()
	lines := Render(m, 80, 10)
	if len(lines) != 10 {
		t.Fatalf("줄 수가 %d, 기대값 10:\n%q", len(lines), lines)
	}

	title := plain(lines[0])
	if !strings.HasPrefix(title, " mfe · 4 worktrees") || !strings.Contains(title, "fetched 3s ago") || !strings.HasSuffix(title, keyHint) {
		t.Fatalf("제목 줄이 다르다: %q", title)
	}
	// 오른쪽은 창 끝(한 칸 여백)에 붙는다.
	if got := tui.Width(lines[0]); got != 79 {
		t.Fatalf("제목 줄의 폭이 %d, 기대값 79: %q", got, title)
	}
	if !strings.Contains(lines[0], tui.Bold("mfe")) {
		t.Fatalf("저장소 이름은 굵어야 한다: %q", lines[0])
	}
	if lines[1] != "" || lines[8] != "" {
		t.Fatalf("제목과 마지막 줄 옆은 빈 줄이어야 한다: %q %q", lines[1], lines[8])
	}

	want := []string{
		" safe     add-widget         gone, merged",
		" review   design-qa-3        ↓12, conflicts",
		" keep     agent-admin        up to date",
		" blocked  widget-studio/dev  main checkout",
	}
	for i, row := range want {
		got := strings.TrimRight(plain(lines[2+i]), " ")
		if got != row {
			t.Fatalf("%d 번째 행이 다르다:\n%q\n기대값\n%q", i, got, row)
		}
	}
	if lines[6] != "" || lines[7] != "" {
		t.Fatalf("행이 모자라면 빈 줄로 채운다: %q %q", lines[6], lines[7])
	}
	if got := plain(lines[9]); got != " 1 safe · d remove selected · D remove all safe · q close" {
		t.Fatalf("마지막 줄이 다르다: %q", got)
	}
}

// 커서 행은 창 끝까지 반전이고, 판정 열은 판정마다 색이 다르다.
func TestRenderCursorAndColors(t *testing.T) {
	m := sample()
	m.Cursor = 1
	lines := Render(m, 60, 10)

	cursor := lines[3]
	if !strings.HasPrefix(cursor, "\x1b[7m") || !strings.HasSuffix(cursor, "\x1b[27m") {
		t.Fatalf("커서 행은 반전이어야 한다: %q", cursor)
	}
	if got := tui.Width(cursor); got != 60 {
		t.Fatalf("커서 행은 창 폭까지 채워야 반전이 끝까지 간다: 폭 %d", got)
	}
	if strings.HasPrefix(lines[2], "\x1b[7m") {
		t.Fatalf("커서가 아닌 행은 반전이 아니어야 한다: %q", lines[2])
	}

	cases := []struct {
		name string
		line string
		want string
	}{
		{"safe 는 초록", lines[2], "\x1b[32msafe   \x1b[39m"},
		{"review 는 노랑", lines[3], "\x1b[33mreview \x1b[39m"},
		{"blocked 는 흐림", lines[5], "\x1b[2mblocked\x1b[22m"},
	}
	for _, tc := range cases {
		if !strings.Contains(tc.line, tc.want) {
			t.Fatalf("%s: %q 에 %q 가 없다", tc.name, tc.line, tc.want)
		}
	}
	if strings.Contains(lines[4], "\x1b[3") || strings.Contains(lines[4], "\x1b[2m") {
		t.Fatalf("keep 은 기본색이어야 한다: %q", lines[4])
	}
}

// 행이 창보다 많으면 커서가 보이도록 위를 잘라 낸다. 커서는 언제나 보이는 마지막 줄 안에 있다.
func TestRenderScrollsToCursor(t *testing.T) {
	var m Model
	for i := 0; i < 10; i++ {
		m.Rows = append(m.Rows, Row{Branch: "b" + string(rune('0'+i)), Verdict: judge.Keep, Detail: "up to date"})
	}
	cases := []struct {
		name   string
		cursor int
		rows   int
		first  string
		last   string
	}{
		{"위쪽이면 처음부터", 0, 7, "b0", "b2"},
		{"창 안의 마지막 행", 2, 7, "b0", "b2"},
		{"창을 넘으면 커서가 마지막 줄", 5, 7, "b3", "b5"},
		{"끝까지", 9, 7, "b7", "b9"},
		{"빈 줄 없는 작은 창", 4, 4, "b3", "b4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m.Cursor = tc.cursor
			lines := Render(m, 40, tc.rows)
			height, padded := layout(tc.rows)
			start := 1
			if padded {
				start = 2
			}
			visible := lines[start : start+height]
			if !strings.Contains(plain(visible[0]), tc.first) || !strings.Contains(plain(visible[len(visible)-1]), tc.last) {
				t.Fatalf("보이는 행이 다르다: %q, 기대값 %s..%s", visible, tc.first, tc.last)
			}
			found := false
			for _, line := range visible {
				if strings.HasPrefix(line, "\x1b[7m") {
					found = true
				}
			}
			if !found {
				t.Fatalf("커서 행이 보여야 한다: %q", visible)
			}
		})
	}
}

// 마지막 줄은 물음, 안내, 기본 순이다. 물음이 떠 있으면 안내가 있어도 물음이다.
func TestRenderFooter(t *testing.T) {
	cases := []struct {
		name string
		edit func(m *Model)
		want string
	}{
		{"기본", func(m *Model) {}, " 1 safe · d remove selected · D remove all safe · q close"},
		{"안내", func(m *Model) { m.Message = "removed 1 worktree" }, " removed 1 worktree"},
		{"물음(여럿)", func(m *Model) { m.Confirm = &Confirm{Count: 3} }, " Remove 3 worktrees? y/N"},
		{"물음(하나)", func(m *Model) { m.Confirm = &Confirm{Count: 1} }, " Remove 1 worktree? y/N"},
		{"물음이 안내보다 먼저", func(m *Model) { m.Message = "x"; m.Confirm = &Confirm{Count: 2} }, " Remove 2 worktrees? y/N"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := sample()
			tc.edit(&m)
			lines := Render(m, 80, 8)
			if got := plain(lines[len(lines)-1]); got != tc.want {
				t.Fatalf("%q, 기대값 %q", got, tc.want)
			}
		})
	}
}

// 제목 줄은 herdr 상태와 fetch 상태를 말한다.
func TestRenderTitleStatus(t *testing.T) {
	cases := []struct {
		name    string
		edit    func(m *Model)
		want    string
		exclude string
	}{
		{"fetch 전에는 키 안내만", func(m *Model) { m.Fetch = FetchIdle }, keyHint, "fetch"},
		{"fetch 중", func(m *Model) { m.Fetch = FetchFetching }, "fetching…", "fetched"},
		{"fetch 실패는 빨강", func(m *Model) { m.Fetch = FetchFailed }, tui.Fg("fetch failed", tui.Red), "fetched"},
		{"fetch 끝", func(m *Model) { m.Fetch = FetchDone; m.FetchedAgo = "2m ago" }, "fetched 2m ago", "failed"},
		{"herdr 없음은 흐리게", func(m *Model) { m.HerdrUnavailable = true }, tui.Dim("herdr unavailable"), ""},
		{"하나면 단수", func(m *Model) { m.Rows = m.Rows[:1] }, " · 1 worktree ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := sample()
			tc.edit(&m)
			title := Render(m, 100, 5)[0]
			if !strings.Contains(title, tc.want) {
				t.Fatalf("%q 에 %q 가 없다", title, tc.want)
			}
			if tc.exclude != "" && strings.Contains(plain(title), tc.exclude) {
				t.Fatalf("%q 에 %q 가 있으면 안 된다", plain(title), tc.exclude)
			}
		})
	}
	// herdr 상태가 없으면 흐림 꾸밈도 없다.
	if title := Render(sample(), 100, 5)[0]; strings.Contains(title, "herdr") {
		t.Fatalf("herdr 에 닿으면 그 말이 없어야 한다: %q", title)
	}
}

// 창이 작으면 빈 줄부터 빼고, 제목과 마지막 줄은 끝까지 남긴다. 아예 없으면 아무것도 그리지 않는다.
func TestRenderSmallWindow(t *testing.T) {
	m := sample()
	cases := []struct {
		rows  int
		lines int
		first string
		last  string
	}{
		{0, 0, "", ""},
		{1, 1, " mfe", " mfe"},
		{2, 2, " mfe", " 1 safe"},
		{3, 3, " mfe", " 1 safe"},
		{4, 4, " mfe", " 1 safe"},
		{5, 5, " mfe", " 1 safe"},
	}
	for _, tc := range cases {
		lines := Render(m, 80, tc.rows)
		if len(lines) != tc.lines {
			t.Fatalf("rows=%d: 줄 수 %d, 기대값 %d: %q", tc.rows, len(lines), tc.lines, lines)
		}
		if tc.lines == 0 {
			continue
		}
		if !strings.HasPrefix(plain(lines[0]), tc.first) || !strings.HasPrefix(plain(lines[len(lines)-1]), tc.last) {
			t.Fatalf("rows=%d: 처음과 마지막이 다르다: %q", tc.rows, lines)
		}
	}
	// rows=3 은 행 하나, rows=4 는 행 둘이고 둘 다 빈 줄이 없다.
	if lines := Render(m, 80, 3); !strings.Contains(plain(lines[1]), "add-widget") {
		t.Fatalf("rows=3 의 가운데는 첫 행이어야 한다: %q", lines)
	}
	if lines := Render(m, 80, 4); !strings.Contains(plain(lines[2]), "design-qa-3") {
		t.Fatalf("rows=4 의 셋째는 둘째 행이어야 한다: %q", lines)
	}
	if lines := Render(m, 0, 5); lines != nil {
		t.Fatalf("폭이 0 이면 아무것도 그리지 않는다: %q", lines)
	}
}

// 행이 없으면 그렇다고 말한다. 빈 표는 아직 모으는 중인 것처럼 보인다.
func TestRenderEmpty(t *testing.T) {
	lines := Render(Model{RepoName: "x"}, 40, 6)
	if !strings.Contains(lines[2], tui.Dim(" no worktrees")) {
		t.Fatalf("행이 없다는 말이 흐리게 보여야 한다: %q", lines)
	}
	if !strings.HasPrefix(plain(lines[0]), " x · 0 worktrees") || !strings.HasPrefix(plain(lines[5]), " 0 safe") {
		t.Fatalf("제목과 마지막 줄: %q", lines)
	}
}

// 긴 이름은 이름 열의 상한에서 잘라 설명 열이 밀려나지 않게 한다.
func TestRenderCapsLabelWidth(t *testing.T) {
	long := strings.Repeat("x", maxLabelWidth+10)
	m := Model{Rows: []Row{
		{Branch: long, Verdict: judge.Keep, Detail: "up to date"},
		{Branch: "short", Verdict: judge.Keep, Detail: "↓1"},
	}}
	lines := Render(m, 200, 6)
	if got := plain(lines[2]); !strings.Contains(got, strings.Repeat("x", maxLabelWidth)+"  up to date") || strings.Contains(got, long) {
		t.Fatalf("긴 이름은 상한에서 잘려야 한다: %q", got)
	}
	if got := plain(lines[3]); !strings.Contains(got, "short"+strings.Repeat(" ", maxLabelWidth-5)+"  ↓1") {
		t.Fatalf("짧은 이름은 상한까지 채워야 열이 맞는다: %q", got)
	}
}

func key(r rune) tui.Key { return tui.Key{Kind: tui.KeyRune, Rune: r} }

// 키 표(docs/PLAN.md)대로 Action 이 나오고 모델이 바뀌는지 본다.
func TestUpdate(t *testing.T) {
	noSafe := sample()
	noSafe.Rows = noSafe.Rows[1:]
	cases := []struct {
		name    string
		start   Model
		key     tui.Key
		action  Action
		cursor  int
		message string
		confirm int
	}{
		{"위, 맨 위에서는 그대로", sample(), tui.Key{Kind: tui.KeyUp}, ActionNone, 0, "", 0},
		{"아래", sample(), tui.Key{Kind: tui.KeyDown}, ActionNone, 1, "", 0},
		{"j 는 아래", sample(), key('j'), ActionNone, 1, "", 0},
		{"k 는 위", withCursor(sample(), 2), key('k'), ActionNone, 1, "", 0},
		{"아래, 맨 아래에서는 그대로", withCursor(sample(), 3), key('j'), ActionNone, 3, "", 0},
		{"Enter 는 열기", withCursor(sample(), 2), tui.Key{Kind: tui.KeyEnter}, ActionOpen, 2, "", 0},
		{"행이 없으면 Enter 는 아무것도 아님", Model{}, tui.Key{Kind: tui.KeyEnter}, ActionNone, 0, "", 0},
		{"q 는 닫기", sample(), key('q'), ActionQuit, 0, "", 0},
		{"Esc 는 닫기", sample(), tui.Key{Kind: tui.KeyEsc}, ActionQuit, 0, "", 0},
		{"Ctrl-C 는 닫기", sample(), tui.Key{Kind: tui.KeyCtrlC}, ActionQuit, 0, "", 0},
		{"r 은 다시 fetch", sample(), key('r'), ActionRefresh, 0, "", 0},
		{"d 는 safe 행이면 지움", sample(), key('d'), ActionRemoveSelected, 0, "", 0},
		{"d 는 review 행이면 이유만", withCursor(sample(), 1), key('d'), ActionNone, 1, "design-qa-3 is review (↓12, conflicts); only safe worktrees can be removed", 0},
		{"d 는 blocked 행이면 이유만", withCursor(sample(), 3), key('d'), ActionNone, 3, "widget-studio/dev is blocked (main checkout); only safe worktrees can be removed", 0},
		{"d 는 행이 없으면 그렇다고", Model{}, key('d'), ActionNone, 0, "no worktree selected", 0},
		{"D 는 safe 가 있으면 물음", withCursor(sample(), 2), key('D'), ActionRemoveAllSafe, 2, "", 1},
		{"D 는 safe 가 없으면 그렇다고", noSafe, key('D'), ActionNone, 0, "no safe worktrees to remove", 0},
		{"모르는 키는 아무것도 아님", sample(), key('x'), ActionNone, 0, "", 0},
		{"창 크기 변경은 모델을 건드리지 않음", withMessage(sample(), "keep me"), tui.Key{Kind: tui.KeyResize}, ActionNone, 0, "keep me", 0},
		{"키가 오면 지난 안내를 지움", withMessage(sample(), "old"), tui.Key{Kind: tui.KeyDown}, ActionNone, 1, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, action := Update(tc.start, tc.key)
			if action != tc.action {
				t.Fatalf("action %s, 기대값 %s", action, tc.action)
			}
			if got.Cursor != tc.cursor {
				t.Fatalf("cursor %d, 기대값 %d", got.Cursor, tc.cursor)
			}
			if got.Message != tc.message {
				t.Fatalf("message %q, 기대값 %q", got.Message, tc.message)
			}
			count := 0
			if got.Confirm != nil {
				count = got.Confirm.Count
			}
			if count != tc.confirm {
				t.Fatalf("confirm %d, 기대값 %d", count, tc.confirm)
			}
			if !reflect.DeepEqual(got.Rows, tc.start.Rows) {
				t.Fatal("Update 는 행을 바꾸지 않는다")
			}
		})
	}
}

// 물음이 떠 있으면 y 만 승낙이고 나머지는 전부 거절이다. 어느 쪽이든 물음은 내린다. Ctrl-C 는 물음 중에도 닫기다.
func TestUpdateConfirm(t *testing.T) {
	asked := sample()
	asked.Confirm = &Confirm{Count: 1}
	cases := []struct {
		name   string
		key    tui.Key
		action Action
	}{
		{"y", key('y'), ActionConfirmYes},
		{"Y", key('Y'), ActionConfirmYes},
		{"n", key('n'), ActionConfirmNo},
		{"Esc", tui.Key{Kind: tui.KeyEsc}, ActionConfirmNo},
		{"Enter 는 승낙이 아님", tui.Key{Kind: tui.KeyEnter}, ActionConfirmNo},
		{"d 는 지우지 않고 거절", key('d'), ActionConfirmNo},
		{"q 는 닫지 않고 거절", key('q'), ActionConfirmNo},
		{"Ctrl-C 는 닫기", tui.Key{Kind: tui.KeyCtrlC}, ActionQuit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, action := Update(asked, tc.key)
			if action != tc.action {
				t.Fatalf("action %s, 기대값 %s", action, tc.action)
			}
			if got.Confirm != nil {
				t.Fatal("답을 받으면 물음은 내려야 한다")
			}
			if got.Cursor != asked.Cursor {
				t.Fatal("물음에 답하는 키는 커서를 움직이지 않는다")
			}
		})
	}
	// 창 크기 변경은 답이 아니다. 물음이 그대로 남는다.
	if got, action := Update(asked, tui.Key{Kind: tui.KeyResize}); action != ActionNone || got.Confirm == nil {
		t.Fatalf("창 크기 변경에 물음이 내려가면 안 된다: %s %+v", action, got.Confirm)
	}
}

func withCursor(m Model, cursor int) Model {
	m.Cursor = cursor
	return m
}

func withMessage(m Model, message string) Model {
	m.Message = message
	return m
}

// 정렬은 판정 순서 다음 이름이다. 분리된 HEAD 는 디렉터리 이름으로 이름을 삼는다.
func TestSortRows(t *testing.T) {
	rows := []Row{
		{Branch: "z-keep", Verdict: judge.Keep},
		{Branch: "main", Verdict: judge.Blocked},
		{Branch: "b-safe", Verdict: judge.Safe},
		{Path: "/wt/old", Verdict: judge.Safe},
		{Branch: "a-review", Verdict: judge.Review},
		{Branch: "a-safe", Verdict: judge.Safe},
		{Branch: "a-keep", Verdict: judge.Keep},
	}
	SortRows(rows)
	var got []string
	for _, row := range rows {
		got = append(got, row.Verdict.String()+":"+row.Label())
	}
	want := []string{"safe:(detached) old", "safe:a-safe", "safe:b-safe", "review:a-review", "keep:a-keep", "keep:z-keep", "blocked:main"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v\n기대값 %v", got, want)
	}
}

func TestSafeRowsAndSelected(t *testing.T) {
	m := sample()
	if safe := m.SafeRows(); len(safe) != 1 || safe[0].Branch != "add-widget" {
		t.Fatalf("safe 행이 다르다: %+v", safe)
	}
	if row, ok := m.Selected(); !ok || row.Branch != "add-widget" {
		t.Fatalf("선택 행이 다르다: %+v %v", row, ok)
	}
	if _, ok := (Model{}).Selected(); ok {
		t.Fatal("행이 없으면 선택도 없다")
	}
	if got := clampCursor(withCursor(m, 9)); got.Cursor != 3 {
		t.Fatalf("행 밖의 커서는 마지막 행으로: %d", got.Cursor)
	}
	if got := clampCursor(withCursor(m, -2)); got.Cursor != 0 {
		t.Fatalf("음수 커서는 첫 행으로: %d", got.Cursor)
	}
}

func TestAgo(t *testing.T) {
	cases := []struct {
		since time.Duration
		want  string
	}{
		{0, "just now"},
		{900 * time.Millisecond, "just now"},
		{3 * time.Second, "3s ago"},
		{59 * time.Second, "59s ago"},
		{90 * time.Second, "1m ago"},
		{59 * time.Minute, "59m ago"},
		{2 * time.Hour, "2h ago"},
		{30 * time.Hour, "30h ago"},
	}
	for _, tc := range cases {
		if got := Ago(tc.since); got != tc.want {
			t.Fatalf("%v -> %q, 기대값 %q", tc.since, got, tc.want)
		}
	}
}

func TestActionString(t *testing.T) {
	if ActionRemoveAllSafe.String() != "remove-all-safe" || Action(42).String() != "action(42)" {
		t.Fatalf("%s %s", ActionRemoveAllSafe, Action(42))
	}
}
