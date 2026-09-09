// Package setup은 사용자가 herdr 설정에 붙여 넣을 조각을 만든다.
//
// 이 플러그인은 설정을 두 군데 손봐야 제 몫을 한다. 사이드바 행에 토큰을 넣어야 숫자가 보이고,
// 키를 묶어야 worktree 화면이 열린다. herdr에는 플러그인이 자기 설정을 끼워 넣는 통로가 없으므로
// 붙여 넣을 본문을 대신 만들어 보여 준다.
//
// 파일은 말없이 고치지 않는다. 남의 설정 파일을 손대는 플러그인은 신뢰를 잃는다. 출력만 하고
// 붙여 넣는 것은 사람이 한다. 대신 이미 들어 있는 것은 빼고 남은 것만 보여 주어, 두 번 붙여 넣는
// 실수를 막는다.
package setup

import (
	"strings"

	"herdr-git-upstream/internal/config"
	"herdr-git-upstream/internal/herdrconfig"
	"herdr-git-upstream/internal/herdrpaths"
)

// 사이드바 토큰이 들어갈 테이블과, 키에 묶을 액션 이름이다. herdr는 "<plugin-id>.<action-id>"로 부른다.
const (
	spacesTable       = "ui.sidebar.spaces"
	worktreesAction   = herdrpaths.PluginID + ".worktrees"
	newWorktreeAction = herdrpaths.PluginID + ".new-worktree"
)

// 사용자에게 보이는 안내 문장들. TOML 조각은 그대로 두고 문장만 README와 같은 언어로 적는다.
const (
	headerAdd      = " 에 아래를 더하세요."
	headerAllDone  = "설정이 모두 들어 있습니다."
	sidebarPartial = "이미 [" + spacesTable + "] 를 쓰고 있으니 아래 토큰 항목을 그 행에 더하세요."
	// 테이블은 정의되어 있는데 rows 가 없는 경우다. 헤더를 다시 내면 테이블이 두 번 정의되므로 rows 만 준다.
	sidebarTableOnly = "이미 [" + spacesTable + "] 테이블이 있으니 그 아래에 rows 를 더하세요."
	sidebarDone      = "사이드바 토큰은 이미 들어 있습니다."
	sidebarNoToken   = "보고할 토큰 이름이 모두 비어 있어 사이드바에 더할 것이 없습니다."
	keyDone          = "worktrees 키는 이미 묶여 있습니다."
	newWorktreeWhy   = "기준 브랜치를 고르는 생성 팝업. herdr 내장 팝업 대신 쓰려면 주석을 풀고 new_worktree 키를 비운다."
	mergeTarget      = "통합 브랜치가 origin/HEAD 가 아닌 저장소에서는 그 저장소에서 이렇게 알려 주세요.\n" +
		"  git config --add git-upstream.mergeTarget origin/develop"
	nextStep = "그다음: herdr config check && herdr server reload-config"
)

// tokenEntry는 사이드바 rows에 들어갈 토큰 항목 하나다.
type tokenEntry struct {
	name string
	fg   string
	bold bool
	dim  bool
}

// entries는 사이드바에 놓을 토큰을 순서대로 돌려준다. 이름이 빈 토큰은 보고하지 않으므로 뺀다.
//
// 색은 Catppuccin Mocha 팔레트다. 뒤처짐과 충돌은 손을 써야 하는 신호라 진하게, 끝난 작업(gone,
// merged)과 stale은 눈에 덜 띄는 회색으로 둔다.
func entries(cfg config.Resolved) []tokenEntry {
	all := []tokenEntry{
		{name: cfg.BehindToken, fg: "#f38ba8", bold: true},
		{name: cfg.AheadToken, fg: "#a6e3a1"},
		{name: cfg.GoneToken, fg: "#6c7086"},
		{name: cfg.MergedToken, fg: "#6c7086"},
		{name: cfg.CatchupToken, fg: "#fab387", bold: true},
		{name: cfg.StaleToken, fg: "#6c7086", dim: true},
	}
	out := make([]tokenEntry, 0, len(all))
	for _, e := range all {
		if e.name != "" {
			out = append(out, e)
		}
	}
	return out
}

// SidebarConfigured는 우리 토큰이 하나라도 config.toml에 요청되어 있는지 답한다.
// status가 "아무것도 안 보인다"는 물음에 답할 때 쓴다.
//
// `$이름`을 통째로 찾는다. 부분 일치로 보면 다른 플러그인의 `$merged_prs`가 우리 `$merged`로 잡혀,
// 우리 토큰이 하나도 없는데 설정됐다고 답하게 된다.
func SidebarConfigured(cfg config.Resolved, scan herdrconfig.Scan) bool {
	for _, name := range cfg.TokenNames() {
		if scan.HasToken(name) {
			return true
		}
	}
	return false
}

// KeyBound는 worktree 화면을 여는 액션이 키에 묶여 있는지 답한다.
func KeyBound(scan herdrconfig.Scan) bool {
	return scan.Contains(worktreesAction)
}

// Render는 붙여 넣을 본문을 만든다. configPath는 첫 줄에 보여 줄 파일 자리다.
//
// 절은 넷이다. 사이드바 토큰, worktrees 키, 주석 처리한 생성 팝업 키, 통합 브랜치 안내.
// 앞의 셋은 이미 들어 있으면 줄이거나 빼고, 마지막은 저장소마다 다른 일이라 언제나 붙인다.
//
// "모두 들어 있다"는 필수 둘(사이드바 토큰, worktrees 키)로만 판정하고, 그때는 통합 브랜치 안내만
// 붙인다. 생성 팝업 키는 옵트인이라 빠진 설정이 아니다. 그것까지 판정에 넣으면 출력을 그대로 붙여
// 넣은 사람(주석 블록을 주석인 채로 둔 기본 상태)이 매번 "더하세요"와 reload 안내를 받게 되어, 더할
// 것이 없는데도 설정을 다시 반영하라는 말을 듣는다. 대신 사이드바와 키를 손으로 넣은 사람은 여기서
// 생성 팝업 제안을 보지 못하는데, 옵트인 기능을 알리는 일은 README 의 몫이다.
func Render(cfg config.Resolved, scan herdrconfig.Scan, configPath string) string {
	sidebar, sidebarMissing := sidebarSection(cfg, scan)
	keyMissing := !KeyBound(scan)

	if !sidebarMissing && !keyMissing {
		return headerAllDone + "\n\n" + mergeTarget + "\n"
	}

	sections := []string{configPath + headerAdd, sidebar}
	if keyMissing {
		sections = append(sections, keyBlock())
	} else {
		sections = append(sections, keyDone)
	}
	// 주석 처리된 것은 herdr가 읽지 않으므로 없는 것이다. 다른 절과 같은 잣대다.
	if !scan.Contains(newWorktreeAction) {
		sections = append(sections, newWorktreeBlock())
	}
	sections = append(sections, mergeTarget, nextStep)
	return strings.Join(sections, "\n\n") + "\n"
}

// sidebarSection은 사이드바 절과, 아직 더할 것이 남았는지를 돌려준다.
//
// 사이드바 행을 아직 쓰지 않으면 rows 전체를 보여 준다. 기본 행을 바꾼 적이 없는 사람이 그대로
// 붙여 넣으면 되게 하기 위해서다. 이미 쓰고 있으면 rows를 통째로 내놓아도 사용자의 행을 덮을 뿐이므로,
// 그 행에 더할 토큰 항목만 보여 준다.
//
// "이미 쓰고 있다"의 기준은 [ui.sidebar.spaces] 헤더가 아니라 rows 키다. `[ui.sidebar]` 아래에
// `spaces.rows = ...`처럼 점 키로 적어도 같은 테이블의 같은 키이고, 반대로 헤더는 있어도 `row_gap`만
// 두고 rows는 없는 파일도 있다. 헤더든 점 키든 테이블이 어떤 모양으로든 이미 정의되어 있으면
// `[ui.sidebar.spaces]` 헤더를 다시 내놓을 수 없다. 붙여 넣는 순간 테이블이 두 번 정의되어 herdr가
// 설정 전체를 거절하고 기본값으로 돌아간다. 그때는 헤더 없이 rows만 내놓는다.
func sidebarSection(cfg config.Resolved, scan herdrconfig.Scan) (string, bool) {
	items := entries(cfg)
	if len(items) == 0 {
		return sidebarNoToken, false
	}
	if !scan.HasKey(spacesTable+".rows") && !SidebarConfigured(cfg, scan) {
		if scan.HasTable(spacesTable) || scan.HasKey(spacesTable) {
			return sidebarTableOnly + "\n" + rowsValue(items), true
		}
		return rowsBlock(items), true
	}
	missing := make([]tokenEntry, 0, len(items))
	for _, e := range items {
		if !scan.HasToken(e.name) {
			missing = append(missing, e)
		}
	}
	if len(missing) == 0 {
		return sidebarDone, false
	}
	return sidebarPartial + "\n" + strings.Join(formatEntries(missing), "\n"), true
}

// rowsBlock은 [ui.sidebar.spaces] 테이블 전체를 만든다.
func rowsBlock(items []tokenEntry) string {
	return "[" + spacesTable + "]\n" + rowsValue(items)
}

// rowsValue는 rows 키와 값이다. herdr 기본 행에서 git_status 자리를 우리 토큰으로 바꾼 모양이다.
func rowsValue(items []tokenEntry) string {
	lines := []string{
		"rows = [",
		`  ["state_icon", "workspace"],`,
		"  [",
		`    "branch",`,
	}
	lines = append(lines, formatEntries(items)...)
	lines = append(lines, "  ],", "]")
	return strings.Join(lines, "\n")
}

// formatEntries는 토큰 항목을 한 줄씩 만든다. fg 열이 나란히 서도록 이름 뒤를 채운다.
func formatEntries(items []tokenEntry) []string {
	width := 0
	for _, e := range items {
		if n := len(tokenRef(e.name)); n > width {
			width = n
		}
	}
	lines := make([]string, 0, len(items))
	for _, e := range items {
		ref := tokenRef(e.name)
		line := "    { token = " + ref + strings.Repeat(" ", width-len(ref)+1) + `fg = "` + e.fg + `"`
		if e.bold {
			line += ", bold = true"
		}
		if e.dim {
			line += ", dim = true"
		}
		lines = append(lines, line+" },")
	}
	return lines
}

// tokenRef는 rows 안에서 토큰을 가리키는 값(`"$이름",`)이다. 뒤의 쉼표까지 포함해 너비를 맞춘다.
func tokenRef(name string) string {
	return `"$` + name + `",`
}

// keyBlock은 worktree 화면을 여는 키다. prefix+shift+u는 herdr 기본 키에서 비어 있다.
func keyBlock() string {
	return strings.Join([]string{
		"[[keys.command]]",
		`key = "prefix+shift+u"`,
		`type = "plugin_action"`,
		`command = "` + worktreesAction + `"`,
		`description = "git upstream: worktrees"`,
	}, "\n")
}

// newWorktreeBlock은 생성 팝업 키를 주석 처리한 채로 만든다.
//
// herdr 내장 팝업을 갈아끼우는 일은 사용자가 고르게 둔다. 켜는 방법을 주석으로 함께 적어,
// 주석을 푸는 것만으로 되게 한다.
func newWorktreeBlock() string {
	return strings.Join([]string{
		"# " + newWorktreeWhy,
		"# [[keys.command]]",
		`# key = "prefix+shift+g"`,
		`# type = "plugin_action"`,
		`# command = "` + newWorktreeAction + `"`,
		`# description = "git upstream: new worktree"`,
	}, "\n")
}
