package worktreeui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/tui"
)

// PathSlug는 herdr 의 branch_to_path_slug 와 같다. ASCII 영숫자 외의 연속 문자는 대시 하나가 되고,
// 이름이 전부 사라지면 herdr 와 같은 worktree 를 쓴다. 실제 경로 선택은 herdr 에 맡기며 이것은 미리보기다.
func PathSlug(branch string) string {
	var out strings.Builder
	dash := false
	for _, r := range branch {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if dash && out.Len() > 0 {
				out.WriteByte('-')
			}
			out.WriteRune(unicode.ToLower(r))
			dash = false
		} else {
			dash = true
		}
	}
	if out.Len() == 0 {
		return "worktree"
	}
	return out.String()
}

// AutoName은 기준에서 아직 쓰이지 않는 브랜치 이름을 고른다. 전체 참조는 로컬/원격 이름을 구분한다.
// 짧은 feature/foo 는 로컬 이름으로 보존한다. 원격 이름에 slash 가 있으면 호출자는 Fetch.RemoteRef
// (refs/heads/<실제 브랜치>)를 넘겨 원격 경계의 추측을 피한다.
func AutoName(base string, taken func(string) bool) string {
	name := strings.TrimPrefix(base, "refs/heads/")
	if remote, ok := strings.CutPrefix(base, "refs/remotes/"); ok {
		_, name, _ = strings.Cut(remote, "/")
	}
	if name == "" {
		name = "worktree"
	}
	if taken == nil || !taken(name) {
		return name
	}
	stem, number := name, 2
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		if suffix, err := strconv.Atoi(name[i+1:]); err == nil && suffix >= 2 && suffix < int(^uint(0)>>1) {
			stem, number = name[:i], suffix+1
		}
	}
	for {
		candidate := stem + "-" + strconv.Itoa(number)
		if !taken(candidate) {
			return candidate
		}
		number++
	}
}

// PathPreview는 herdr 의 worktree 뿌리와 저장소 이름 아래에 슬러그를 잇고 홈 아래의 경로만 줄인다.
func PathPreview(root, repoName, branch string) string {
	path := filepath.Join(root, repoName, PathSlug(branch))
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return filepath.Join("~", rel)
		}
	}
	return path
}

type CandidateKind int

const (
	CandidateCurrent CandidateKind = iota
	CandidateUpstream
	CandidateDefault
	CandidateMergeTarget
	CandidateLocal
	CandidateRemote
)

func (k CandidateKind) String() string {
	switch k {
	case CandidateCurrent:
		return "current"
	case CandidateUpstream:
		return "upstream"
	case CandidateDefault:
		return "default"
	case CandidateMergeTarget:
		return "merge target"
	case CandidateLocal:
		return "local"
	default:
		return "remote"
	}
}

// Candidate는 생성 기준이다. Label 은 표시용, Ref 는 중복 제거와 git 호출에 쓰는 전체 참조다.
type Candidate struct {
	Label  string
	Ref    string
	Kind   CandidateKind
	Fetch  gitrepo.Upstream
	Status string
}

func (c Candidate) nameRef() string {
	if c.Fetch.RemoteRef != "" {
		return c.Fetch.RemoteRef
	}
	return c.Ref
}

func (c Candidate) detail() string {
	detail := c.Kind.String()
	if c.Status != "" {
		detail += " · " + c.Status
	}
	return detail
}

type FocusArea int

const (
	FocusName FocusArea = iota
	FocusBase
)

type CreateModel struct {
	RepoName string
	Name     string
	// NameCursor is a rune offset, so edits never split a UTF-8 character.
	NameCursor int
	UserEdited bool
	PathRoot   string
	Candidates []Candidate
	Selected   int
	MenuOpen   bool
	MenuIndex  int
	MenuQuery  string
	// MenuRef keeps the highlight on the same candidate when async lookup inserts a row.
	MenuRef       string
	Focus         FocusArea
	Message       string
	MessageOpen   bool
	MessageOffset int
	// The controller records the latest terminal size for message scrolling.
	Cols, Rows int
	Busy       bool
	// Taken은 자동 이름을 고를 때 보는 읽기 전용 집합이다. 갱신할 때는 새 집합으로 바꾼다.
	Taken map[string]bool
}

type CreateAction int

const (
	CreateNone CreateAction = iota
	CreateSubmit
	CreateCancel
)

func (m CreateModel) base() (Candidate, bool) {
	if m.Selected < 0 || m.Selected >= len(m.Candidates) {
		return Candidate{}, false
	}
	return m.Candidates[m.Selected], true
}

func (m CreateModel) autoName() CreateModel {
	if m.MenuOpen {
		if m.MenuRef == "" {
			m = m.filterMenu()
		} else if index, ok := m.menuIndex(); ok {
			m.MenuIndex = index
		} else {
			m.MenuOpen = false
			m.Message = "base list changed; reopen Base"
		}
	}
	if m.UserEdited {
		return m
	}
	if base, ok := m.base(); ok {
		name := AutoName(base.nameRef(), func(name string) bool { return m.Taken[name] })
		if name != m.Name {
			m.Name = name
			m.NameCursor = utf8.RuneCountInString(name)
		}
	}
	return m
}

// UpdateCreate는 순수 키 처리다. 외부 생성은 컨트롤러만 실행하고, 생성 중 취소도 컨트롤러에 알려
// 생성 결과와 upstream 정리가 끝난 뒤 닫도록 한다.
func UpdateCreate(m CreateModel, k tui.Key) (CreateModel, CreateAction) {
	if k.Kind == tui.KeyCtrlC {
		return m, CreateCancel
	}
	if m.MessageOpen {
		switch k.Kind {
		case tui.KeyEsc, tui.KeyEnter, tui.KeyF1:
			m.MessageOpen = false
		case tui.KeyUp:
			m.MessageOffset = max(0, m.MessageOffset-1)
		case tui.KeyDown:
			cols, rows := m.Cols, m.Rows
			if cols == 0 || rows == 0 {
				cols, rows = 64, 18
			}
			wrapped := wrapCreateMessage(m.Message, max(1, min(cols, 64)-4))
			m.MessageOffset = min(max(0, len(wrapped)-max(1, rows-4)), m.MessageOffset+1)
		}
		return m, CreateNone
	}
	if k.Kind == tui.KeyF1 && m.Message != "" && !m.Busy {
		m.MessageOpen, m.MessageOffset = true, 0
		return m, CreateNone
	}
	if k.Kind == tui.KeyEsc && m.MenuOpen && !m.Busy {
		m.MenuOpen = false
		m.MenuQuery = ""
		return m, CreateNone
	}
	if k.Kind == tui.KeyEsc || k.Kind == tui.KeyCtrlC {
		return m, CreateCancel
	}
	if k.Kind == tui.KeyResize || m.Busy {
		return m, CreateNone
	}
	m.Message = ""
	name := []rune(m.Name)
	m.NameCursor = max(0, min(m.NameCursor, len(name)))
	switch k.Kind {
	case tui.KeyTab:
		m.MenuOpen = false
		m.MenuQuery = ""
		if m.Focus == FocusName {
			m.Focus = FocusBase
		} else {
			m.Focus = FocusName
		}
	case tui.KeyEnter:
		if m.Focus == FocusBase {
			if m.MenuOpen {
				index, ok := m.menuIndex()
				if !ok {
					m.Message = "no matching base branch"
					return m, CreateNone
				}
				m.MenuOpen = false
				m.MenuQuery = ""
				m.Selected = index
				m = m.autoName()
			} else if base, ok := m.base(); ok {
				m.MenuOpen, m.MenuIndex, m.MenuRef = true, m.Selected, base.Ref
				m.MenuQuery = ""
			} else {
				m.Message = "no base branch available"
			}
			return m, CreateNone
		}
		if strings.TrimSpace(m.Name) == "" {
			m.Message = "enter a branch name"
			return m, CreateNone
		}
		base, ok := m.base()
		if !ok {
			m.Message = "no base branch available"
			return m, CreateNone
		}
		if base.Kind != CandidateCurrent && base.Status != "up to date" {
			if base.Status == "fetch failed" {
				m.Message = "base fetch failed; reopen the popup to retry"
			} else {
				m.Message = "waiting for selected base fetch"
			}
			return m, CreateNone
		}
		return m, CreateSubmit
	case tui.KeyBackspace:
		if m.Focus == FocusName {
			if m.NameCursor > 0 {
				m.Name = string(name[:m.NameCursor-1]) + string(name[m.NameCursor:])
				m.NameCursor--
			}
			m.UserEdited = true
		} else if m.MenuOpen && m.MenuQuery != "" {
			_, size := utf8.DecodeLastRuneInString(m.MenuQuery)
			m.MenuQuery = m.MenuQuery[:len(m.MenuQuery)-size]
			m = m.filterMenu()
		}
	case tui.KeyRune:
		if m.Focus == FocusName && unicode.IsPrint(k.Rune) {
			m.Name = string(name[:m.NameCursor]) + string(k.Rune) + string(name[m.NameCursor:])
			m.NameCursor++
			m.UserEdited = true
		} else if m.MenuOpen && unicode.IsPrint(k.Rune) {
			m.MenuQuery += string(k.Rune)
			m = m.filterMenu()
		}
	case tui.KeyLeft, tui.KeyRight:
		if m.Focus == FocusName {
			if k.Kind == tui.KeyLeft {
				m.NameCursor = max(0, m.NameCursor-1)
			} else {
				m.NameCursor = min(len(name), m.NameCursor+1)
			}
		}
	case tui.KeyUp, tui.KeyDown:
		if m.Focus == FocusBase && m.MenuOpen {
			matches := m.menuMatches()
			if len(matches) == 0 {
				break
			}
			index := m.menuPosition(matches)
			if k.Kind == tui.KeyUp {
				index--
			} else {
				index++
			}
			m.MenuIndex = matches[max(0, min(index, len(matches)-1))]
			m.MenuRef = m.Candidates[m.MenuIndex].Ref
		}
	}
	return m, CreateNone
}

func (m CreateModel) menuIndex() (int, bool) {
	for i, candidate := range m.Candidates {
		if candidate.Ref == m.MenuRef && strings.Contains(strings.ToLower(candidate.Label), strings.ToLower(m.MenuQuery)) {
			return i, true
		}
	}
	return 0, false
}

func (m CreateModel) menuMatches() []int {
	query := strings.ToLower(m.MenuQuery)
	var matches []int
	for i, candidate := range m.Candidates {
		if strings.Contains(strings.ToLower(candidate.Label), query) {
			matches = append(matches, i)
		}
	}
	return matches
}

func (m CreateModel) menuPosition(matches []int) int {
	for position, index := range matches {
		if m.Candidates[index].Ref == m.MenuRef {
			return position
		}
	}
	return 0
}

// Filtering changes only the highlight; Enter commits the actual candidate index.
func (m CreateModel) filterMenu() CreateModel {
	matches := m.menuMatches()
	if len(matches) == 0 {
		m.MenuIndex, m.MenuRef = -1, ""
		return m
	}
	m.MenuIndex = matches[m.menuPosition(matches)]
	m.MenuRef = m.Candidates[m.MenuIndex].Ref
	return m
}

// inputView scrolls around the cursor and reserves its display width before truncation.
func inputView(value string, cursor, width int, focused bool) string {
	if width <= 0 {
		return ""
	}
	if !focused {
		return tui.Truncate(value, width)
	}
	runes := []rune(value)
	cursor = max(0, min(cursor, len(runes)))
	left, caret, right := string(runes[:cursor]), " ", ""
	if cursor < len(runes) {
		caret, right = string(runes[cursor]), string(runes[cursor+1:])
	}
	if tui.Width(caret) > width {
		caret = " "
	}
	remaining := width - tui.Width(caret)
	leftWidth := tui.Width(left)
	for leftWidth > remaining && left != "" {
		_, size := utf8.DecodeRuneInString(left)
		leftWidth -= tui.Width(left[:size])
		left = left[size:]
	}
	if focused {
		caret = tui.Reverse(caret)
	}
	return left + caret + tui.Truncate(right, remaining-leftWidth)
}

// At the end of a bracketed input, the closing bracket occupies the cursor cell.
// No synthetic space becomes part of the displayed value, even when focus moves away.
func branchInputView(value string, cursor, width int, focused bool) string {
	if focused && cursor >= utf8.RuneCountInString(value) {
		return "[" + inputView(value+"]", utf8.RuneCountInString(value), width+1, true)
	}
	return "[" + inputView(value, cursor, width, focused) + "]"
}

func createMenuBorder(text string, width int, top bool) string {
	left, right := "└", "┘"
	if top {
		left, right = "┌", "┐"
	}
	text = tui.Truncate(" "+text+" ", width)
	return left + text + strings.Repeat("─", max(0, width-tui.Width(text))) + right
}

// Match against the full label before shortening it, so even a partly visible match is emphasized.
// Preserve both the namespace and suffix when a branch name exceeds the compact field.
func compactBranchLabel(label, query string, width int) string {
	spans := branchMatchSpans(label, query)
	if tui.Width(label) <= width || width < 3 {
		part := tui.Truncate(label, width)
		return emphasizeBranchPart(label, spans, 0, len(part))
	}
	left := tui.Truncate(label, width/2)
	tailWidth := width - tui.Width(left) - 1
	right := label
	rightWidth := tui.Width(right)
	for rightWidth > tailWidth && right != "" {
		_, size := utf8.DecodeRuneInString(right)
		rightWidth -= tui.Width(right[:size])
		right = right[size:]
	}
	return emphasizeBranchPart(label, spans, 0, len(left)) + "…" +
		emphasizeBranchPart(label, spans, len(label)-len(right), len(label))
}

// Unicode lowercasing can change byte lengths (for example, K becomes k). Match runes, then
// map back to byte offsets in the original label so styling preserves its spelling and UTF-8.
func branchMatchSpans(label, query string) [][2]int {
	if query == "" {
		return nil
	}
	lower, needle := []rune(strings.ToLower(label)), []rune(strings.ToLower(query))
	var offsets []int
	for offset := range label {
		offsets = append(offsets, offset)
	}
	offsets = append(offsets, len(label))
	var spans [][2]int
	for i := 0; i+len(needle) <= len(lower); i++ {
		matched := true
		for j, r := range needle {
			if lower[i+j] != r {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		start, end := offsets[i], offsets[i+len(needle)]
		if last := len(spans) - 1; last >= 0 && start <= spans[last][1] {
			spans[last][1] = end
		} else {
			spans = append(spans, [2]int{start, end})
		}
	}
	return spans
}

func emphasizeBranchPart(label string, spans [][2]int, start, end int) string {
	var out strings.Builder
	position := start
	for _, span := range spans {
		from, to := max(start, span[0]), min(end, span[1])
		if from >= to {
			continue
		}
		out.WriteString(label[position:from])
		// These styles reset only themselves, preserving the selected row's reverse video.
		out.WriteString(tui.Bold(tui.Fg(label[from:to], tui.Cyan)))
		position = to
	}
	out.WriteString(label[position:end])
	return out.String()
}

// wrapCreateMessage keeps words together where possible and splits long paths on rune boundaries.
func wrapCreateMessage(message string, width int) []string {
	width = max(2, width)
	message = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) || r == '\n' {
			return r
		}
		if unicode.IsSpace(r) {
			return ' '
		}
		return -1
	}, message)
	var lines []string
	for _, paragraph := range strings.Split(message, "\n") {
		line := ""
		for _, word := range strings.Fields(paragraph) {
			if line != "" && tui.Width(line)+1+tui.Width(word) > width {
				lines = append(lines, line)
				line = ""
			}
			for tui.Width(word) > width {
				part := tui.Truncate(word, width)
				lines = append(lines, part)
				word = word[len(part):]
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		lines = append(lines, line)
	}
	return lines
}

// RenderCreate는 화면 가운데 테두리 상자를 그린다. 작은 창에서는 후보 목록을 줄여 선택 행과 키 안내를 남긴다.
func RenderCreate(m CreateModel, cols, rows int) []string {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	lines := make([]string, rows)
	if cols < 10 || rows < 8 {
		lines[0] = tui.Truncate("New worktree", cols)
		return lines
	}
	width := min(cols, 64)
	inside := width - 2
	if m.MessageOpen {
		wrapped := wrapCreateMessage(m.Message, inside-2)
		visible := min(len(wrapped), rows-4)
		offset := max(0, min(m.MessageOffset, len(wrapped)-visible))
		content := []string{" Message " + strconv.Itoa(offset+1) + "-" + strconv.Itoa(offset+visible) + "/" + strconv.Itoa(len(wrapped))}
		for _, line := range wrapped[offset : offset+visible] {
			content = append(content, " "+line)
		}
		content = append(content, " ↑↓ scroll · Enter/Esc back")
		return renderCreateBox(m.RepoName, content, cols, rows)
	}
	name := branchInputView(m.Name, m.NameCursor, inside-11, m.Focus == FocusName && !m.Busy)
	base, available := m.base()
	label, detail := "no base branch available", ""
	if available {
		label, detail = base.Label, base.detail()
	}
	fieldWidth := max(0, min(34, inside-13))
	field := "[" + compactBranchLabel(label, "", fieldWidth) + " ▾]"
	if m.Focus == FocusBase && !m.MenuOpen {
		field = tui.Reverse(field)
	}
	content := []string{"", " Branch  " + name, " Path    " + PathPreview(m.PathRoot, m.RepoName, m.Name), ""}
	if m.MenuOpen {
		matches := m.menuMatches()
		visible := min(len(matches), 4, max(1, rows-11))
		index := m.menuPosition(matches)
		_, found := m.menuIndex()
		offset := max(0, index-visible+1)
		menuWidth := max(0, min(36, inside-11))
		indent := strings.Repeat(" ", 9)
		search := inputView(m.MenuQuery, utf8.RuneCountInString(m.MenuQuery), menuWidth-4, !m.Busy)
		if m.MenuQuery == "" {
			search += tui.Dim(tui.Truncate("Search branches…", menuWidth-5))
		}
		content = append(content, " Base    "+createMenuBorder("/ "+search, menuWidth, true))
		positionNumber := 0
		if found {
			positionNumber = index + 1
		}
		position := strconv.Itoa(positionNumber) + "/" + strconv.Itoa(len(matches))
		if m.MenuQuery != "" {
			position += " · " + strconv.Itoa(len(m.Candidates)) + " total"
		}
		for i := offset; i < len(matches) && i < offset+visible; i++ {
			candidate := m.Candidates[matches[i]]
			marker := "  "
			if found && i == index {
				marker = "▸ "
			}
			line := padRight(marker+compactBranchLabel(candidate.Label, m.MenuQuery, menuWidth-3), menuWidth)
			if found && i == index {
				line = tui.Reverse(line)
			}
			content = append(content, indent+"│"+line+"│")
		}
		if len(matches) == 0 {
			content = append(content, indent+"│"+padRight(tui.Truncate(" No matching branches", menuWidth), menuWidth)+"│")
		}
		content = append(content, indent+createMenuBorder(position, menuWidth, false))
		if found {
			content = append(content, indent+m.Candidates[matches[index]].detail())
		}
	} else {
		content = append(content, " Base    "+field, "         "+detail)
	}
	message := m.Message
	if m.Busy && message == "" {
		message = "creating…"
	}
	footer := " ←→ edit · Tab switch · Enter create · Esc cancel"
	if m.Focus == FocusBase {
		footer = " Tab switch · Enter open · Esc cancel"
	}
	if m.MenuOpen {
		footer = " ↑↓ move · Enter select · Esc close · Tab name"
		if tui.Width(footer) > inside {
			footer = " ↑↓ · Enter select · Esc close"
		}
	}
	if m.Busy {
		footer = " Creating… · Esc waits for completion"
	}
	messageLines := wrapCreateMessage(message, inside-2)
	if message != "" && !m.Busy {
		footer = " F1 message · Tab switch · Enter create · Esc cancel"
		if m.Focus == FocusBase {
			footer = " F1 message · Tab switch · Enter open · Esc cancel"
		}
		if m.MenuOpen {
			footer = " F1 message · ↑↓ move · Enter select · Esc close"
		}
	}
	// Use the available rows and offer the complete message separately when it cannot fit.
	messageSpace := max(1, rows-3-len(content))
	if len(messageLines) > messageSpace {
		messageLines = messageLines[:messageSpace]
		messageLines[len(messageLines)-1] = "F1: read full message"
	}
	for _, line := range messageLines {
		content = append(content, " "+line)
	}
	content = append(content, footer)
	if len(content) > rows-2 {
		content = append(content[:rows-3], content[len(content)-1])
	}
	return renderCreateBox(m.RepoName, content, cols, rows)
}

func renderCreateBox(repoName string, content []string, cols, rows int) []string {
	lines := make([]string, rows)
	width := min(cols, 64)
	inside := width - 2
	title := tui.Truncate(" New worktree ", inside)
	repoWidth := max(0, inside-tui.Width(title))
	repo := tui.Truncate(" "+repoName+" ", repoWidth)
	top := "┌" + title + strings.Repeat("─", max(0, inside-tui.Width(title)-tui.Width(repo))) + repo + "┐"
	box := []string{top}
	for _, line := range content {
		box = append(box, "│"+padRight(tui.Truncate(line, inside), inside)+"│")
	}
	box = append(box, "└"+strings.Repeat("─", inside)+"┘")
	topOffset := max(0, (rows-len(box))/2)
	left := strings.Repeat(" ", max(0, (cols-width)/2))
	for i, line := range box {
		if topOffset+i < rows {
			lines[topOffset+i] = left + line
		}
	}
	return lines
}
