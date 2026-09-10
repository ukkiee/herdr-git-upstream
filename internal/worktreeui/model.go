// Package worktreeui는 worktree 화면이다. 한 저장소의 worktree 전부를 판정과 함께 표로 보이고, safe 인 것만 지운다.
//
// 세 겹으로 나뉜다. 모델(Model, Row)과 그리기(Render)와 키 처리(Update)는 순수 함수라 터미널 없이 시험한다.
// 자료 모으기(collect)는 git 에 묻되 herdr 없이 돈다. herdr 호출(목록, 삭제, 이동)은 컨트롤러(Run)만 하고,
// 그것도 인터페이스(Herdr) 뒤에 두어 가짜로 시험한다.
//
// 모델은 컨트롤러 고루틴 하나만 만진다. fetch 와 자료 모으기는 배경 고루틴이 하지만 결과를 채널로 돌려주고,
// 모델에 손대는 것은 그 결과를 받은 컨트롤러다. 잠금이 없어야 화면 논리가 순수 함수로 남고 데이터 경쟁이 없다.
//
// 판정 규칙은 여기 없다. internal/judge 의 Assess 가 재료(Facts)에서 판정과 설명 조각을 내고, 이 패키지는 그것을
// 그대로 그린다. 사이드바 토큰과 화면이 같은 답을 내야 하므로 규칙은 한곳에만 둔다.
package worktreeui

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"herdr-git-upstream/internal/judge"
	"herdr-git-upstream/internal/tui"
)

// Row는 화면의 한 행, worktree 하나다.
type Row struct {
	// Path는 worktree 의 디렉터리다. 삭제와 열기의 대상이다.
	Path string
	// Branch는 체크아웃된 브랜치 이름이다. 분리된 HEAD 면 비어 있다.
	Branch string
	// Head는 이 행을 판정할 때의 커밋이다. 삭제 직전에 같은 대상인지 다시 확인한다.
	Head string
	// Verdict는 judge.Assess 가 낸 판정이다. 영값은 blocked 라 판정을 채우지 못한 행도 지워지지 않는다.
	Verdict judge.Verdict
	// Detail은 Assess 가 준 설명 조각들을 ", " 로 이은 것이다. 예: "gone, merged", "↓12, conflicts".
	Detail string
	// OpenWorkspaceID는 이 worktree 가 열려 있는 herdr 워크스페이스다. 비어 있으면 디스크에만 있다.
	// 있으면 삭제와 이동을 herdr 에게 맡기고, 없으면 git 을 직접 부른다.
	OpenWorkspaceID string
	// IsMain은 본 체크아웃이다. 지울 수 없고, Enter 는 본 체크아웃의 워크스페이스로 간다.
	IsMain bool
}

// Label은 화면에 보이는 이름이다. 브랜치 이름이고, 분리된 HEAD 는 브랜치가 없으므로 디렉터리 이름에 표시를 붙인다.
// 정렬도 이 이름으로 한다.
func (r Row) Label() string {
	if r.Branch != "" {
		return r.Branch
	}
	return "(detached) " + filepath.Base(r.Path)
}

// FetchState는 화면이 열릴 때 스스로 도는 fetch 의 상태다. 제목 줄에 보인다.
type FetchState int

const (
	// FetchIdle은 아직 fetch 를 시작하지 않았거나 할 원격이 없다.
	FetchIdle FetchState = iota
	// FetchFetching은 배경에서 도는 중이다.
	FetchFetching
	// FetchDone은 끝났다. Model.FetchedAgo 가 얼마나 전인지 말한다.
	FetchDone
	// FetchFailed는 실패했다. 제목 줄에 "fetch failed" 로만 보이고 표는 로컬 자료로 그린다.
	FetchFailed
)

// Confirm은 마지막 줄에 떠 있는 물음이다. D(safe 전부 삭제)만 이것을 켠다(ADR 0002).
type Confirm struct {
	// Count는 지울 safe worktree 수다. 물음 문구에 쓴다.
	Count int
	// Rows는 D 를 누른 순간 확인한 대상이다. 배경 갱신이 삭제 범위를 바꿀 수 없다.
	Rows []Row
}

// Model은 화면의 전부다. Render 는 이것만 보고 그린다.
type Model struct {
	// RepoName은 제목 줄의 저장소 이름이다. herdr 가 부르는 이름이거나, 없으면 본 체크아웃의 디렉터리 이름이다.
	RepoName string
	// Rows는 정렬된 행들이다. 정렬은 컨트롤러가 SortRows 로 한다.
	Rows []Row
	// Cursor는 선택된 행의 자리다. Rows 가 비어 있으면 뜻이 없다.
	Cursor int
	Fetch  FetchState
	// FetchedAgo는 "3s ago" 같은 문구다. FetchDone 일 때만 제목 줄에 붙는다.
	FetchedAgo string
	// Message는 마지막 줄에 보이는 안내다. 다음 키가 오면 지워진다.
	Message string
	// Confirm이 있으면 마지막 줄은 물음이고, 다음 키는 그 답으로 읽는다.
	Confirm *Confirm
	// HerdrUnavailable이 참이면 herdr 에 닿지 못해 git 만으로 목록을 만들었다. 제목 줄에 흐리게 보인다.
	// 그때는 "herdr 에 열려 있음" 정보가 없어 Enter 와 herdr 를 거치는 삭제가 되지 않는다.
	HerdrUnavailable bool
}

// SafeRows는 safe 판정인 행들이다. D 가 지울 대상이다.
func (m Model) SafeRows() []Row {
	var rows []Row
	for _, row := range m.Rows {
		if row.Verdict == judge.Safe {
			rows = append(rows, row)
		}
	}
	return rows
}

// Selected는 커서가 가리키는 행이다. 행이 없으면 거짓이다.
func (m Model) Selected() (Row, bool) {
	if len(m.Rows) == 0 || m.Cursor < 0 || m.Cursor >= len(m.Rows) {
		return Row{}, false
	}
	return m.Rows[m.Cursor], true
}

// SortRows는 판정 순서(safe, review, keep, blocked) 다음 이름으로 정렬한다. 손볼 것이 위에 온다.
func SortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Verdict.Order() != b.Verdict.Order() {
			return a.Verdict.Order() < b.Verdict.Order()
		}
		return a.Label() < b.Label()
	})
}

// Action은 Update 가 컨트롤러에게 시키는 일이다. 모델만 바뀌는 키는 ActionNone 이다.
type Action int

const (
	ActionNone Action = iota
	// ActionQuit은 화면을 닫는다. q, Esc, Ctrl-C.
	ActionQuit
	// ActionOpen은 선택한 worktree 로 간다. Enter.
	ActionOpen
	// ActionRemoveSelected는 선택한 safe worktree 를 확인 없이 지운다. d.
	ActionRemoveSelected
	// ActionRemoveAllSafe는 safe 전부를 지우겠다는 뜻이다. D. 물음(Confirm)이 모델에 실려 있으므로 컨트롤러는
	// 아직 아무것도 지우지 않는다. 답은 ActionConfirmYes 로 온다.
	ActionRemoveAllSafe
	// ActionRefresh는 다시 fetch 한다. r.
	ActionRefresh
	// ActionConfirmYes는 물음에 y 로 답했다. 물음은 safe 전부 삭제뿐이므로 컨트롤러는 그것을 한다.
	ActionConfirmYes
	// ActionConfirmNo는 물음을 거두었다. n 이나 그 밖의 키.
	ActionConfirmNo
)

// String은 시험 실패 문구에 쓴다.
func (a Action) String() string {
	switch a {
	case ActionNone:
		return "none"
	case ActionQuit:
		return "quit"
	case ActionOpen:
		return "open"
	case ActionRemoveSelected:
		return "remove-selected"
	case ActionRemoveAllSafe:
		return "remove-all-safe"
	case ActionRefresh:
		return "refresh"
	case ActionConfirmYes:
		return "confirm-yes"
	case ActionConfirmNo:
		return "confirm-no"
	default:
		return "action(" + strconv.Itoa(int(a)) + ")"
	}
}

// Update는 키 하나를 모델에 적용하고 컨트롤러가 할 일을 돌려준다. 순수 함수다.
//
// 키 표는 docs/PLAN.md 의 것이다. 물음이 떠 있으면 어떤 키든 그 답으로 읽는다. y 만 승낙이고 나머지는 전부
// 거절이다. 지우는 일은 되돌릴 수 없으므로 엉뚱한 키가 승낙으로 읽히면 안 된다. 다만 Ctrl-C 는 물음 중에도
// 닫기다. 사람이 나가려는 뜻을 물음이 삼키면 안 된다.
//
// d 는 선택한 행이 safe 일 때만 지운다. 아니면 이유를 Message 에 남기고 아무것도 시키지 않는다. 화면이 왜 안
// 지우는지 말해 주어야 사람이 --force 를 찾아 나서지 않는다. D 는 safe 가 하나라도 있어야 묻고, 없으면 그렇다고
// 말한다.
func Update(m Model, k tui.Key) (Model, Action) {
	// 창 크기 변경은 키가 아니다. 모델은 그대로 두고 컨트롤러가 새 크기로 다시 그린다.
	if k.Kind == tui.KeyResize {
		return m, ActionNone
	}
	if m.Confirm != nil {
		return answerConfirm(m, k)
	}
	// 새 키가 오면 지난 안내는 치운다. 안내는 그 키에 대한 답이지 붙박이가 아니다.
	m.Message = ""
	switch k.Kind {
	case tui.KeyCtrlC, tui.KeyEsc:
		return m, ActionQuit
	case tui.KeyUp:
		return moveCursor(m, -1), ActionNone
	case tui.KeyDown:
		return moveCursor(m, 1), ActionNone
	case tui.KeyEnter:
		if _, ok := m.Selected(); !ok {
			return m, ActionNone
		}
		return m, ActionOpen
	case tui.KeyRune:
		switch k.Rune {
		case 'q':
			return m, ActionQuit
		case 'k':
			return moveCursor(m, -1), ActionNone
		case 'j':
			return moveCursor(m, 1), ActionNone
		case 'r':
			return m, ActionRefresh
		case 'd':
			return removeSelected(m)
		case 'D':
			return removeAllSafe(m)
		}
	}
	return m, ActionNone
}

// answerConfirm은 물음에 대한 답을 읽는다. 어느 답이든 물음은 내린다.
func answerConfirm(m Model, k tui.Key) (Model, Action) {
	m.Confirm = nil
	m.Message = ""
	switch {
	case k.Kind == tui.KeyCtrlC:
		return m, ActionQuit
	case k.Kind == tui.KeyRune && (k.Rune == 'y' || k.Rune == 'Y'):
		return m, ActionConfirmYes
	default:
		return m, ActionConfirmNo
	}
}

func moveCursor(m Model, delta int) Model {
	m.Cursor += delta
	return clampCursor(m)
}

// clampCursor는 커서를 행 안에 둔다. 행이 줄어든 뒤(삭제, 다시 모으기)에도 커서가 표 밖을 가리키지 않게 한다.
func clampCursor(m Model) Model {
	if len(m.Rows) == 0 {
		m.Cursor = 0
		return m
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
	if m.Cursor >= len(m.Rows) {
		m.Cursor = len(m.Rows) - 1
	}
	return m
}

func removeSelected(m Model) (Model, Action) {
	row, ok := m.Selected()
	if !ok {
		m.Message = "no worktree selected"
		return m, ActionNone
	}
	if row.Verdict != judge.Safe {
		m.Message = row.Label() + " is " + row.Verdict.String() + " (" + row.Detail + "); only safe worktrees can be removed"
		return m, ActionNone
	}
	return m, ActionRemoveSelected
}

func removeAllSafe(m Model) (Model, Action) {
	count := len(m.SafeRows())
	if count == 0 {
		m.Message = "no safe worktrees to remove"
		return m, ActionNone
	}
	m.Confirm = &Confirm{Count: count, Rows: m.SafeRows()}
	return m, ActionRemoveAllSafe
}

// verdictWidth는 판정 열의 폭이다. 가장 긴 이름(blocked)에 맞춘다. 고정 폭이어야 열이 흔들리지 않는다.
const verdictWidth = 7

// maxLabelWidth는 이름 열의 상한이다. 브랜치 이름이 아무리 길어도 설명 열이 화면 밖으로 밀리지 않게 한다.
const maxLabelWidth = 40

// keyHint는 제목 줄 오른쪽의 키 안내다. 삭제 키는 마지막 줄에 있다. 되돌릴 수 없는 키는 눈에 띄는 자리에 둔다.
const keyHint = "↑↓ move  ⏎ open  r refresh"

// Render는 모델을 줄 목록으로 그린다. 순수 함수다. 그림은 docs/PLAN.md 의 것이다.
//
// 제목 줄, 빈 줄, 행들, 빈 줄, 마지막 줄이다. 행이 창보다 많으면 커서가 보이도록 위를 잘라 낸다. 잘라 내는 양은
// 커서 자리에서 바로 계산하므로 모델에 스크롤 값이 따로 없다. 창이 작아 빈 줄을 둘 자리가 없으면 빈 줄부터
// 뺀다. 제목과 마지막 줄은 끝까지 남긴다. 무엇을 보는지와 무엇을 할 수 있는지가 행보다 먼저다.
//
// 줄의 폭은 여기서 맞추지 않는다. tui.Frame 이 cols 에 맞춰 자른다. 다만 커서 행은 반전이 창 끝까지 이어지도록
// 공백으로 채운다.
func Render(m Model, cols, rows int) []string {
	if rows <= 0 || cols <= 0 {
		return nil
	}
	lines := []string{title(m, cols)}
	if rows == 1 {
		return lines
	}
	height, padded := layout(rows)
	if padded {
		lines = append(lines, "")
	}
	lines = append(lines, list(m, cols, height)...)
	if padded {
		lines = append(lines, "")
	}
	return append(lines, footer(m))
}

// layout은 행 목록에 줄 몇 개를 줄지와 빈 줄을 둘지 정한다. 제목과 마지막 줄은 언제나 있다.
func layout(rows int) (height int, padded bool) {
	if rows >= 5 {
		return rows - 4, true
	}
	return rows - 2, false
}

// title은 제목 줄이다. 왼쪽은 저장소와 worktree 수, 오른쪽은 herdr 상태와 fetch 상태와 키 안내다.
func title(m Model, cols int) string {
	count := strconv.Itoa(len(m.Rows)) + " worktrees"
	if len(m.Rows) == 1 {
		count = "1 worktree"
	}
	left := " " + tui.Bold(m.RepoName) + " · " + count

	var right []string
	if m.HerdrUnavailable {
		right = append(right, tui.Dim("herdr unavailable"))
	}
	switch m.Fetch {
	case FetchFetching:
		right = append(right, "fetching…")
	case FetchDone:
		if m.FetchedAgo != "" {
			right = append(right, "fetched "+m.FetchedAgo)
		} else {
			right = append(right, "fetched")
		}
	case FetchFailed:
		right = append(right, tui.Fg("fetch failed", tui.Red))
	}
	right = append(right, keyHint)
	status := strings.Join(right, "    ")

	// 오른쪽을 창 끝에 붙인다. 자리가 모자라면 두 칸만 띄우고 Frame 이 꼬리를 자르게 둔다.
	gap := cols - tui.Width(left) - tui.Width(status) - 1
	if gap < 2 {
		gap = 2
	}
	return left + strings.Repeat(" ", gap) + status
}

// list는 행들을 height 줄에 그린다. 모자란 줄은 빈 줄로 채워 마지막 줄이 늘 창 바닥에 오게 한다.
func list(m Model, cols, height int) []string {
	if height <= 0 {
		return nil
	}
	lines := make([]string, 0, height)
	if len(m.Rows) == 0 {
		lines = append(lines, tui.Dim(" no worktrees"))
	}

	labelWidth := 1
	for _, row := range m.Rows {
		if w := tui.Width(row.Label()); w > labelWidth {
			labelWidth = w
		}
	}
	if labelWidth > maxLabelWidth {
		labelWidth = maxLabelWidth
	}

	// 커서가 창 아래로 나가면 그만큼 위를 잘라 낸다. 커서는 언제나 보이는 마지막 줄 안에 있다.
	offset := 0
	if m.Cursor >= height {
		offset = m.Cursor - height + 1
	}
	for i := offset; i < len(m.Rows) && len(lines) < height; i++ {
		line := rowLine(m.Rows[i], labelWidth)
		if i == m.Cursor {
			line = tui.Reverse(padRight(line, cols))
		}
		lines = append(lines, line)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

// rowLine은 행 하나다. 판정 열(고정 폭, 색), 이름 열(맞춘 폭), 설명 열.
func rowLine(row Row, labelWidth int) string {
	verdict := padRight(row.Verdict.String(), verdictWidth)
	switch row.Verdict {
	case judge.Safe:
		verdict = tui.Fg(verdict, tui.Green)
	case judge.Review:
		verdict = tui.Fg(verdict, tui.Yellow)
	case judge.Blocked:
		verdict = tui.Dim(verdict)
	}
	label := padRight(tui.Truncate(row.Label(), labelWidth), labelWidth)
	return " " + verdict + "  " + label + "  " + row.Detail
}

// footer는 마지막 줄이다. 물음이 먼저고, 안내가 다음이고, 그 밖에는 safe 수와 삭제 키다.
func footer(m Model) string {
	switch {
	case m.Confirm != nil:
		noun := "worktrees"
		if m.Confirm.Count == 1 {
			noun = "worktree"
		}
		return " Remove " + strconv.Itoa(m.Confirm.Count) + " " + noun + "? y/N"
	case m.Message != "":
		return " " + m.Message
	default:
		return " " + strconv.Itoa(len(m.SafeRows())) + " safe · d remove selected · D remove all safe · q close"
	}
}

// padRight는 표시 폭이 width 가 되도록 오른쪽을 공백으로 채운다. 꾸밈은 폭에 들지 않는다.
func padRight(s string, width int) string {
	if w := tui.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// Ago는 지난 시간을 "3s ago" 처럼 짧게 적는다. 제목 줄의 fetch 시각에 쓴다.
//
// 초 단위는 1분까지만이다. 화면은 상태가 바뀔 때만 다시 그리므로 이 문구는 살아 움직이지 않는다. 그래서
// 정확한 초보다 대강의 크기(방금, 몇 분 전, 몇 시간 전)가 사람에게 더 정직하다.
func Ago(since time.Duration) string {
	switch {
	case since < time.Second:
		return "just now"
	case since < time.Minute:
		return strconv.Itoa(int(since/time.Second)) + "s ago"
	case since < time.Hour:
		return strconv.Itoa(int(since/time.Minute)) + "m ago"
	default:
		return strconv.Itoa(int(since/time.Hour)) + "h ago"
	}
}
