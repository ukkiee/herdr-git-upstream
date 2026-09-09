package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDueAt(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	throttle := time.Minute

	if !(Record{}).DueAt(now, throttle) {
		t.Fatal("한 번도 시도하지 않은 저장소는 곧바로 가져올 수 있어야 한다")
	}
	recent := Record{LastAttemptUnix: now.Add(-30 * time.Second).Unix()}
	if recent.DueAt(now, throttle) {
		t.Fatal("스로틀 안에서는 다시 가져오지 않아야 한다")
	}
	old := Record{LastAttemptUnix: now.Add(-90 * time.Second).Unix()}
	if !old.DueAt(now, throttle) {
		t.Fatal("스로틀이 지나면 다시 가져와야 한다")
	}
}

// 첫 실패만으로 곧장 경고를 띄우면, 잠깐 끊긴 네트워크와 정말 손봐야 하는 상황을 구별할 수 없다.
func TestStaleWaitsForTheFailureToPersist(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	after := 15 * time.Minute

	first := Record{}.MarkFailure(now, "연결 거부", false)
	if first.Stale(now, after) {
		t.Fatal("실패가 막 시작된 시점에는 경고하지 않아야 한다")
	}
	if first.Stale(now.Add(14*time.Minute), after) {
		t.Fatal("기준 시간 전에는 경고하지 않아야 한다")
	}
	if !first.Stale(now.Add(16*time.Minute), after) {
		t.Fatal("실패가 기준 시간을 넘겨 이어지면 경고해야 한다")
	}
}

func TestStaleClearsAfterSuccess(t *testing.T) {
	now := time.Unix(3_000_000, 0)
	after := time.Minute

	failing := Record{}.MarkFailure(now, "연결 거부", false)
	recovered := failing.MarkSuccess(now.Add(2 * time.Minute))
	if recovered.Stale(now.Add(3*time.Minute), after) {
		t.Fatal("한 번 성공하면 경고가 사라져야 한다")
	}
	if recovered.FailingSinceUnix != 0 {
		t.Fatal("성공하면 실패 시작 시각을 지워야 한다")
	}
}

// 실패가 이어지는 동안 시작 시각이 갱신되면 경고가 영원히 뜨지 않는다.
func TestFailingSinceIsNotResetWhileFailing(t *testing.T) {
	start := time.Unix(4_000_000, 0)
	record := Record{}.MarkFailure(start, "첫 실패", false)
	record = record.MarkFailure(start.Add(10*time.Minute), "두 번째 실패", false)
	if record.FailingSinceUnix != start.Unix() {
		t.Fatalf("실패 시작 시각이 유지되어야 한다: %d != %d", record.FailingSinceUnix, start.Unix())
	}
	if !record.Stale(start.Add(11*time.Minute), 5*time.Minute) {
		t.Fatal("첫 실패로부터 기준 시간이 지났으면 경고해야 한다")
	}
}

// 원격에서 브랜치가 사라진 경우처럼 손쓸 수 없는 실패는 경고 대상이 아니다.
func TestPermanentFailureNeverGoesStale(t *testing.T) {
	now := time.Unix(5_000_000, 0)
	record := Record{}.MarkFailure(now, "couldn't find remote ref", true)
	if record.Stale(now.Add(24*time.Hour), time.Minute) {
		t.Fatal("영구 실패는 경고하지 않아야 한다")
	}
}

func TestRecordRoundTrip(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	key := Key("어떤/저장소\x00origin")
	want := Record{LastAttemptUnix: 11, LastSuccessUnix: 22, LastError: "오류", FailingSinceUnix: 33}

	if err := store.SaveRecord(key, want); err != nil {
		t.Fatalf("기록을 저장하지 못했다: %v", err)
	}
	if got := store.LoadRecord(key); got != want {
		t.Fatalf("읽어 온 기록이 다르다: %+v != %+v", got, want)
	}
}

// 깨진 파일은 오류가 아니라 "기록 없음"으로 다뤄야, 다음 회차에 저절로 회복된다.
func TestLoadRecordToleratesGarbage(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	key := Key("깨진 것")
	path := filepath.Join(store.Dir, "fetch", key+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{이건 JSON이 아니다"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := store.LoadRecord(key); got != (Record{}) {
		t.Fatalf("깨진 파일은 빈 기록으로 읽어야 한다: %+v", got)
	}
}

func TestDaemonLockIsExclusive(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.AcquireDaemonLock(time.Minute); err != nil {
		t.Fatalf("첫 잠금을 얻지 못했다: %v", err)
	}
	// 같은 프로세스에서 다시 얻으려 해도, 하트비트가 아직 신선하므로 거절되어야 한다.
	if err := store.AcquireDaemonLock(time.Minute); err != ErrDaemonRunning {
		t.Fatalf("두 번째 잠금은 거절되어야 한다: %v", err)
	}
	if !store.DaemonAlive(time.Minute) {
		t.Fatal("잠금을 쥐고 있으면 살아 있는 것으로 보여야 한다")
	}
	store.ReleaseDaemonLock()
	if store.DaemonAlive(time.Minute) {
		t.Fatal("잠금을 놓으면 죽은 것으로 보여야 한다")
	}
}

// 데몬이 죽으면서 잠금 파일을 남겼을 때, 다음 데몬이 넘겨받을 수 있어야 한다.
func TestDaemonLockTakeoverAfterStaleHeartbeat(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.AcquireDaemonLock(time.Minute); err != nil {
		t.Fatal(err)
	}
	// 하트비트를 과거로 되돌려, 앞선 데몬이 오래 멈춰 있는 상황을 만든다.
	lock, err := store.readLock()
	if err != nil {
		t.Fatal(err)
	}
	lock.PID = os.Getpid() + 1
	lock.HeartbeatUnix = time.Now().Add(-time.Hour).Unix()
	raw, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(store.lockPath(), raw); err != nil {
		t.Fatal(err)
	}

	if err := store.AcquireDaemonLock(time.Minute); err != nil {
		t.Fatalf("멈춘 데몬의 잠금은 넘겨받을 수 있어야 한다: %v", err)
	}
	if !store.Heartbeat() {
		t.Fatal("넘겨받은 뒤에는 하트비트를 남길 수 있어야 한다")
	}
}

func TestStopRequestIsConsumedOnce(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if store.StopRequested() {
		t.Fatal("요청이 없었는데 있다고 답했다")
	}
	if err := store.RequestStop(); err != nil {
		t.Fatal(err)
	}
	if !store.StopRequested() {
		t.Fatal("중지 요청을 보지 못했다")
	}
	if store.StopRequested() {
		t.Fatal("중지 요청은 한 번만 소비되어야 한다")
	}
}
