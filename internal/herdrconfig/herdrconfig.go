// Package herdrconfig는 사용자의 herdr 설정 파일(config.toml)을 파싱하지 않고 훑는다.
//
// 이 플러그인이 config.toml에서 알고 싶은 것은 몇 가지뿐이다. 사이드바 행이 어떤 모양으로든 이미
// 있는지와 우리 토큰을 요청하는지, worktree 화면이 키에 묶여 있는지, worktree 뿌리가 어디인지. 이 정도 물음에
// TOML 파서를 들이는 것은 지나치다. 표준 라이브러리에는 파서가 없고, 그것 하나 때문에 의존성을
// 더하면 "표준 라이브러리만 쓴다"는 약속이 깨진다. 그래서 줄 단위로 훑어 문자열이 있는지만 본다.
//
// 이 방식은 틀릴 수 있는 자리가 있다. 여러 줄 문자열 안의 `#`이나 `[`는 구분하지 못한다. 그래도
// 여기서 내는 답은 사람이 붙여 넣기 전에 눈으로 보는 안내와 status의 참고 값이지, 자동으로 파일을
// 고치는 근거가 아니다. 어긋나도 잃는 것이 없으므로 단순함을 택했다.
package herdrconfig

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"herdr-git-upstream/internal/herdrpaths"
)

// DefaultWorktreeDirectory는 herdr가 [worktrees] directory를 지정하지 않았을 때 쓰는 값이다.
const DefaultWorktreeDirectory = "~/.herdr/worktrees"

// Scan은 config.toml을 한 번 훑은 결과다. 영값은 빈 파일과 같다.
type Scan struct {
	// lines는 주석과 앞뒤 공백을 걷어 낸 줄들이다. 빈 줄은 담지 않는다.
	lines []string
}

// Load는 herdr 설정 파일을 읽어 훑는다.
//
// 파일이 없으면 오류가 아니다. herdr를 기본 설정으로 쓰는 사람에게는 파일 자체가 없을 수 있고,
// 그때 답은 "아무것도 설정되지 않았다"가 맞다. 그 밖의 오류는 빈 결과와 함께 돌려주어
// 호출자가 경고만 남기고 진행할 수 있게 한다.
func Load() (Scan, error) {
	raw, err := os.ReadFile(herdrpaths.ConfigFile())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Scan{}, nil
		}
		return Scan{}, err
	}
	return Parse(string(raw)), nil
}

// Parse는 본문을 훑는다. 파일 없이 시험할 수 있도록 Load와 떼어 두었다.
func Parse(text string) Scan {
	// 윈도우 편집기가 붙이는 BOM이 첫 줄의 테이블 헤더를 가리지 않게 한다.
	text = strings.TrimPrefix(text, "\uFEFF")
	split := strings.Split(text, "\n")
	lines := make([]string, 0, len(split))
	for _, line := range split {
		// CRLF로 저장된 파일도 그대로 받는다.
		line = strings.TrimSuffix(line, "\r")
		line = strings.TrimSpace(stripComment(line))
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return Scan{lines: lines}
}

// HasTable은 `[이름]` 또는 `[[이름]]` 헤더가 있는지 답한다. 이름은 점으로 이은 전체 이름이다.
// 예: "ui.sidebar.spaces".
func (s Scan) HasTable(name string) bool {
	for _, line := range s.lines {
		if got, ok := tableName(line); ok && got == name {
			return true
		}
	}
	return false
}

// Contains는 주석이 아닌 자리에 문자열이 있는지 답한다.
//
// `$behind` 같은 토큰 참조나 `git-upstream.worktrees` 같은 액션 이름을 찾는 데 쓴다.
// 주석 처리된 줄은 herdr가 읽지 않으므로 여기서도 없는 것으로 본다. 그래야 setup이 내놓은
// 주석 블록을 그대로 붙여 넣은 사람에게 "이미 있다"고 잘못 말하지 않는다.
func (s Scan) Contains(needle string) bool {
	if needle == "" {
		return false
	}
	for _, line := range s.lines {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

// HasToken은 사이드바 행이 `$이름` 토큰을 요청하고 있는지 답한다.
//
// Contains("$"+이름)으로는 모자란다. 부분 일치라서 다른 플러그인의 `$merged_prs`가 우리 `$merged`로,
// 기본 `$ahead`가 짧게 바꾼 이름 `$a`로 잡힌다. 그러면 setup은 그 토큰을 빼놓고 status는 있다고
// 답해, 사이드바에 아무것도 안 보이는데 설정됐다고 말하게 된다. herdr 토큰 이름은 `[A-Za-z0-9_-]`로만
// 이루어지므로, `$이름` 바로 뒤가 이름 글자가 아닐 때만 일치로 본다. 한 줄에 여러 토큰이 있을 수
// 있으므로 첫 자리에서 멈추지 않고 끝까지 본다.
func (s Scan) HasToken(name string) bool {
	if name == "" {
		return false
	}
	needle := "$" + name
	for _, line := range s.lines {
		for rest := line; ; {
			i := strings.Index(rest, needle)
			if i < 0 {
				break
			}
			after := i + len(needle)
			if after == len(rest) || !isBareKeyByte(rest[after]) {
				return true
			}
			rest = rest[i+1:]
		}
	}
	return false
}

// HasKey는 점으로 이은 전체 이름의 키가 값으로 적혀 있는지 답한다. 예: "ui.sidebar.spaces.rows".
//
// 테이블 헤더만 보아서는 모자란다. TOML은 `[ui.sidebar]` 아래 `spaces.rows = ...`로, `[ui]` 아래
// `sidebar.spaces.rows = ...`로, 뿌리에 `ui.sidebar.spaces.rows = ...`로 적어도 모두 같은 테이블의
// 같은 키다. 그런 파일에 setup이 `[ui.sidebar.spaces]` 헤더를 다시 내놓으면 붙여 넣는 순간 테이블이
// 두 번 정의되어 herdr가 설정 전체를 거절한다. 그래서 헤더가 있는지가 아니라 키가 있는지를 묻는다.
//
// 이름 아래의 키(`이름.무엇`)가 있어도 참이다. `ui.sidebar.spaces`를 물었을 때 `spaces.row_gap`처럼
// 그 테이블에 속한 키가 하나라도 있으면 테이블은 이미 정의된 것이기 때문이다.
func (s Scan) HasKey(name string) bool {
	if name == "" {
		return false
	}
	found := false
	s.walkKeys(func(key, _ string) bool {
		found = key == name || strings.HasPrefix(key, name+".")
		return !found
	})
	return found
}

// WorktreeDirectory는 herdr가 worktree를 만드는 뿌리 디렉터리다.
//
// `[worktrees]` 테이블의 `directory` 값이며, 없으면 herdr의 기본값이다. 앞의 `~`는 홈 디렉터리로
// 풀어 돌려주므로 그대로 경로 계산에 쓸 수 있다. 생성 팝업이 새 worktree가 놓일 자리를 미리
// 보여 줄 때 필요하다.
func (s Scan) WorktreeDirectory() string {
	dir := DefaultWorktreeDirectory
	s.walkKeys(func(key, value string) bool {
		if key != "worktrees.directory" {
			return true
		}
		got, ok := unquote(value)
		if !ok || got == "" {
			return true
		}
		dir = got
		return false
	})
	return expandHome(dir)
}

// walkKeys는 `이름 = 값` 줄을 차례로 fn에 넘긴다. 키는 그 줄이 속한 테이블 이름을 앞에 이어 붙인
// 전체 이름이고, 값은 따옴표를 벗기지 않은 그대로다. fn이 false를 돌려주면 멈춘다.
//
// 다른 테이블 헤더가 나오면 앞 테이블은 끝난 것이다. TOML은 같은 테이블을 두 번 열지 못하므로
// 뒤에 다시 나올 일도 없다. 테이블을 따라가는 규칙은 하나여야 하므로, 키를 묻는 물음은 모두
// 여기를 거친다.
func (s Scan) walkKeys(fn func(key, value string) bool) {
	table := ""
	for _, line := range s.lines {
		if name, ok := tableName(line); ok {
			table = name
			continue
		}
		key, value, ok := keyValue(line)
		if !ok {
			continue
		}
		if table != "" {
			key = table + "." + key
		}
		if !fn(key, value) {
			return
		}
	}
}

// stripComment는 줄에서 주석을 걷어 낸다.
//
// `#`이 나오면 그 뒤는 주석이지만, 따옴표 안의 `#`은 값의 일부다. 색 이름을 `"#f38ba8"`처럼 적는
// 사이드바 설정에서 바로 그 경우가 생기므로, 따옴표를 따라가며 판단한다.
func stripComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == 0:
			if c == '#' {
				return line[:i]
			}
			if c == '"' || c == '\'' {
				quote = c
			}
		case c == '\\' && quote == '"':
			// 기본 문자열의 이스케이프. 다음 글자가 닫는 따옴표여도 문자열은 끝나지 않는다.
			i++
		case c == quote:
			quote = 0
		}
	}
	return line
}

// tableName은 줄이 테이블 헤더면 점으로 이은 이름을 돌려준다.
//
// `[ ui . sidebar . spaces ]`처럼 공백을 섞어 적어도 같은 이름으로 본다. 다만 `["branch", "git_status"]`
// 같은 배열 값 줄도 대괄호로 시작해 끝나므로, 안쪽이 키 모양일 때만 헤더로 인정한다. 그래도
// `["a"]`처럼 원소가 하나뿐인 줄은 따옴표 키 헤더와 모양이 같아 가르지 못한다. 줄 단위 훑기의 한계다.
func tableName(line string) (string, bool) {
	inner, ok := strings.CutPrefix(line, "[")
	if !ok {
		return "", false
	}
	inner, ok = strings.CutSuffix(inner, "]")
	if !ok {
		return "", false
	}
	// [[이름]]은 테이블 배열이다. 이름을 묻는 데는 차이가 없다.
	if strings.HasPrefix(inner, "[") && strings.HasSuffix(inner, "]") {
		inner = inner[1 : len(inner)-1]
	}
	parts := strings.Split(inner, ".")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if unquoted, ok := unquote(part); ok {
			part = unquoted
		} else if !isBareKey(part) {
			return "", false
		}
		parts[i] = part
	}
	return strings.Join(parts, "."), true
}

// keyValue는 `이름 = 값` 줄을 나눈다. 값은 따옴표를 벗기지 않은 채로 돌려준다.
func keyValue(line string) (key, value string, ok bool) {
	i := strings.IndexByte(line, '=')
	if i < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:i])
	if key == "" {
		return "", "", false
	}
	// `worktrees.directory = ...`처럼 점으로 이은 키의 공백을 헤더와 같은 규칙으로 정리한다.
	parts := strings.Split(key, ".")
	for j, part := range parts {
		parts[j] = strings.TrimSpace(part)
	}
	return strings.Join(parts, "."), strings.TrimSpace(line[i+1:]), true
}

// unquote는 TOML 문자열 값의 따옴표를 벗긴다.
//
// 기본 문자열(`"..."`)은 `\\`, `\"`, `\t`, `\n`만 풀고, 리터럴 문자열(`'...'`)은 그대로 둔다.
// 윈도우 경로를 적는 두 방식(`"C:\\Users"`, `'C:\Users'`)이 모두 같은 값으로 읽히는 데는 이것으로 충분하다.
// 여러 줄 문자열은 다루지 않는다.
func unquote(value string) (string, bool) {
	if len(value) < 2 {
		return "", false
	}
	quote := value[0]
	if quote != '"' && quote != '\'' {
		return "", false
	}
	end := closingQuote(value, quote)
	if end < 0 {
		return "", false
	}
	// 닫는 따옴표가 값의 끝이어야 한다. `"branch", "git_status"`처럼 뒤에 다른 글자가 남아 있으면
	// 문자열 하나가 아니라 배열의 일부이고, 그것을 문자열로 받으면 배열 값 줄이 테이블 헤더로 둔갑한다.
	// keyValue가 넘기는 값은 주석을 걷고 공백을 뗀 것이라 정상 문자열은 언제나 따옴표로 끝난다.
	if end != len(value)-1 {
		return "", false
	}
	body := value[1:end]
	if quote == '\'' {
		return body, true
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' || i+1 >= len(body) {
			b.WriteByte(c)
			continue
		}
		i++
		switch body[i] {
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		default:
			// 모르는 이스케이프는 그대로 둔다. 잘못 풀어 다른 경로를 만드는 것보다 낫다.
			b.WriteByte('\\')
			b.WriteByte(body[i])
		}
	}
	return b.String(), true
}

// closingQuote는 여는 따옴표와 짝이 되는 닫는 따옴표의 자리를 찾는다. 없으면 -1이다.
func closingQuote(value string, quote byte) int {
	for i := 1; i < len(value); i++ {
		c := value[i]
		if c == '\\' && quote == '"' {
			i++
			continue
		}
		if c == quote {
			return i
		}
	}
	return -1
}

// isBareKey는 따옴표 없이 적을 수 있는 TOML 키인지 답한다(영숫자, `_`, `-`).
func isBareKey(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isBareKeyByte(s[i]) {
			return false
		}
	}
	return true
}

// isBareKeyByte는 따옴표 없는 TOML 키에 쓸 수 있는 글자인지 답한다. herdr 토큰 이름의 규칙
// (`[A-Za-z0-9_-]`)과 같으므로 HasToken이 이름의 경계를 볼 때도 이것을 쓴다.
func isBareKeyByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		return true
	}
	return false
}

// expandHome은 앞의 `~`를 홈 디렉터리로 바꾼다. herdr가 [worktrees] directory를 그렇게 읽는다.
// 홈을 알 수 없으면 그대로 둔다. 짐작한 자리로 바꾸느니 사용자가 적은 것을 보여 주는 편이 낫다.
func expandHome(path string) string {
	rest, ok := strings.CutPrefix(path, "~")
	if !ok || (rest != "" && rest[0] != '/' && rest[0] != '\\') {
		return path
	}
	home := homeDir()
	if home == "" {
		return path
	}
	return filepath.Join(home, rest)
}

// homeDir는 홈 디렉터리를 환경변수에서 읽는다.
// 윈도우에서는 HOME이 없는 셸이 흔하므로 USERPROFILE을 먼저 본다. herdrpaths와 같은 순서다.
func homeDir() string {
	if runtime.GOOS == "windows" {
		if profile := os.Getenv("USERPROFILE"); profile != "" {
			return profile
		}
	}
	return os.Getenv("HOME")
}
