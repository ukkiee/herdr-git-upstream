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
)

func (k CandidateKind) String() string {
	switch k {
	case CandidateCurrent:
		return "current"
	case CandidateUpstream:
		return "upstream"
	case CandidateDefault:
		return "default"
	default:
		return "merge target"
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
	RepoName     string
	Name         string
	NameSelected bool
	UserEdited   bool
	PathRoot     string
	Candidates   []Candidate
	Selected     int
	MenuOpen     bool
	MenuIndex    int
	// MenuRef keeps the highlight on the same candidate when async lookup inserts a row.
	MenuRef string
	Focus   FocusArea
	Message string
	Busy    bool
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
		if index, ok := m.menuIndex(); ok {
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
		m.Name = AutoName(base.nameRef(), func(name string) bool { return m.Taken[name] })
		m.NameSelected = true
	}
	return m
}

// UpdateCreate는 순수 키 처리다. 외부 생성은 컨트롤러만 실행하고, 생성 중 취소도 컨트롤러에 알려
// 생성 결과와 upstream 정리가 끝난 뒤 닫도록 한다.
func UpdateCreate(m CreateModel, k tui.Key) (CreateModel, CreateAction) {
	if k.Kind == tui.KeyEsc && m.MenuOpen && !m.Busy {
		m.MenuOpen = false
		return m, CreateNone
	}
	if k.Kind == tui.KeyEsc || k.Kind == tui.KeyCtrlC {
		return m, CreateCancel
	}
	if k.Kind == tui.KeyResize || m.Busy {
		return m, CreateNone
	}
	m.Message = ""
	switch k.Kind {
	case tui.KeyTab:
		m.MenuOpen = false
		if m.Focus == FocusName {
			m.Focus = FocusBase
		} else {
			m.Focus = FocusName
		}
	case tui.KeyEnter:
		if m.Focus == FocusBase {
			if m.MenuOpen {
				m.MenuOpen = false
				index, ok := m.menuIndex()
				if !ok {
					m.Message = "base list changed; choose again"
					return m, CreateNone
				}
				m.Selected = index
				m = m.autoName()
			} else if base, ok := m.base(); ok {
				m.MenuOpen, m.MenuIndex, m.MenuRef = true, m.Selected, base.Ref
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
			if m.NameSelected {
				m.Name = ""
			} else if m.Name != "" {
				_, size := utf8.DecodeLastRuneInString(m.Name)
				m.Name = m.Name[:len(m.Name)-size]
			}
			m.NameSelected, m.UserEdited = false, true
		}
	case tui.KeyRune:
		if m.Focus == FocusName && unicode.IsPrint(k.Rune) {
			if m.NameSelected {
				m.Name = ""
			}
			m.Name += string(k.Rune)
			m.NameSelected, m.UserEdited = false, true
		}
	case tui.KeyUp, tui.KeyDown:
		if m.Focus == FocusBase && m.MenuOpen && len(m.Candidates) > 0 {
			index, ok := m.menuIndex()
			if !ok {
				index = m.Selected
			}
			if k.Kind == tui.KeyUp {
				index--
			} else {
				index++
			}
			m.MenuIndex = max(0, min(index, len(m.Candidates)-1))
			m.MenuRef = m.Candidates[m.MenuIndex].Ref
		}
	}
	return m, CreateNone
}

func (m CreateModel) menuIndex() (int, bool) {
	for i, candidate := range m.Candidates {
		if candidate.Ref == m.MenuRef {
			return i, true
		}
	}
	return 0, false
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
	name := m.Name
	if name == "" {
		name = " "
	}
	if m.Focus == FocusName && m.NameSelected {
		name = tui.Reverse(name)
	}
	base, available := m.base()
	label, detail := "no base branch available", ""
	if available {
		label, detail = base.Label, base.detail()
	}
	chevron := "▾"
	if m.MenuOpen {
		chevron = "▴"
	}
	fieldWidth := max(0, inside-13)
	field := "[" + padRight(tui.Truncate(label, fieldWidth), fieldWidth) + " " + chevron + "]"
	if m.Focus == FocusBase && !m.MenuOpen {
		field = tui.Reverse(field)
	}
	content := []string{"", " Branch  [" + name + "]", " Path    " + PathPreview(m.PathRoot, m.RepoName, m.Name), "", " Base    " + field}
	if m.MenuOpen {
		visible := min(len(m.Candidates), max(0, rows-11))
		index, found := m.menuIndex()
		if !found {
			index = min(m.Selected, max(0, len(m.Candidates)-1))
		}
		offset := max(0, index-visible+1)
		menuWidth := max(0, inside-11)
		indent := strings.Repeat(" ", 9)
		content = append(content, indent+"┌"+strings.Repeat("─", menuWidth)+"┐")
		for i := offset; i < len(m.Candidates) && i < offset+visible; i++ {
			candidate := m.Candidates[i]
			marker := "  "
			if found && i == index {
				marker = "▸ "
			}
			minimumLabel := max(0, min(12, menuWidth-4))
			labelWidth := min(26, max(minimumLabel, menuWidth-tui.Width(candidate.detail())-4))
			line := marker + padRight(tui.Truncate(candidate.Label, labelWidth), labelWidth) + "  " + candidate.detail()
			line = padRight(tui.Truncate(line, menuWidth), menuWidth)
			if found && i == index {
				line = tui.Reverse(line)
			}
			content = append(content, indent+"│"+line+"│")
		}
		content = append(content, indent+"└"+strings.Repeat("─", menuWidth)+"┘")
	} else {
		content = append(content, "         "+detail)
	}
	message := m.Message
	if m.Busy && message == "" {
		message = "creating…"
	}
	footer := " Tab switch · Enter create · Esc cancel"
	if m.Focus == FocusBase {
		footer = " Tab switch · Enter open · Esc cancel"
	}
	if m.MenuOpen {
		footer = " ↑↓ move · Enter select · Esc close · Tab name"
	}
	if m.Busy {
		footer = " Creating… · Esc waits for completion"
	}
	content = append(content, " "+message, footer)
	if len(content) > rows-2 {
		content = append(content[:rows-3], content[len(content)-1])
	}
	title := tui.Truncate(" New worktree ", inside)
	repoWidth := max(0, inside-tui.Width(title))
	repo := tui.Truncate(" "+m.RepoName+" ", repoWidth)
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
