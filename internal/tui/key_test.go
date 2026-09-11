package tui

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseKeys(t *testing.T) {
	rune_ := func(r rune) Key { return Key{Kind: KeyRune, Rune: r} }
	kind := func(k KeyKind) Key { return Key{Kind: k} }
	cases := []struct {
		name string
		in   string
		keys []Key
		rest string
	}{
		{"빈 입력", "", nil, ""},
		{"글자", "j", []Key{rune_('j')}, ""},
		{"글자 여럿", "jk", []Key{rune_('j'), rune_('k')}, ""},
		{"한글 룬", "한", []Key{rune_('한')}, ""},
		{"한글과 영문 섞임", "a한b", []Key{rune_('a'), rune_('한'), rune_('b')}, ""},
		{"단독 ESC", "\x1b", []Key{kind(KeyEsc)}, ""},
		{"위", "\x1b[A", []Key{kind(KeyUp)}, ""},
		{"아래", "\x1b[B", []Key{kind(KeyDown)}, ""},
		{"오른쪽", "\x1b[C", []Key{kind(KeyRight)}, ""},
		{"왼쪽", "\x1b[D", []Key{kind(KeyLeft)}, ""},
		{"SS3 화살표(응용 모드)", "\x1bOA\x1bOB", []Key{kind(KeyUp), kind(KeyDown)}, ""},
		{"수식 키가 붙은 화살표", "\x1b[1;5A", []Key{kind(KeyUp)}, ""},
		{"Enter CR", "\r", []Key{kind(KeyEnter)}, ""},
		{"Enter LF", "\n", []Key{kind(KeyEnter)}, ""},
		{"CRLF 는 Enter 한 번", "\r\n", []Key{kind(KeyEnter)}, ""},
		{"CR 둘은 Enter 둘", "\r\r", []Key{kind(KeyEnter), kind(KeyEnter)}, ""},
		{"Backspace DEL", "\x7f", []Key{kind(KeyBackspace)}, ""},
		{"Backspace BS", "\x08", []Key{kind(KeyBackspace)}, ""},
		{"Tab", "\t", []Key{kind(KeyTab)}, ""},
		{"Ctrl-C", "\x03", []Key{kind(KeyCtrlC)}, ""},
		{"다루지 않는 제어 문자는 버림", "\x01\x1a", nil, ""},
		{"글자와 화살표가 이어짐", "jk\x1b[Aq", []Key{rune_('j'), rune_('k'), kind(KeyUp), rune_('q')}, ""},
		{"끝나지 않은 CSI 는 rest", "j\x1b[", []Key{rune_('j')}, "\x1b["},
		{"매개변수까지 왔지만 종결 바이트가 없음", "\x1b[1;5", nil, "\x1b[1;5"},
		{"끝나지 않은 SS3 는 rest", "\x1bO", nil, "\x1bO"},
		{"모르는 CSI 는 버림", "\x1b[3~q", []Key{rune_('q')}, ""},
		{"모르는 SS3 는 버림", "\x1bOQq", []Key{rune_('q')}, ""},
		{"F1 SS3", "\x1bOP", []Key{kind(KeyF1)}, ""},
		{"F1 CSI", "\x1b[11~", []Key{kind(KeyF1)}, ""},
		{"Alt+글자는 ESC 를 버리고 글자만", "\x1bj", []Key{rune_('j')}, ""},
		{"ESC 둘은 단독 ESC 하나", "\x1b\x1b", []Key{kind(KeyEsc)}, ""},
		{"CSI 안에 올 수 없는 바이트에서 끊음", "\x1b[\x1b", []Key{kind(KeyEsc)}, ""},
		{"반쪽짜리 UTF-8 은 rest", "a\xed\x95", []Key{rune_('a')}, "\xed\x95"},
		{"깨진 바이트는 버림", "\xffa", []Key{rune_('a')}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys, rest := ParseKeys([]byte(tc.in))
			if !reflect.DeepEqual(keys, tc.keys) || string(rest) != tc.rest {
				t.Fatalf("%q -> %+v rest=%q, 기대값 %+v rest=%q", tc.in, keys, rest, tc.keys, tc.rest)
			}
		})
	}
}

// 조각으로 나뉘어 도착한 시퀀스는 이어 붙여 읽고, 끝(EOF)에서는 남은 조각을 단독 ESC 로 푼다.
func TestReadKeysJoinsChunksAndFlushesAtEOF(t *testing.T) {
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keys := ReadKeys(ctx, reader)

	go func() {
		_, _ = writer.Write([]byte("j\x1b["))
		_, _ = writer.Write([]byte("A"))
		_, _ = writer.Write([]byte("\x1b["))
		_ = writer.Close()
	}()

	var got []Key
	for key := range keys {
		got = append(got, key)
	}
	want := []Key{{Kind: KeyRune, Rune: 'j'}, {Kind: KeyUp}, {Kind: KeyEsc}, {Kind: KeyRune, Rune: '['}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v, 기대값 %+v", got, want)
	}
}

// 갈라져 온 시퀀스는 이어 붙이고, 끝나지 않은 채 시간이 지나면 단독 ESC 로 푼다.
//
// 조각은 io.Pipe 로 따로따로 쓰고 writer 를 닫지 않아 EOF 경로가 아니라 flush 타이머 경로를 지나게 한다.
// 기다려야 하는 경우는 첫 키가 escapeTimeout 뒤에 왔는지도 본다. 그래야 타이머가 실제로 걸렸음을 안다.
func TestReadKeysJoinsSplitSequencesAndFlushesAfterTimeout(t *testing.T) {
	rune_ := func(r rune) Key { return Key{Kind: KeyRune, Rune: r} }
	kind := func(k KeyKind) Key { return Key{Kind: k} }
	cases := []struct {
		name   string
		chunks []string
		want   []Key
		// waits 는 첫 키가 escapeTimeout 을 다 기다린 뒤에 와야 한다는 뜻이다.
		waits bool
	}{
		{"끝나지 않은 CSI 는 시간이 지나면 단독 ESC 와 글자", []string{"\x1b["}, []Key{kind(KeyEsc), rune_('[')}, true},
		{"단독 ESC 는 다음 조각을 기다렸다가 보냄", []string{"\x1b"}, []Key{kind(KeyEsc)}, true},
		{"글자 뒤의 단독 ESC 도 글자는 바로, ESC 는 기다렸다가", []string{"j\x1b"}, []Key{rune_('j'), kind(KeyEsc)}, false},
		{"ESC 와 나머지가 갈라져 와도 화살표 하나", []string{"\x1b", "[A"}, []Key{kind(KeyUp)}, false},
		{"종결 바이트만 갈라져 와도 화살표 하나", []string{"\x1b[", "B"}, []Key{kind(KeyDown)}, false},
		{"세 조각으로 갈라져도 화살표 하나", []string{"\x1b", "[", "C"}, []Key{kind(KeyRight)}, false},
		{"ESC 뒤에 글자가 갈라져 오면 Alt+글자", []string{"\x1b", "j"}, []Key{rune_('j')}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader, writer := io.Pipe()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer writer.Close()
			keys := ReadKeys(ctx, reader)

			start := time.Now()
			go func() {
				for i, chunk := range tc.chunks {
					if i > 0 {
						time.Sleep(5 * time.Millisecond)
					}
					_, _ = writer.Write([]byte(chunk))
				}
			}()

			var got []Key
			var first time.Duration
			for len(got) < len(tc.want) {
				select {
				case key := <-keys:
					if len(got) == 0 {
						first = time.Since(start)
					}
					got = append(got, key)
				case <-time.After(2 * time.Second):
					t.Fatalf("키가 오지 않았다. 지금까지 %+v, 기대값 %+v", got, tc.want)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%+v, 기대값 %+v", got, tc.want)
			}
			if tc.waits && first < escapeTimeout {
				t.Fatalf("첫 키가 %v 만에 왔다. 타이머(%v)를 기다렸어야 한다", first, escapeTimeout)
			}
			// 이어 붙인 조각의 찌꺼기('[' 나 'A' 같은 글자)가 뒤따라 오면 안 된다.
			select {
			case key := <-keys:
				t.Fatalf("기대하지 않은 키가 더 왔다: %+v", key)
			case <-time.After(3 * escapeTimeout):
			}
		})
	}
}

// readKeys 는 고루틴 둘(읽기, 해석)을 모두 spawn 으로 띄운다. Terminal.Keys 가 그 자리에 Go 를 넣어
// 패닉에도 터미널을 되돌리므로, 하나라도 go 로 직접 띄우면 그 약속이 새어 나간다.
func TestReadKeysSpawnsEveryGoroutineThroughSpawn(t *testing.T) {
	spawned := 0
	keys := readKeys(context.Background(), strings.NewReader("q"), func(fn func()) {
		spawned++
		go fn()
	})
	var got []Key
	for key := range keys {
		got = append(got, key)
	}
	if want := []Key{{Kind: KeyRune, Rune: 'q'}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v, 기대값 %+v", got, want)
	}
	if spawned != 2 {
		t.Fatalf("고루틴 %d개를 spawn 으로 띄웠다. 읽기와 해석 둘이어야 한다", spawned)
	}
}

// ctx 가 끝나면 채널이 닫힌다. 화면 루프가 range 로 끝날 수 있어야 한다.
func TestReadKeysClosesOnContextDone(t *testing.T) {
	reader, _ := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	keys := ReadKeys(ctx, reader)
	cancel()
	select {
	case _, ok := <-keys:
		if ok {
			t.Fatal("취소 뒤에는 키가 아니라 닫힘이어야 한다")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("취소 뒤에도 채널이 닫히지 않았다")
	}
}

func TestReadKeysFromStringReader(t *testing.T) {
	keys := ReadKeys(context.Background(), strings.NewReader("q\r"))
	var got []Key
	for key := range keys {
		got = append(got, key)
	}
	want := []Key{{Kind: KeyRune, Rune: 'q'}, {Kind: KeyEnter}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v, 기대값 %+v", got, want)
	}
}
