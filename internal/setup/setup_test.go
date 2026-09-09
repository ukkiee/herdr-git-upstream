package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"herdr-git-upstream/internal/config"
	"herdr-git-upstream/internal/herdrconfig"
)

const configPath = "~/.config/herdr/config.toml"

// 기본 이름으로 빈 설정에 내놓는 본문 전체다. 모양이 바뀌면 여기서 드러난다.
const fullOutput = `~/.config/herdr/config.toml 에 아래를 더하세요.

[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  [
    "branch",
    { token = "$behind",     fg = "#f38ba8", bold = true },
    { token = "$ahead",      fg = "#a6e3a1" },
    { token = "$gone",       fg = "#6c7086" },
    { token = "$merged",     fg = "#6c7086" },
    { token = "$catchup",    fg = "#fab387", bold = true },
    { token = "$sync_stale", fg = "#6c7086", dim = true },
  ],
]

[[keys.command]]
key = "prefix+shift+u"
type = "plugin_action"
command = "git-upstream.worktrees"
description = "git upstream: worktrees"

# 기준 브랜치를 고르는 생성 팝업. herdr 내장 팝업 대신 쓰려면 주석을 풀고 new_worktree 키를 비운다.
# [[keys.command]]
# key = "prefix+shift+g"
# type = "plugin_action"
# command = "git-upstream.new-worktree"
# description = "git upstream: new worktree"

통합 브랜치가 origin/HEAD 가 아닌 저장소에서는 그 저장소에서 이렇게 알려 주세요.
  git config --add git-upstream.mergeTarget origin/develop

그다음: herdr config check && herdr server reload-config
`

const spacesOnly = `[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  ["branch", "git_status"],
]
`

const someTokens = `[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  [
    "branch",
    { token = "$behind", fg = "#f38ba8", bold = true },
    { token = "$ahead",  fg = "#a6e3a1" },
    { token = "$sync_stale", fg = "#6c7086", dim = true },
  ],
]
`

const allTokens = `[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  [
    "branch",
    { token = "$behind", fg = "#f38ba8", bold = true },
    { token = "$ahead", fg = "#a6e3a1" },
    { token = "$gone", fg = "#6c7086" },
    { token = "$merged", fg = "#6c7086" },
    { token = "$catchup", fg = "#fab387", bold = true },
    { token = "$sync_stale", fg = "#6c7086", dim = true },
  ],
]
`

const worktreesKey = `[[keys.command]]
key = "prefix+shift+u"
type = "plugin_action"
command = "git-upstream.worktrees"
`

const newWorktreeKey = `[[keys.command]]
key = "prefix+shift+g"
type = "plugin_action"
command = "git-upstream.new-worktree"
`

// setup 이 내놓는 그대로의 주석 처리된 생성 팝업 키다.
const newWorktreeCommented = `# 기준 브랜치를 고르는 생성 팝업. herdr 내장 팝업 대신 쓰려면 주석을 풀고 new_worktree 키를 비운다.
# [[keys.command]]
# key = "prefix+shift+g"
# type = "plugin_action"
# command = "git-upstream.new-worktree"
# description = "git upstream: new worktree"
`

const mergeTargetNote = `통합 브랜치가 origin/HEAD 가 아닌 저장소에서는 그 저장소에서 이렇게 알려 주세요.
  git config --add git-upstream.mergeTarget origin/develop
`

// 헤더 없이 부모 테이블 아래 점 키로 적은 사이드바 행이다. TOML 로는 [ui.sidebar.spaces] 와 같은 테이블이다.
const dottedRows = `[ui.sidebar]
spaces.rows = [["state_icon", "workspace"], ["branch", { token = "$behind" }, { token = "$ahead" }]]
`

// 점 키로 적은 사이드바 행에 우리 토큰이 하나도 없는 경우다. 세 가지 모양 모두 TOML 로는 같은 키다.
const dottedRowsNoTokens = `[ui.sidebar]
spaces.rows = [["state_icon", "workspace"], ["branch", "git_status"]]
`

const grandparentDottedRowsNoTokens = `[ui]
sidebar.spaces.rows = [["state_icon", "workspace"], ["branch", "git_status"]]
`

const rootDottedRowsNoTokens = `ui.sidebar.spaces.rows = [["state_icon", "workspace"], ["branch", "git_status"]]
`

// 다른 플러그인의 토큰이 우리 이름을 앞부분으로 갖는 경우다. 우리 토큰은 하나도 없다.
const prefixOnlyTokens = `[ui.sidebar.spaces]
rows = [["state_icon", "workspace"], ["branch", { token = "$merged_prs" }, { token = "$ahead_x" }]]
`

func TestRender(t *testing.T) {
	cases := []struct {
		name string
		toml string
		// json 은 플러그인 설정(config.json)이다. 비우면 기본값이다.
		json string
		// exact 가 있으면 본문 전체를 견주고, 없으면 want/unwanted 로 부분만 본다.
		exact    string
		want     []string
		unwanted []string
	}{
		{
			name:  "빈 설정",
			toml:  "",
			exact: fullOutput,
		},
		{
			name: "spaces 만 있음",
			toml: spacesOnly,
			want: []string{
				configPath + " 에 아래를 더하세요.",
				"이미 [ui.sidebar.spaces] 를 쓰고 있으니 아래 토큰 항목을 그 행에 더하세요.",
				`    { token = "$behind",     fg = "#f38ba8", bold = true },`,
				`    { token = "$sync_stale", fg = "#6c7086", dim = true },`,
				"[[keys.command]]\nkey = \"prefix+shift+u\"",
				"# command = \"git-upstream.new-worktree\"",
				"git config --add git-upstream.mergeTarget origin/develop",
				"그다음: herdr config check && herdr server reload-config",
			},
			unwanted: []string{
				// 사용자의 행을 덮어쓰게 하는 rows 전체는 내놓지 않는다.
				"[ui.sidebar.spaces]\nrows = [",
				`["state_icon", "workspace"]`,
			},
		},
		{
			name: "토큰 일부 있음",
			toml: someTokens,
			want: []string{
				"이미 [ui.sidebar.spaces] 를 쓰고 있으니 아래 토큰 항목을 그 행에 더하세요.",
				`    { token = "$gone",    fg = "#6c7086" },`,
				`    { token = "$merged",  fg = "#6c7086" },`,
				`    { token = "$catchup", fg = "#fab387", bold = true },`,
			},
			unwanted: []string{
				`token = "$behind"`,
				`token = "$ahead"`,
				`token = "$sync_stale"`,
			},
		},
		{
			name: "worktrees 키 있음",
			toml: worktreesKey,
			want: []string{
				"[ui.sidebar.spaces]\nrows = [",
				"worktrees 키는 이미 묶여 있습니다.",
				"# command = \"git-upstream.new-worktree\"",
				"그다음: herdr config check && herdr server reload-config",
			},
			unwanted: []string{
				"[[keys.command]]\nkey = \"prefix+shift+u\"",
				`command = "git-upstream.worktrees"`,
			},
		},
		{
			name: "new-worktree 키 있음",
			toml: newWorktreeKey,
			want: []string{
				"[ui.sidebar.spaces]\nrows = [",
				"[[keys.command]]\nkey = \"prefix+shift+u\"",
			},
			unwanted: []string{
				"git-upstream.new-worktree",
				"기준 브랜치를 고르는 생성 팝업",
			},
		},
		{
			name:  "전부 있음",
			toml:  allTokens + "\n" + worktreesKey + "\n" + newWorktreeKey,
			exact: "설정이 모두 들어 있습니다.\n\n" + mergeTargetNote,
		},
		{
			// 생성 팝업 키는 옵트인이라 없어도 "모두 들어 있음"이다. 그때는 안내만 붙이고 제안도 하지 않는다.
			name:  "필수 절만 있음",
			toml:  allTokens + "\n" + worktreesKey,
			exact: "설정이 모두 들어 있습니다.\n\n" + mergeTargetNote,
		},
		{
			// setup 출력을 그대로 붙여 넣은 기본 상태다. 답은 위와 같아야 매번 되풀이되지 않는다.
			name:  "필수 절과 주석 처리된 생성 팝업 키",
			toml:  allTokens + "\n" + worktreesKey + "\n" + newWorktreeCommented,
			exact: "설정이 모두 들어 있습니다.\n\n" + mergeTargetNote,
		},
		{
			// `$merged_prs` 는 `$merged` 가 아니다. 앞부분만 같은 남의 토큰에 속아 우리 항목을 빼면 안 된다.
			name: "접두사만 같은 다른 토큰이 있음",
			toml: prefixOnlyTokens,
			want: []string{
				"이미 [ui.sidebar.spaces] 를 쓰고 있으니 아래 토큰 항목을 그 행에 더하세요.",
				`    { token = "$ahead",      fg = "#a6e3a1" },`,
				`    { token = "$merged",     fg = "#6c7086" },`,
			},
			unwanted: []string{"사이드바 토큰은 이미 들어 있습니다."},
		},
		{
			// 헤더 없이 점 키로 적어도 같은 테이블이다. rows 전체를 내놓으면 붙여 넣는 순간 테이블이
			// 두 번 정의되므로, 빠진 토큰 항목만 보여 준다.
			name: "헤더 없이 점 키로 적은 rows",
			toml: dottedRows,
			want: []string{
				"이미 [ui.sidebar.spaces] 를 쓰고 있으니 아래 토큰 항목을 그 행에 더하세요.",
				`    { token = "$gone",       fg = "#6c7086" },`,
				`    { token = "$sync_stale", fg = "#6c7086", dim = true },`,
			},
			unwanted: []string{
				"[ui.sidebar.spaces]\nrows = [",
				`["state_icon", "workspace"]`,
				`token = "$behind"`,
				`token = "$ahead"`,
			},
		},
		{
			// 우리 토큰이 하나도 없어도 점 키로 적은 rows 는 이미 있는 행이다. 실측으로 herdr 는 여기에
			// 헤더를 덧붙인 파일을 "duplicate key `spaces`" 로 거절하고 설정 전체를 버린다.
			name: "점 키로 적었고 우리 토큰은 없음",
			toml: dottedRowsNoTokens,
			want: []string{
				"이미 [ui.sidebar.spaces] 를 쓰고 있으니 아래 토큰 항목을 그 행에 더하세요.",
				`    { token = "$behind",     fg = "#f38ba8", bold = true },`,
				`    { token = "$sync_stale", fg = "#6c7086", dim = true },`,
			},
			unwanted: []string{
				"[ui.sidebar.spaces]\nrows = [",
				"rows = [",
				`["state_icon", "workspace"]`,
			},
		},
		{
			name: "조부모 테이블 아래 점 키로 적었고 우리 토큰은 없음",
			toml: grandparentDottedRowsNoTokens,
			want: []string{
				"이미 [ui.sidebar.spaces] 를 쓰고 있으니 아래 토큰 항목을 그 행에 더하세요.",
				`    { token = "$behind",     fg = "#f38ba8", bold = true },`,
			},
			unwanted: []string{"[ui.sidebar.spaces]\nrows = [", "rows = ["},
		},
		{
			name: "뿌리에 점 키로 적었고 우리 토큰은 없음",
			toml: rootDottedRowsNoTokens,
			want: []string{
				"이미 [ui.sidebar.spaces] 를 쓰고 있으니 아래 토큰 항목을 그 행에 더하세요.",
				`    { token = "$behind",     fg = "#f38ba8", bold = true },`,
			},
			unwanted: []string{"[ui.sidebar.spaces]\nrows = [", "rows = ["},
		},
		{
			// 헤더는 있는데 rows 가 없으면 herdr 는 기본 행을 쓰고 있어 "그 행에 더하라"고 할 자리가 없다.
			// 헤더를 다시 내면 테이블이 두 번 정의되므로, 헤더 없이 rows 만 내놓는다.
			name: "헤더는 있는데 rows 가 없음",
			toml: "[ui.sidebar.spaces]\nrow_gap = 1\n",
			want: []string{
				"이미 [ui.sidebar.spaces] 테이블이 있으니 그 아래에 rows 를 더하세요.\nrows = [\n  [\"state_icon\", \"workspace\"],",
				`    { token = "$behind",     fg = "#f38ba8", bold = true },`,
				`    { token = "$sync_stale", fg = "#6c7086", dim = true },`,
			},
			unwanted: []string{
				"[ui.sidebar.spaces]\nrows = [",
				"그 행에 더하세요",
			},
		},
		{
			// 점 키로 테이블만 정의한 경우도 같다. `spaces.row_gap` 하나로 ui.sidebar.spaces 는 이미 있는 테이블이다.
			name: "점 키로 테이블만 있고 rows 가 없음",
			toml: "[ui.sidebar]\nspaces.row_gap = 1\n",
			want: []string{
				"이미 [ui.sidebar.spaces] 테이블이 있으니 그 아래에 rows 를 더하세요.\nrows = [",
			},
			unwanted: []string{"[ui.sidebar.spaces]\nrows = [", "그 행에 더하세요"},
		},
		{
			name: "토큰 이름을 바꾼 설정",
			toml: "",
			json: `{"behind_token":"down","ahead_token":"","gone_token":"g","stale_token":"old"}`,
			want: []string{
				`    { token = "$down",    fg = "#f38ba8", bold = true },`,
				`    { token = "$g",       fg = "#6c7086" },`,
				`    { token = "$merged",  fg = "#6c7086" },`,
				`    { token = "$catchup", fg = "#fab387", bold = true },`,
				`    { token = "$old",     fg = "#6c7086", dim = true },`,
			},
			unwanted: []string{
				// 빈 이름은 보고하지 않으므로 행에도 넣지 않는다.
				`"$ahead"`,
				`"$behind"`,
				`"$sync_stale"`,
			},
		},
		{
			name: "바꾼 이름으로 이미 들어 있음",
			toml: "[ui.sidebar.spaces]\nrows = [[\"branch\", { token = \"$down\" }]]\n",
			json: `{"behind_token":"down","ahead_token":"","gone_token":"","merged_token":"","catchup_token":"","stale_token":""}`,
			want: []string{"사이드바 토큰은 이미 들어 있습니다."},
			unwanted: []string{
				"이미 [ui.sidebar.spaces] 를 쓰고 있으니",
				`token = "$`,
			},
		},
		{
			name: "토큰 이름이 모두 비어 있음",
			toml: "",
			json: `{"behind_token":"","ahead_token":"","gone_token":"","merged_token":"","catchup_token":"","stale_token":""}`,
			want: []string{
				"보고할 토큰 이름이 모두 비어 있어 사이드바에 더할 것이 없습니다.",
				"[[keys.command]]\nkey = \"prefix+shift+u\"",
			},
			unwanted: []string{"[ui.sidebar.spaces]\nrows = ["},
		},
		{
			// 주석 처리된 생성 팝업 키도 마찬가지다. 필수 절이 빠져 있으면 주석 블록을 다시 내놓는다.
			name: "주석 처리된 것은 없는 것이다",
			toml: "# [ui.sidebar.spaces]\n# rows = [[{ token = \"$behind\" }]]\n# command = \"git-upstream.worktrees\"\n" + newWorktreeCommented,
			want: []string{
				"[ui.sidebar.spaces]\nrows = [",
				"[[keys.command]]\nkey = \"prefix+shift+u\"",
				"# command = \"git-upstream.new-worktree\"",
			},
			unwanted: []string{"이미 들어 있습니다", "이미 묶여 있습니다"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadConfig(t, tc.json)
			got := Render(cfg, herdrconfig.Parse(tc.toml), configPath)
			if tc.exact != "" && got != tc.exact {
				t.Fatalf("본문이 다르다.\n--- 결과 ---\n%s\n--- 기대값 ---\n%s", got, tc.exact)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("%q 가 있어야 한다.\n--- 결과 ---\n%s", want, got)
				}
			}
			for _, unwanted := range tc.unwanted {
				if strings.Contains(got, unwanted) {
					t.Fatalf("%q 가 없어야 한다.\n--- 결과 ---\n%s", unwanted, got)
				}
			}
		})
	}
}

// 출력을 그대로 붙여 넣은 뒤 다시 돌리면 더할 것이 없어야 한다. 주석 처리한 생성 팝업 키를 주석인
// 채로 둔 것이 기본 상태이므로, 그것 때문에 "더하세요"나 reload 안내, 같은 주석 블록이 다시 나오면
// 안 된다. 출력과 훑기가 서로 맞물려 있는지 보는 시험이다.
func TestRenderedOutputIsRecognisedWhenPastedBack(t *testing.T) {
	cfg := loadConfig(t, "")
	first := Render(cfg, herdrconfig.Parse(""), configPath)

	second := Render(cfg, herdrconfig.Parse(first), configPath)
	if want := "설정이 모두 들어 있습니다.\n\n" + mergeTargetNote; second != want {
		t.Fatalf("붙여 넣은 뒤에는 더할 것이 없어야 한다.\n--- 결과 ---\n%s\n--- 기대값 ---\n%s", second, want)
	}

	// 주석을 풀어 생성 팝업 키를 켠 뒤에도 같은 답이어야 한다.
	uncommented := strings.ReplaceAll(first, "\n# ", "\n")
	third := Render(cfg, herdrconfig.Parse(uncommented), configPath)
	if second != third {
		t.Fatalf("생성 팝업 키를 켠 뒤에도 답이 같아야 한다.\n--- 결과 ---\n%s", third)
	}
}

func TestSidebarConfiguredAndKeyBound(t *testing.T) {
	cfg := loadConfig(t, "")
	cases := []struct {
		name    string
		toml    string
		sidebar bool
		key     bool
	}{
		{"빈 설정", "", false, false},
		{"토큰 하나만", `rows = [["branch", { token = "$ahead" }]]` + "\n", true, false},
		{"키만", worktreesKey, false, true},
		{"주석 처리된 토큰과 키", "# token = \"$ahead\"\n# command = \"git-upstream.worktrees\"\n", false, false},
		{"접두사만 같은 다른 토큰", prefixOnlyTokens, false, false},
		{"헤더 없이 점 키로 적은 행", dottedRows, true, false},
		{"둘 다", someTokens + worktreesKey, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scan := herdrconfig.Parse(tc.toml)
			if got := SidebarConfigured(cfg, scan); got != tc.sidebar {
				t.Fatalf("SidebarConfigured = %v, 기대값 %v", got, tc.sidebar)
			}
			if got := KeyBound(scan); got != tc.key {
				t.Fatalf("KeyBound = %v, 기대값 %v", got, tc.key)
			}
		})
	}
}

// loadConfig는 플러그인 설정을 임시 디렉터리에서 읽는다. 비우면 기본값이다.
func loadConfig(t *testing.T, json string) config.Resolved {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	if json != "" {
		if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(json), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
