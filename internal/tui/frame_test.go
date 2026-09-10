package tui

import (
	"strings"
	"testing"
)

func TestWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"빈 문자열", "", 0},
		{"영문", "abc", 3},
		{"한글은 두 칸", "한글", 4},
		{"한글과 영문", "a한b", 4},
		{"한자", "漢字", 4},
		{"가나", "ひらがな", 8},
		{"전각 기호", "！", 2},
		{"반각 가타카나는 한 칸", "ｱ", 1},
		{"화살표는 한 칸", "↓12", 3},
		{"꾸밈은 세지 않음", "\x1b[1mab\x1b[22m", 2},
		{"색과 반전이 겹쳐도", Reverse(Fg("safe", Green)), 4},
		{"결합 문자는 폭이 없음", "é", 1},
		{"이체자 선택자는 폭이 없음", "☃️", 1},
		{"제어 문자는 폭이 없음", "a\x01b", 2},
		{"이모지는 두 칸", "🙂", 2},
		{"한글 자모(초성)", "ᄒ", 2},
		{"한글 호환 자모", "ㄱ", 2},
		{"NFD 한글은 두 칸", "\u1112\u1161\u11ab", 2},
		{"NFD 한글 여럿", "\u1112\u1161\u11ab\u1100\u1173\u11af", 4},
		{"자모 확장 B 의 중성·종성은 폭이 없음", "\u1112\ud7b0\ud7cb", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Width(tc.in); got != tc.want {
				t.Fatalf("%q -> %d, 기대값 %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		cols int
		want string
	}{
		{"짧으면 그대로", "abc", 5, "abc"},
		{"딱 맞으면 그대로", "abcde", 5, "abcde"},
		{"넘치면 자름", "abcdef", 4, "abcd"},
		{"한글은 두 칸으로 세어 자름", "한글이다", 4, "한글"},
		{"두 칸짜리가 마지막 한 칸에 걸리면 넣지 않음", "한글이", 5, "한글"},
		{"한글과 영문 섞임", "a한b글c", 5, "a한b"},
		{"0 이하는 빈 문자열", "abc", 0, ""},
		{"꾸밈은 폭에 들지 않음", "\x1b[1mabc\x1b[22m", 3, "\x1b[1mabc\x1b[22m"},
		{"꾸밈 안에서 잘리면 전부 끄기를 붙임", "\x1b[7mabcdef\x1b[27m", 3, "\x1b[7mabc\x1b[0m"},
		{"꾸밈이 잘린 자리 뒤에만 있어도 전부 끄기를 붙임", "abcdef\x1b[7m!\x1b[27m", 3, "abc\x1b[0m"},
		{"꾸밈이 없으면 그냥 자름", "abcdef", 3, "abc"},
		{"NFD 한글은 음절째로 자름", "\u1112\u1161\u11ab\u1100\u1173\u11af", 2, "\u1112\u1161\u11ab"},
		{"NFD 음절이 마지막 한 칸에 걸리면 넣지 않음", "\u1112\u1161\u11ab\u1100\u1173\u11af", 3, "\u1112\u1161\u11ab"},
		{"NFD 한글이 한 칸에는 들어가지 않음", "\u1112\u1161\u11ab", 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Truncate(tc.in, tc.cols); got != tc.want {
				t.Fatalf("%q, %d -> %q, 기대값 %q", tc.in, tc.cols, got, tc.want)
			}
		})
	}
}

// Frame 은 줄마다 커서를 옮기고 줄을 지운 뒤 내용을 쓴다. rows 를 넘는 줄은 버리고 모자란 줄은 비운다.
// 지우기(ESC [K)가 내용보다 앞에 와야 한다. 내용 뒤에 두면 폭이 cols 와 딱 맞는 줄의 마지막 칸을 지우는
// 에뮬레이터(xterm, alacritty)가 있다.
func TestFrame(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		cols  int
		rows  int
		want  string
	}{
		{
			"줄마다 커서 이동과 지우기 뒤에 내용",
			[]string{"one", "two"}, 10, 2,
			"\x1b[1;1H\x1b[Kone\x1b[2;1H\x1b[Ktwo",
		},
		{
			"폭과 딱 맞는 줄도 지우기가 내용 앞에 옴(대기 중인 줄 바꿈 상태에서 EL 이 마지막 칸을 지우는 에뮬레이터가 있다)",
			[]string{Reverse("abcde")}, 5, 1,
			"\x1b[1;1H\x1b[K\x1b[7mabcde\x1b[27m",
		},
		{
			"rows 를 넘는 줄은 버림",
			[]string{"one", "two", "three"}, 10, 2,
			"\x1b[1;1H\x1b[Kone\x1b[2;1H\x1b[Ktwo",
		},
		{
			"모자란 줄은 비움",
			[]string{"one"}, 10, 3,
			"\x1b[1;1H\x1b[Kone\x1b[2;1H\x1b[K\x1b[3;1H\x1b[K",
		},
		{
			"폭을 넘는 줄은 룬 단위로 자름(한글 두 칸)",
			[]string{"abcdef", "한글이다", "third"}, 4, 3,
			"\x1b[1;1H\x1b[Kabcd\x1b[2;1H\x1b[K한글\x1b[3;1H\x1b[Kthir",
		},
		{
			"꾸민 줄이 잘리면 전부 끄기를 붙여 다음 줄로 번지지 않게 함",
			[]string{Reverse("abcdef"), "x"}, 3, 2,
			"\x1b[1;1H\x1b[K\x1b[7mabc\x1b[0m\x1b[2;1H\x1b[Kx",
		},
		{"줄이 없어도 화면을 비움", nil, 5, 1, "\x1b[1;1H\x1b[K"},
		{"행이 0 이면 아무것도 쓰지 않음", []string{"a"}, 5, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			Frame(&out, tc.lines, tc.cols, tc.rows)
			if got := out.String(); got != tc.want {
				t.Fatalf("%q, 기대값 %q", got, tc.want)
			}
		})
	}
}

// 꾸밈 도우미는 켠 것만 끈다. 겹쳐 써도 바깥 꾸밈이 살아남아야 한다.
func TestStyles(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Bold", Bold("a"), "\x1b[1ma\x1b[22m"},
		{"Dim", Dim("a"), "\x1b[2ma\x1b[22m"},
		{"Reverse", Reverse("a"), "\x1b[7ma\x1b[27m"},
		{"Fg green", Fg("a", Green), "\x1b[32ma\x1b[39m"},
		{"Fg yellow", Fg("a", Yellow), "\x1b[33ma\x1b[39m"},
		{"겹침", Reverse(Fg("a", Red)), "\x1b[7m\x1b[31ma\x1b[39m\x1b[27m"},
		{"빈 문자열은 꾸미지 않음", Bold("") + Dim("") + Reverse("") + Fg("", Blue), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("%q, 기대값 %q", tc.got, tc.want)
			}
		})
	}
}
