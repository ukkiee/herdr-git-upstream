package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"herdr-git-upstream/internal/state"
)

// 잠금 갱신이 갱신 작업과 같은 흐름에 있으면, 오래 걸리는 회차 한 번이 데몬을 죽은 것으로 만든다.
// 그 사이 다른 데몬이 잠금을 빼앗아 둘이 함께 돌게 되므로, 갱신 중에도 하트비트가 이어져야 한다.
func TestLockStaysFreshWhileWorkIsBlocked(t *testing.T) {
	store := newTestStore(t)
	shortHeartbeat(t, 20*time.Millisecond)

	if err := store.AcquireDaemonLock(time.Minute); err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseDaemonLock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var lockLost, stopped atomic.Bool
	go keepLockFresh(ctx, store, cancel, &lockLost, &stopped)

	before := heartbeatAt(t, store)
	// 갱신이 오래 걸리는 상황을 흉내 낸다. 그동안 주 흐름은 아무 일도 하지 않는다.
	// 하트비트는 초 단위로 적히므로, 값이 움직였는지 보려면 1초는 지나야 한다.
	time.Sleep(1500 * time.Millisecond)
	after := heartbeatAt(t, store)

	if after <= before {
		t.Fatalf("일이 막혀 있는 동안에도 잠금이 갱신되어야 한다: %d -> %d", before, after)
	}
	if lockLost.Load() || stopped.Load() {
		t.Fatal("아무 일 없이 물러나면 안 된다")
	}
}

func TestHeartbeatLoopStopsOnRequest(t *testing.T) {
	store := newTestStore(t)
	shortHeartbeat(t, 20*time.Millisecond)
	if err := store.AcquireDaemonLock(time.Minute); err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseDaemonLock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var lockLost, stopped atomic.Bool
	go keepLockFresh(ctx, store, cancel, &lockLost, &stopped)

	if err := store.RequestStop(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return stopped.Load() })
	if ctx.Err() == nil {
		t.Fatal("중지 요청은 진행 중인 일까지 끊어야 한다")
	}
}

// 잠금을 남에게 빼앗겼으면 스스로 물러나야 한다. 이것이 데몬이 둘 도는 것을 막는 마지막 방어선이다.
func TestHeartbeatLoopRetiresWhenLockIsTakenOver(t *testing.T) {
	store := newTestStore(t)
	shortHeartbeat(t, 20*time.Millisecond)
	if err := store.AcquireDaemonLock(time.Minute); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var lockLost, stopped atomic.Bool
	go keepLockFresh(ctx, store, cancel, &lockLost, &stopped)

	// 다른 프로세스가 넘겨받은 상황을 만든다.
	writeLockOwnedBySomeoneElse(t, store)

	waitFor(t, 3*time.Second, func() bool { return lockLost.Load() })
	if ctx.Err() == nil {
		t.Fatal("잠금을 잃으면 진행 중인 일을 끊어야 한다")
	}
}

// herdr는 포커스 한 번에 두 이벤트를 내보내고 각각을 별도 프로세스로 띄운다. 그래서 쪽지를 쓰는 일은
// 늘 겹치는데, 그 틈에 사람이 누른 "지금 갱신"이 묻히면 아무 일도 일어나지 않는다.
func TestForceRequestSurvivesConcurrentFocusWakes(t *testing.T) {
	store := newTestStore(t)
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	for i := 0; i < 50; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = Wake(store, WakeHint{Workspace: "w1"})
		}()
	}
	if err := Wake(store, WakeHint{Force: true}); err != nil {
		t.Fatal(err)
	}
	wait.Wait()

	hint, ok := takeWake(store)
	if !ok {
		t.Fatal("쪽지를 집어 오지 못했다")
	}
	if !hint.Force {
		t.Fatal("강제 갱신 요청이 포커스 쪽지에 묻혔다")
	}
	if hint.Workspace != "" {
		t.Fatalf("강제 갱신은 전체를 돌아야 한다: %q", hint.Workspace)
	}
}

// 강제 요청이 이미 놓여 있는데 또 누르는 것은 실패가 아니다.
func TestRepeatedForceRequestIsNotAnError(t *testing.T) {
	store := newTestStore(t)
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Wake(store, WakeHint{Force: true}); err != nil {
		t.Fatal(err)
	}
	if err := Wake(store, WakeHint{Force: true}); err != nil {
		t.Fatalf("이미 대기 중인 요청이 있는 것은 오류가 아니다: %v", err)
	}
	hint, ok := takeWake(store)
	if !ok || !hint.Force {
		t.Fatal("강제 요청이 남아 있어야 한다")
	}
	if _, ok := takeWake(store); ok {
		t.Fatal("강제 요청은 한 번만 소비되어야 한다")
	}
}

// 집어 오는 도중에 들어온 쪽지는 다음 회차에 읽혀야지, 읽히지 않은 채 지워지면 안 된다.
func TestTakeWakeDoesNotSwallowConcurrentNotes(t *testing.T) {
	store := newTestStore(t)
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}

	lost := 0
	for round := 0; round < 300; round++ {
		if err := Wake(store, WakeHint{Workspace: "first"}); err != nil {
			t.Fatal(err)
		}
		written := make(chan error, 1)
		go func() {
			written <- Wake(store, WakeHint{Workspace: "second"})
		}()
		first, firstOK := takeWake(store)
		if err := <-written; err != nil {
			t.Fatal(err)
		}

		// 마지막 쪽지는 첫 읽기에서 소비됐거나 다음 읽기에 남아 있어야 한다.
		// 첫 읽기 전에 두 번째 쓰기가 끝나는 것도 정상적인 실행 순서다.
		second, secondOK := takeWake(store)
		if !(firstOK && first.Workspace == "second") && !(secondOK && second.Workspace == "second") {
			lost++
		}
	}
	// 두 쪽지 중 하나만 읽히고 다른 하나가 사라지는 일이 잦으면 설계가 잘못된 것이다.
	if lost > 0 {
		t.Fatalf("쪽지가 %d번 사라졌다", lost)
	}
}

func TestWakeWritesAtomically(t *testing.T) {
	store := newTestStore(t)
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var empty atomic.Int64
	go func() {
		defer close(done)
		for i := 0; i < 20000; i++ {
			raw, err := os.ReadFile(wakePath(store))
			if err == nil && len(raw) == 0 {
				empty.Add(1)
			}
		}
	}()
	for i := 0; i < 2000; i++ {
		_ = Wake(store, WakeHint{Workspace: "w1"})
	}
	<-done

	if empty.Load() != 0 {
		t.Fatalf("반쯤 쓰인 쪽지가 %d번 읽혔다", empty.Load())
	}
}

func newTestStore(t *testing.T) state.Store {
	t.Helper()
	dir := t.TempDir()
	return state.Store{Dir: dir, Root: dir}
}

// shortHeartbeat는 시험이 90초를 기다리지 않도록 하트비트 간격을 줄인다.
func shortHeartbeat(t *testing.T, d time.Duration) {
	t.Helper()
	original := heartbeatInterval
	heartbeatInterval = d
	t.Cleanup(func() { heartbeatInterval = original })
}

// heartbeatAt은 잠금 파일에 적힌 마지막 갱신 시각을 읽는다.
// 살아 있는지만 보면 값이 실제로 움직였는지 알 수 없어, 파일의 숫자를 직접 본다.
func heartbeatAt(t *testing.T, store state.Store) int64 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(store.Dir, "daemon.lock"))
	if err != nil {
		t.Fatalf("잠금 파일을 읽지 못했다: %v", err)
	}
	var lock struct {
		HeartbeatUnix int64 `json:"heartbeat_unix"`
	}
	if err := json.Unmarshal(bytes.TrimRight(raw, " \x00\n"), &lock); err != nil {
		t.Fatalf("잠금 파일을 해석하지 못했다: %v", err)
	}
	return lock.HeartbeatUnix
}

// writeLockOwnedBySomeoneElse는 다른 프로세스가 잠금을 넘겨받은 상황을 만든다.
func writeLockOwnedBySomeoneElse(t *testing.T, store state.Store) {
	t.Helper()
	// 잠금 파일을 치우고 다시 만들면, 만든 쪽(이 시험 프로세스)이 주인이 된다. 대신 다른 PID를 심으려면
	// 상태 패키지 안을 건드려야 하므로, 여기서는 파일을 지워 소유 확인이 실패하도록 한다.
	// 지운 뒤 다른 내용으로 다시 만들면 Heartbeat가 PID 불일치를 보고 물러난다.
	path := store.Dir + string(os.PathSeparator) + "daemon.lock"
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"pid":999999,"started_unix":1,"heartbeat_unix":` +
		time.Now().Format("20060102") + `}`)
	padded := make([]byte, 160)
	copy(padded, body)
	for i := len(body); i < len(padded); i++ {
		padded[i] = ' '
	}
	if err := os.WriteFile(path, padded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, limit time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("기다리던 상태가 되지 않았다")
}
