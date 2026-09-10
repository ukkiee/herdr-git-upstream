package tui

import (
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Frame은 화면을 통째로 다시 그린다.
//
// 줄마다 커서를 그 줄의 첫 칸으로 옮기고 줄을 지운(ESC [K) 뒤에 내용을 쓴다. 지우는 이유는 지난번에 그린
// 것보다 짧아진 줄의 꼬리가 남지 않게 하기 위해서다. lines 보다 rows 가 많으면 남는 줄도 비운다. rows 를
// 넘는 줄은 버리고, cols 를 넘는 줄은 표시 폭 기준으로 자른다. 한 번의 쓰기로 보내 깜빡임을 줄인다.
//
// 지우기가 내용보다 앞에 있는 이유가 있다. VT/xterm 계열은 마지막 열에 글자를 쓰면 커서를 다음 줄로 넘기지
// 않고 마지막 열에 둔 채 "대기 중인 줄 바꿈" 상태가 되는데, 그 자리에서 EL 을 받으면 마지막 열부터 지운다.
// 표시 폭이 cols 와 딱 맞는 줄(반전으로 끝까지 채운 커서 행, 제목 줄)마다 마지막 한 칸이 사라지는 것이다.
// xterm 과 alacritty 가 그렇고 VTE·iTerm2·kitty 는 다르게 동작해 에뮬레이터마다 결과가 갈린다. 첫 칸에서
// 지우고 쓰면 꼬리 지우기 효과는 같고 커서 상태에 기대지 않는다.
//
// 차이만 그리지 않는다. 화면이 스무 줄 남짓이라 통째로 다시 그려도 값이 싸고, 무엇이 바뀌었는지 따지는
// 코드가 없어야 화면 패키지가 "모델 → 줄 목록" 순수 함수로 남는다.
func Frame(w io.Writer, lines []string, cols, rows int) {
	var buf strings.Builder
	for row := 0; row < rows; row++ {
		buf.WriteString("\x1b[")
		buf.WriteString(strconv.Itoa(row + 1))
		buf.WriteString(";1H\x1b[K")
		if row < len(lines) {
			buf.WriteString(Truncate(lines[row], cols))
		}
	}
	_, _ = io.WriteString(w, buf.String())
}

// Width는 문자열의 표시 폭이다. ANSI 시퀀스는 세지 않고, 한글과 CJK 글자는 두 칸으로 센다.
func Width(s string) int {
	width := 0
	walk(s, func(r rune, w int) bool {
		width += w
		return true
	})
	return width
}

// Truncate는 표시 폭이 cols 를 넘지 않도록 룬 경계에서 자른다.
//
// 두 칸짜리 글자가 마지막 한 칸에 걸리면 그 글자는 넣지 않는다. 반 칸을 그릴 수는 없다. ANSI 시퀀스는
// 폭을 차지하지 않으므로 잘리는 자리와 무관하게 그대로 둔다. 다만 잘라 냈다면 뒤에 있던 꾸밈 끄기까지
// 함께 잘려 나갔을 수 있으므로, 꾸밈이 있던 줄은 끝에 전부 끄기(ESC [0m)를 붙인다. 그러지 않으면
// 반전이나 색이 다음 줄로 번진다.
func Truncate(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	var out strings.Builder
	width := 0
	cut := false
	walkSequences(s, func(start, end int, isEscape bool) bool {
		if isEscape {
			out.WriteString(s[start:end])
			return true
		}
		r, _ := utf8.DecodeRuneInString(s[start:end])
		w := runeWidth(r)
		if width+w > cols {
			cut = true
			return false
		}
		width += w
		out.WriteString(s[start:end])
		return true
	})
	if !cut {
		return s
	}
	// 잘린 자리 뒤에 있던 꾸밈 끄기는 버려졌다. 줄 어디에든 꾸밈이 있었으면 전부 끄기로 닫는다.
	if strings.Contains(s, "\x1b[") {
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

// walk는 문자열을 룬 단위로 돌며 표시 폭과 함께 fn 에 넘긴다. ANSI 시퀀스는 건너뛴다.
func walk(s string, fn func(r rune, width int) bool) {
	walkSequences(s, func(start, end int, isEscape bool) bool {
		if isEscape {
			return true
		}
		r, _ := utf8.DecodeRuneInString(s[start:end])
		return fn(r, runeWidth(r))
	})
}

// walkSequences는 문자열을 ANSI CSI 시퀀스와 룬으로 나누어 fn 에 넘긴다. fn 이 거짓을 돌려주면 멈춘다.
//
// CSI 는 `ESC [` 로 시작해 0x40–0x7e 범위의 종결 바이트로 끝난다. 이 패키지가 만드는 꾸밈은 전부 그
// 모양이고, 화면 패키지도 이 패키지의 도우미만 쓰므로 다른 종류의 시퀀스는 다루지 않는다. 종결 바이트
// 없이 끝나는 시퀀스는 끝까지를 시퀀스로 본다.
func walkSequences(s string, fn func(start, end int, isEscape bool) bool) {
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			end := len(s)
			for j := i + 2; j < len(s); j++ {
				if s[j] >= 0x40 && s[j] <= 0x7e {
					end = j + 1
					break
				}
			}
			if !fn(i, end, true) {
				return
			}
			i = end
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if !fn(i, i+size, false) {
			return
		}
		i += size
	}
}

// runeWidth는 글자 하나의 표시 폭이다.
//
// 유니코드 East Asian Width 표 전체를 넣지 않는다. 표를 옮겨 오면 판이 바뀔 때마다 따라가야 하고,
// 이 화면에 나오는 글자는 브랜치 이름과 경로가 거의 전부다. 한글과 주요 CJK 구간, 전각 기호, 그리고
// 흔한 이모지 구간만 두 칸으로 보고 나머지는 한 칸이다. 결합 문자와 폭 없는 문자는 0 이다.
// 화살표(↓↑)처럼 폭이 모호한 글자는 대부분의 터미널이 한 칸으로 그리므로 한 칸이다.
func runeWidth(r rune) int {
	switch {
	case r < 0x20 || r == 0x7f, r >= 0x80 && r < 0xa0:
		return 0
	case r >= 0x0300 && r <= 0x036f, r >= 0x1ab0 && r <= 0x1aff, r >= 0x1dc0 && r <= 0x1dff,
		r >= 0x20d0 && r <= 0x20ff, r >= 0xfe20 && r <= 0xfe2f:
		// 결합 문자. 앞 글자에 얹힌다.
		return 0
	case r >= 0x200b && r <= 0x200f, r >= 0x2060 && r <= 0x2064, r >= 0xfe00 && r <= 0xfe0f:
		// 폭 없는 공백, 결합 제어, 이체자 선택자.
		return 0
	case r >= 0x1160 && r <= 0x11ff, r >= 0xd7b0 && r <= 0xd7ff:
		// 한글 자모의 중성과 종성(자모 확장 B 포함). 초성(두 칸)에 얹혀 한 음절을 이루므로 폭이 없다.
		// NFD 로 분해된 한글이 이 모양이다. macOS 의 HFS+ 가 경로를 NFD 로 저장하므로 화면에 나오는 worktree
		// 경로에 섞여 들어올 수 있고, 이것을 한 칸으로 세면 폭이 두 배로 불어 Truncate 가 음절 한가운데를 자른다.
		return 0
	case r >= 0x1100 && r <= 0x115f, // 한글 자모(초성)
		r >= 0x2e80 && r <= 0x303e,   // CJK 부수, 강희 부수, 한중일 기호와 구두점
		r >= 0x3041 && r <= 0x33ff,   // 히라가나, 가타카나, 주음 부호, 호환 자모, 괄호 한중일, 한중일 호환
		r >= 0x3400 && r <= 0x4dbf,   // 한자 확장 A
		r >= 0x4e00 && r <= 0x9fff,   // 한자
		r >= 0xa000 && r <= 0xa4cf,   // 이 문자
		r >= 0xac00 && r <= 0xd7a3,   // 한글 음절
		r >= 0xf900 && r <= 0xfaff,   // 한자 호환
		r >= 0xfe30 && r <= 0xfe4f,   // 한중일 호환 형태
		r >= 0xff00 && r <= 0xff60,   // 전각 형태
		r >= 0xffe0 && r <= 0xffe6,   // 전각 기호
		r >= 0x1f300 && r <= 0x1f64f, // 이모지
		r >= 0x1f680 && r <= 0x1f6ff, // 교통과 지도 기호
		r >= 0x1f900 && r <= 0x1f9ff, // 보충 기호와 그림 문자
		r >= 0x20000 && r <= 0x3fffd: // 한자 확장 B 이후
		return 2
	default:
		return 1
	}
}

// Color는 ANSI 기본 여덟 색이다. 값은 전경색 코드다.
type Color int

const (
	Black Color = iota + 30
	Red
	Green
	Yellow
	Blue
	Magenta
	Cyan
	White
)

// Bold는 굵게 꾸민다. 끄는 코드는 전부 끄기가 아니라 굵게 끄기(22)라, 겹쳐 쓴 다른 꾸밈이 살아남는다.
func Bold(s string) string {
	if s == "" {
		return ""
	}
	return "\x1b[1m" + s + "\x1b[22m"
}

// Dim은 흐리게 꾸민다. 끄는 코드(22)는 Bold 와 같다. 둘 다 "굵기" 속성이기 때문이다.
func Dim(s string) string {
	if s == "" {
		return ""
	}
	return "\x1b[2m" + s + "\x1b[22m"
}

// Reverse는 전경과 배경을 맞바꾼다. 커서 행을 표시할 때 쓴다.
func Reverse(s string) string {
	if s == "" {
		return ""
	}
	return "\x1b[7m" + s + "\x1b[27m"
}

// Fg는 전경색을 입힌다. 끄는 코드는 기본 전경색(39)이라 바깥의 다른 꾸밈은 그대로다.
func Fg(s string, color Color) string {
	if s == "" {
		return ""
	}
	return "\x1b[" + strconv.Itoa(int(color)) + "m" + s + "\x1b[39m"
}
