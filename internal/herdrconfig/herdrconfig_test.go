package herdrconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHasTable(t *testing.T) {
	cases := []struct {
		name string
		text string
		// table 은 물어볼 테이블 이름이다. 비우면 ui.sidebar.spaces 다.
		table string
		want  bool
	}{
		{"그대로 적힌 헤더", "[ui.sidebar.spaces]\nrows = []\n", "", true},
		{"앞뒤 공백", "   [ui.sidebar.spaces]   \n", "", true},
		{"점 둘레의 공백", "[ ui . sidebar . spaces ]\n", "", true},
		{"CRLF", "[ui.sidebar.spaces]\r\nrows = []\r\n", "", true},
		{"뒤에 주석", "[ui.sidebar.spaces] # 사이드바\n", "", true},
		{"주석 처리된 헤더", "# [ui.sidebar.spaces]\n", "", false},
		{"다른 테이블", "[ui.sidebar.tabs]\n", "", false},
		{"더 긴 이름", "[ui.sidebar.spaces.extra]\n", "", false},
		{"배열 값 줄은 헤더가 아니다", `rows = [["branch", "git_status"]]` + "\n", "", false},
		// 배열의 마지막 원소는 뒤 쉼표를 생략할 수 있어 `[`로 시작해 `]`로 끝난다. 그래도 헤더가 아니다.
		{"끝 쉼표 없는 마지막 배열 원소는 헤더가 아니다", `["branch", "git_status"]` + "\n", "branch", false},
		{"인라인 테이블을 담은 배열 원소도 헤더가 아니다", `[ "branch", { token = "$behind" } ]` + "\n", "branch", false},
		{"빈 본문", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table := tc.table
			if table == "" {
				table = "ui.sidebar.spaces"
			}
			if got := Parse(tc.text).HasTable(table); got != tc.want {
				t.Fatalf("HasTable(%q) = %v, 기대값 %v", table, got, tc.want)
			}
		})
	}
}

// [[keys.command]] 같은 테이블 배열도 이름으로 찾을 수 있어야 한다.
func TestHasTableAcceptsArrayOfTables(t *testing.T) {
	scan := Parse("[[keys.command]]\nkey = \"prefix+shift+u\"\n")
	if !scan.HasTable("keys.command") {
		t.Fatal("테이블 배열 헤더를 알아보지 못했다")
	}
}

func TestContains(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		needle string
		want   bool
	}{
		{"값 안에 있음", `{ token = "$behind", fg = "#f38ba8" },` + "\n", "$behind", true},
		{"주석 줄", "# { token = \"$behind\" }\n", "$behind", false},
		{"줄 안의 주석 뒤", "rows = [] # $behind 는 나중에\n", "$behind", false},
		{"따옴표 안의 # 은 주석이 아니다", `{ token = "$behind", fg = "#f38ba8" }, # 색` + "\n", "#f38ba8", true},
		{"따옴표 안의 # 뒤 토큰", `{ fg = "#f38ba8", token = "$behind" }` + "\n", "$behind", true},
		{"작은따옴표 안의 #", `command = '#$behind'` + "\n", "$behind", true},
		{"이스케이프된 따옴표 뒤", `description = "say \"hi\" # not comment $behind"` + "\n", "$behind", true},
		{"액션 이름", `command = "git-upstream.worktrees"` + "\n", "git-upstream.worktrees", true},
		{"주석 처리된 액션 이름", `# command = "git-upstream.new-worktree"` + "\n", "git-upstream.new-worktree", false},
		{"CRLF", "command = \"git-upstream.worktrees\"\r\n", "git-upstream.worktrees", true},
		{"빈 바늘", "anything\n", "", false},
		{"없음", "rows = []\n", "$ahead", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.text).Contains(tc.needle); got != tc.want {
				t.Fatalf("Contains(%q) = %v, 기대값 %v", tc.needle, got, tc.want)
			}
		})
	}
}

// HasKey 는 헤더가 아니라 키를 묻는다. 점 키로 적은 사이드바 행에 setup 이 헤더를 다시 내놓으면
// 테이블이 두 번 정의되므로, 같은 키를 어떤 모양으로 적었든 같은 답이 나와야 한다.
func TestHasKey(t *testing.T) {
	cases := []struct {
		name string
		text string
		// key 는 물어볼 전체 키 이름이다. 비우면 ui.sidebar.spaces.rows 다.
		key  string
		want bool
	}{
		{"헤더 아래의 키", "[ui.sidebar.spaces]\nrows = []\n", "", true},
		{"부모 테이블 아래의 점 키", "[ui.sidebar]\nspaces.rows = []\n", "", true},
		{"조부모 테이블 아래의 점 키", "[ui]\nsidebar.spaces.rows = []\n", "", true},
		{"뿌리의 점 키", "ui.sidebar.spaces.rows = []\n", "", true},
		{"점 둘레의 공백", "[ ui . sidebar ]\nspaces . rows = []\n", "", true},
		{"공백 없이 적음", "[ui.sidebar.spaces]\nrows=[]\n", "", true},
		{"여러 줄 배열의 첫 줄", "[ui.sidebar.spaces]\nrows = [\n  [\"branch\", \"git_status\"],\n]\n", "", true},
		{"CRLF", "[ui.sidebar]\r\nspaces.rows = []\r\n", "", true},
		{"아래 키가 있으면 테이블 이름도 참", "[ui.sidebar]\nspaces.row_gap = 1\n", "ui.sidebar.spaces", true},
		{"헤더만 있고 키가 없음", "[ui.sidebar.spaces]\n", "", false},
		{"같은 테이블의 다른 키", "[ui.sidebar.spaces]\nrow_gap = 1\n", "", false},
		{"다른 테이블 뒤의 키는 그 테이블 것", "[ui.sidebar.spaces]\n[ui.sidebar.tabs]\nrows = []\n", "", false},
		{"이름의 앞부분만 같은 키", "[ui.sidebar.spaces]\nrows_extra = []\n", "", false},
		{"뿌리의 같은 이름 키", "rows = []\n", "", false},
		{"주석 처리된 키", "[ui.sidebar.spaces]\n# rows = []\n", "", false},
		{"빈 본문", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.key
			if key == "" {
				key = "ui.sidebar.spaces.rows"
			}
			if got := Parse(tc.text).HasKey(key); got != tc.want {
				t.Fatalf("HasKey(%q) = %v, 기대값 %v", key, got, tc.want)
			}
		})
	}
	// 빈 이름은 어떤 키와도 맞지 않아야 한다. 접두사 검사가 빈 이름에 걸려 전부 참이 되면 안 된다.
	if Parse("rows = []\n").HasKey("") {
		t.Fatal("빈 이름은 거짓이어야 한다")
	}
}

// HasToken 은 `$이름` 을 통째로 찾아야 한다. 앞부분만 같은 다른 토큰에 속으면 setup 은 그 항목을
// 빼놓고 status 는 있다고 답한다.
func TestHasToken(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		token string
		want  bool
	}{
		{"기본 문자열 안", `{ token = "$behind", fg = "#f38ba8" },` + "\n", "behind", true},
		{"리터럴 문자열 안", `{ token = '$behind' }` + "\n", "behind", true},
		{"줄 끝", "token = $behind\n", "behind", true},
		{"짧은 이름은 긴 토큰에 맞지 않는다", `{ token = "$gone" }` + "\n", "g", false},
		{"기본 이름은 바꾼 짧은 이름에 맞지 않는다", `{ token = "$ahead" }` + "\n", "a", false},
		{"다른 플러그인의 접두사가 같은 토큰", `{ token = "$merged_prs" }` + "\n", "merged", false},
		{"대시로 이어진 긴 토큰", `{ token = "$behind-main" }` + "\n", "behind", false},
		{"숫자로 이어진 긴 토큰", `{ token = "$behind2" }` + "\n", "behind", false},
		{"같은 줄에서 접두사 토큰 뒤에 진짜 토큰", `{ token = "$merged_prs" }, { token = "$merged" }` + "\n", "merged", true},
		{"긴 이름", `{ token = "$merged_prs" }` + "\n", "merged_prs", true},
		{"주석 줄", `# { token = "$behind" }` + "\n", "behind", false},
		{"빈 이름", `{ token = "$" }` + "\n", "", false},
		{"없음", "rows = []\n", "behind", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.text).HasToken(tc.token); got != tc.want {
				t.Fatalf("HasToken(%q) = %v, 기대값 %v", tc.token, got, tc.want)
			}
		})
	}
}

func TestWorktreeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	custom := filepath.Join(home, "wt")
	// 기본 문자열 안의 백슬래시는 이스케이프해야 한다. 윈도우 경로를 그대로 넣으면 `\t`, `\n`이
	// 탭과 줄바꿈으로 풀려 사용자 이름에 따라 시험이 흔들리고, 애초에 herdr가 거절하는 본문이다.
	escaped := strings.ReplaceAll(custom, `\`, `\\`)

	cases := []struct {
		name string
		text string
		want string
	}{
		{"파일이 비어 있음", "", filepath.Join(home, ".herdr", "worktrees")},
		{"테이블은 있는데 키가 없음", "[worktrees]\nauto_open = true\n", filepath.Join(home, ".herdr", "worktrees")},
		{"기본 문자열", "[worktrees]\ndirectory = \"" + escaped + "\"\n", custom},
		{"리터럴 문자열", "[worktrees]\ndirectory = '" + custom + "'\n", custom},
		{"물결표 풀기", "[worktrees]\ndirectory = \"~/wt\"\n", custom},
		{"공백 없이 적음", "[worktrees]\ndirectory=\"~/wt\"\n", custom},
		{"뒤에 주석", "[worktrees]\ndirectory = \"~/wt\" # 여기\n", custom},
		{"CRLF", "[worktrees]\r\ndirectory = \"~/wt\"\r\n", custom},
		{"점으로 이은 키", "worktrees.directory = \"~/wt\"\n", custom},
		{"다른 테이블 뒤의 키는 무시", "[worktrees]\n[ui]\ndirectory = \"~/wt\"\n", filepath.Join(home, ".herdr", "worktrees")},
		{"다른 테이블의 같은 키는 무시", "[ui]\ndirectory = \"~/wt\"\n", filepath.Join(home, ".herdr", "worktrees")},
		{"앞의 다른 테이블을 지나 찾음", "[ui]\ndirectory = \"~/no\"\n[worktrees]\ndirectory = \"~/wt\"\n", custom},
		// 끝 쉼표 없는 배열 원소 줄은 `[`로 시작해 `]`로 끝나지만 테이블 헤더가 아니다.
		// 그것을 헤더로 보면 [worktrees] 가 거기서 끝난 것이 되어 뒤의 directory 를 놓친다.
		{"같은 테이블 안의 배열 값 줄을 지나 찾음", "[worktrees]\ncopy = [\n  [\"a\", \"b\"]\n]\ndirectory = \"~/wt\"\n", custom},
		{"빈 값은 기본값", "[worktrees]\ndirectory = \"\"\n", filepath.Join(home, ".herdr", "worktrees")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.text).WorktreeDirectory(); got != tc.want {
				t.Fatalf("WorktreeDirectory = %q, 기대값 %q", got, tc.want)
			}
		})
	}
}

// 홈을 알 수 없으면 물결표를 그대로 두어야 한다. 엉뚱한 자리로 바꾸는 것보다 낫다.
func TestWorktreeDirectoryKeepsTildeWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if got := Parse("").WorktreeDirectory(); got != DefaultWorktreeDirectory {
		t.Fatalf("홈이 없으면 그대로 두어야 한다: %q", got)
	}
}

func TestUnquote(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
		ok    bool
	}{
		{"기본 문자열", `"a b"`, "a b", true},
		{"이스케이프 풀기", `"C:\\Users\\me"`, `C:\Users\me`, true},
		{"따옴표 이스케이프", `"say \"hi\""`, `say "hi"`, true},
		{"리터럴은 그대로", `'C:\Users\me'`, `C:\Users\me`, true},
		{"닫는 따옴표 없음", `"open`, "", false},
		{"닫는 따옴표 뒤에 글자가 남음", `"a", "b"`, "", false},
		{"리터럴의 닫는 따옴표 뒤에 글자가 남음", `'a', 'b'`, "", false},
		{"문자열이 아님", `true`, "", false},
		{"너무 짧음", `"`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := unquote(tc.value)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("unquote(%q) = %q, %v; 기대값 %q, %v", tc.value, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// 파일이 없는 것은 오류가 아니다. herdr 를 기본 설정으로 쓰는 사람에게는 파일 자체가 없다.
func TestLoadWithoutAFileIsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	scan, err := Load()
	if err != nil {
		t.Fatalf("파일이 없는 것은 오류가 아니어야 한다: %v", err)
	}
	if scan.HasTable("ui.sidebar.spaces") || scan.Contains("$behind") {
		t.Fatal("빈 결과여야 한다")
	}
}

func TestLoadReadsTheHerdrConfigFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := filepath.Join(root, "herdr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[ui.sidebar.spaces]\nrows = [[\"branch\", { token = \"$behind\" }]]\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	scan, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !scan.HasTable("ui.sidebar.spaces") || !scan.Contains("$behind") {
		t.Fatal("파일 내용을 훑지 못했다")
	}
}

// 읽기 자체가 실패하면 오류를 알리되, 호출자가 빈 결과로 진행할 수 있어야 한다.
func TestLoadReportsReadErrorsWithAnEmptyScan(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	// config.toml 자리에 디렉터리를 두면 읽기가 실패한다.
	if err := os.MkdirAll(filepath.Join(root, "herdr", "config.toml"), 0o755); err != nil {
		t.Fatal(err)
	}

	scan, err := Load()
	if err == nil {
		t.Fatal("읽기 실패는 알려야 한다")
	}
	if scan.Contains("$behind") {
		t.Fatal("실패했을 때는 빈 결과여야 한다")
	}
}
