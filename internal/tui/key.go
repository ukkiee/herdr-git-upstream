package tui

import (
	"context"
	"io"
	"os"
	"time"
	"unicode/utf8"
)

// KeyKind는 키의 종류다. 화면이 구별해야 하는 것만 둔다.
type KeyKind int

const (
	// KeyRune은 글자 하나다. Key.Rune 에 그 글자가 든다.
	KeyRune KeyKind = iota
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyEnter
	KeyEsc
	KeyBackspace
	KeyTab
	// KeyCtrlC는 Ctrl-C 다. raw 모드에서는 신호가 아니라 바이트(0x03)로 오므로 키로 다룬다.
	KeyCtrlC
	// KeyResize는 창 크기가 바뀌었다는 뜻이다. 키가 아니지만 같은 채널로 오면 화면 루프가 하나로 끝난다.
	KeyResize
)

// Key는 사용자가 누른 키 하나다.
type Key struct {
	Kind KeyKind
	Rune rune
}

// ParseKeys는 터미널에서 읽은 바이트를 키로 바꾼다. 순수 함수다.
//
// 아직 끝나지 않은 조각(ESC 시퀀스의 앞부분, UTF-8 의 앞 바이트)은 rest 로 돌려주어 다음 읽기와 이어
// 붙이게 한다. ESC 하나만 있고 뒤에 아무것도 없으면 단독 ESC 키다. 터미널은 화살표 같은 시퀀스를 한 번에
// 쓰므로, ESC 가 혼자 읽혔다는 것은 사람이 ESC 를 눌렀다는 뜻이다. ESC 뒤에 `[` 나 `O` 가 아닌 바이트가
// 오면 Alt 를 누른 채 친 글자로 보고 ESC 는 버린다. 모르는 시퀀스(Home, F1 같은 것)와 다루지 않는
// 제어 문자는 버린다. 화면이 쓰지 않는 키를 글자로 돌려주면 엉뚱한 자리에 찍힌다.
func ParseKeys(buf []byte) (keys []Key, rest []byte) {
	for i := 0; i < len(buf); {
		b := buf[i]
		switch {
		case b == 0x1b:
			if i+1 == len(buf) {
				keys = append(keys, Key{Kind: KeyEsc})
				i++
				continue
			}
			if next := buf[i+1]; next == '[' || next == 'O' {
				key, n, complete := parseEscape(buf[i:])
				if !complete {
					return keys, buf[i:]
				}
				if key.Kind != KeyRune {
					keys = append(keys, key)
				}
				i += n
				continue
			}
			// Alt+글자. ESC 를 버리고 뒤의 바이트를 보통 글자로 읽는다.
			i++
		case b == '\r':
			keys = append(keys, Key{Kind: KeyEnter})
			i++
			// 붙여 넣은 CRLF 는 Enter 한 번이다.
			if i < len(buf) && buf[i] == '\n' {
				i++
			}
		case b == '\n':
			keys = append(keys, Key{Kind: KeyEnter})
			i++
		case b == 0x7f || b == 0x08:
			// 터미널에 따라 Backspace 가 DEL(0x7f) 로도 BS(0x08) 로도 온다.
			keys = append(keys, Key{Kind: KeyBackspace})
			i++
		case b == '\t':
			keys = append(keys, Key{Kind: KeyTab})
			i++
		case b == 0x03:
			keys = append(keys, Key{Kind: KeyCtrlC})
			i++
		case b < 0x20:
			i++
		default:
			r, size := utf8.DecodeRune(buf[i:])
			if r == utf8.RuneError && size <= 1 {
				if !utf8.FullRune(buf[i:]) {
					return keys, buf[i:]
				}
				// 깨진 바이트다. 버리고 다음으로 간다.
				i++
				continue
			}
			keys = append(keys, Key{Kind: KeyRune, Rune: r})
			i += size
		}
	}
	return keys, nil
}

// parseEscape는 ESC 로 시작하는 시퀀스 하나를 읽는다. seq[0] 은 ESC, seq[1] 은 `[` 나 `O` 다.
//
// CSI(`ESC [`)는 매개변수 바이트(0x30–0x3f)와 중간 바이트(0x20–0x2f)가 이어지다 종결 바이트(0x40–0x7e)로
// 끝난다. 종결 바이트가 아직 없으면 끝나지 않은 것이다. SS3(`ESC O`)는 종결 바이트 하나가 바로 온다.
// 어느 쪽이든 종결 바이트가 A/B/C/D 면 화살표다. `ESC [ 1 ; 5 A` 처럼 수식 키가 붙은 화살표도 매개변수만
// 다르므로 같은 화살표로 읽는다. 그 밖의 시퀀스는 KeyRune 으로 표시해 호출자가 버리게 한다.
// 시퀀스 안에 올 수 없는 바이트를 만나면 거기서 끊고, 그 바이트는 소비하지 않는다.
func parseEscape(seq []byte) (key Key, n int, complete bool) {
	if seq[1] == 'O' {
		if len(seq) < 3 {
			return Key{}, 0, false
		}
		return arrowKey(seq[2]), 3, true
	}
	for j := 2; j < len(seq); j++ {
		c := seq[j]
		switch {
		case c >= 0x40 && c <= 0x7e:
			return arrowKey(c), j + 1, true
		case c >= 0x20 && c <= 0x3f:
			continue
		default:
			return Key{}, j, true
		}
	}
	return Key{}, 0, false
}

func arrowKey(final byte) Key {
	switch final {
	case 'A':
		return Key{Kind: KeyUp}
	case 'B':
		return Key{Kind: KeyDown}
	case 'C':
		return Key{Kind: KeyRight}
	case 'D':
		return Key{Kind: KeyLeft}
	default:
		return Key{}
	}
}

// escapeTimeout은 끝나지 않은 조각을 기다리는 시간이다.
//
// ESC 시퀀스는 보통 한 번의 쓰기로 오지만, 느린 연결(ssh, mosh)에서는 ESC 와 나머지가 갈라져 도착할 수 있다.
// 그래서 ReadKeys 는 조각 끝의 단독 ESC 와 끝나지 않은 시퀀스(`ESC [` 까지만 온 것)를 이만큼 붙들었다가
// 다음 조각과 이어 붙인다. 다만 영원히 기다리면 사람이 ESC 를 누른 뒤 아무것도 안 했을 때 화면이 멈춘
// 것처럼 보이므로, 이 시간이 지나면 조각의 첫 ESC 를 단독 ESC 로 보낸다. 사람이 느끼지 못할 만큼 짧고
// 갈라진 시퀀스가 모이기에는 넉넉한 값이다.
const escapeTimeout = 50 * time.Millisecond

// ReadKeys는 r 을 읽어 키를 채널로 보낸다. 유닉스에서는 창 크기 변경(SIGWINCH)도 KeyResize 로 보낸다.
//
// ctx 가 끝나거나 r 이 끝(EOF, 오류)에 닿으면 채널을 닫는다. 읽기는 다른 고루틴이 하며 막힌 읽기를
// 끊을 방법은 없으므로, ctx 가 끝난 뒤에도 그 고루틴은 다음 바이트가 올 때까지 남는다. 프로세스가 곧
// 끝나는 자리에서만 쓰는 것을 전제로 한다.
//
// 조각 끝에 홀로 남은 ESC 는 ParseKeys 에 바로 넘기지 않고 escapeTimeout 만큼 붙든다. ParseKeys 는 "뒤에
// 바이트가 없는 ESC 는 단독 ESC" 라고 읽는데, 그 말은 버퍼 하나 안에서만 맞는다. 갈라져 온 화살표의 앞머리를
// 단독 ESC 로 내보내면 느린 연결에서 ↑ 를 눌렀는데 화면이 닫힌다(Esc 가 닫기다).
//
// 고루틴은 그냥 go 로 띄운다. 터미널 안에서 쓸 때는 Terminal.Keys 가 같은 일을 Go 로 띄워 패닉에도 되돌린다.
func ReadKeys(ctx context.Context, r io.Reader) <-chan Key {
	return readKeys(ctx, r, func(fn func()) { go fn() })
}

// readKeys는 ReadKeys 의 본체다. spawn 은 고루틴을 띄우는 방법이다.
func readKeys(ctx context.Context, r io.Reader, spawn func(func())) <-chan Key {
	keys := make(chan Key, 16)
	chunks := make(chan []byte)
	spawn(func() {
		defer close(chunks)
		for {
			buf := make([]byte, 256)
			n, err := r.Read(buf)
			if n > 0 {
				select {
				case chunks <- buf[:n]:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	})

	resize := make(chan os.Signal, 1)
	stopResize := notifyResize(resize)
	spawn(func() {
		defer close(keys)
		defer stopResize()
		emit := func(list []Key) bool {
			for _, key := range list {
				select {
				case keys <- key:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		var pending []byte
		var flush <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-resize:
				if !emit([]Key{{Kind: KeyResize}}) {
					return
				}
			case <-flush:
				flush = nil
				if !emit(flushPending(pending)) {
					return
				}
				pending = nil
			case chunk, ok := <-chunks:
				if !ok {
					emit(flushPending(pending))
					return
				}
				buf := append(pending, chunk...)
				// 조각 끝의 ESC 는 붙들어 둔다. 다음 조각이 오면 그 앞에 이어 붙고, 오지 않으면 flush 가
				// flushPending 으로 단독 ESC 를 낸다.
				held := 0
				if buf[len(buf)-1] == 0x1b {
					held = 1
				}
				parsed, rest := ParseKeys(buf[:len(buf)-held])
				if !emit(parsed) {
					return
				}
				// rest 는 방금 이어 붙인 버퍼의 일부다. 다음 chunk 를 그 뒤에 붙여도 되지만, 남은 조각만
				// 따로 복사해 두면 오래된 버퍼가 붙들려 있지 않다.
				pending = append(append([]byte(nil), rest...), buf[len(buf)-held:]...)
				if len(pending) > 0 {
					flush = time.After(escapeTimeout)
				} else {
					flush = nil
				}
			}
		}
	})
	return keys
}

// flushPending은 기다려도 끝나지 않은 조각을 키로 바꾼다.
//
// ESC 로 시작하면 그 ESC 는 단독 ESC 고 나머지는 보통 바이트로 다시 읽는다. `ESC [` 만 온 채 멈췄다면
// 사람이 ESC 를 누르고 `[` 를 친 것이다. ESC 가 아닌 조각은 반쪽짜리 UTF-8 이라 버린다.
func flushPending(pending []byte) []Key {
	if len(pending) == 0 || pending[0] != 0x1b {
		return nil
	}
	keys, _ := ParseKeys(pending[1:])
	return append([]Key{{Kind: KeyEsc}}, keys...)
}
