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
	if err := WriteFileAtomic(store.lockPath(), raw); err != nil {
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

// 잠금 파일을 잠깐 읽거나 쓰지 못한 것은 소유권을 잃었다는 뜻이 아니다. 그것을 물러날 이유로 삼으면
// 윈도우처럼 다른 프로세스가 파일을 열어 둔 동안 쓰기가 막히는 환경에서 데몬이 무작위로 죽는다.
func TestHeartbeatOnlyReportsLossOfOwnership(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.AcquireDaemonLock(time.Minute); err != nil {
		t.Fatal(err)
	}
	if !store.Heartbeat() {
		t.Fatal("우리가 쥔 잠금에서는 참이어야 한다")
	}

	// 파일이 사라진 경우: 다시 세워 두고 계속 돈다.
	if err := os.Remove(store.lockPath()); err != nil {
		t.Fatal(err)
	}
	if !store.Heartbeat() {
		t.Fatal("잠금이 사라진 것은 소유권 상실이 아니다")
	}
	if !store.DaemonAlive(time.Minute) {
		t.Fatal("사라진 잠금을 다시 세워야 한다")
	}

	// 내용이 깨진 경우: 판단 근거가 없으므로 계속 돈다.
	if err := os.WriteFile(store.lockPath(), []byte("이건 JSON이 아니다"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !store.Heartbeat() {
		t.Fatal("읽을 수 없는 잠금은 소유권 상실이 아니다")
	}

	// 주인이 바뀐 경우에만 물러난다.
	raw, err := encodeLock(daemonLock{PID: os.Getpid() + 1, StartedUnix: 1, HeartbeatUnix: time.Now().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.lockPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if store.Heartbeat() {
		t.Fatal("주인이 바뀌었으면 물러나야 한다")
	}
}

// 하트비트가 파일을 갈아치우면, 그 사이 넘겨받기가 끝났을 때 새 주인의 잠금을 옛 주인 것으로 되돌린다.
// 제자리에 같은 길이로 쓰면 그 일이 생기지 않고, 반쯤 쓰인 파일이 읽히는 구간도 없다.
func TestHeartbeatWritesInPlaceWithFixedSize(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.AcquireDaemonLock(time.Minute); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(store.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != lockRecordSize {
		t.Fatalf("잠금 파일은 정해진 길이여야 한다: %d", before.Size())
	}
	if !store.Heartbeat() {
		t.Fatal(err)
	}
	after, err := os.Stat(store.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != lockRecordSize {
		t.Fatalf("갱신 뒤에도 길이가 같아야 한다: %d", after.Size())
	}
	if !os.SameFile(before, after) {
		t.Fatal("갱신은 같은 파일에 제자리로 이루어져야 한다")
	}
}

// herdr 세션마다 서버가 따로이므로 데몬도 따로 떠야 한다. 잠금을 나눠 쓰면 먼저 뜬 데몬이
// 다른 세션의 데몬까지 막아, 그 세션에는 영영 토큰이 오지 않는다.
func TestDaemonLockIsScopedToTheServer(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)

	t.Setenv("HERDR_SOCKET_PATH", "/tmp/session-one.sock")
	first := New()
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/session-two.sock")
	second := New()

	if first.Dir == second.Dir {
		t.Fatalf("서버가 다르면 잠금 자리도 달라야 한다: %q", first.Dir)
	}
	if first.Root != second.Root {
		t.Fatalf("fetch 기록은 나눠 써야 한다: %q != %q", first.Root, second.Root)
	}
	if err := first.AcquireDaemonLock(time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := second.AcquireDaemonLock(time.Minute); err != nil {
		t.Fatalf("다른 세션의 데몬은 막히지 않아야 한다: %v", err)
	}
}

// 같은 서버라면 어디서 부르든 같은 자리를 봐야 한다. 그러지 않으면 셸에서 부른 명령이
// herdr 가 띄운 데몬과 다른 잠금을 보고 두 번째 데몬을 띄운다.
func TestSameServerResolvesToTheSameDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/session-one.sock")
	if New().Dir != New().Dir {
		t.Fatal("같은 서버는 같은 자리를 가리켜야 한다")
	}
}

// 따라잡기 기록은 fetch 기록과 다른 파일에 산다. 같은 열쇠로 저장해도 서로를 덮지 않아야, 판정 경로가
// 다른 데몬이 남긴 fetch 결과를 되돌리는 일이 없다.
func TestCatchupRecordRoundTripIsSeparateFromFetchRecord(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	key := Key("catchup")
	if got := store.LoadCatchupRecord(key); got != (CatchupRecord{}) {
		t.Fatalf("없는 기록은 비어 있어야 한다: %+v", got)
	}
	fetched := Record{LastAttemptUnix: 1, LastSuccessUnix: 1}
	if err := store.SaveRecord(key, fetched); err != nil {
		t.Fatal(err)
	}
	want := CatchupRecord{Head: "aaa", Tracking: "bbb", Result: "conflict", CheckedUnix: 7}
	if err := store.SaveCatchupRecord(key, want); err != nil {
		t.Fatal(err)
	}
	if got := store.LoadCatchupRecord(key); got != want {
		t.Fatalf("읽어 온 따라잡기 기록이 다르다: %+v != %+v", got, want)
	}
	if got := store.LoadRecord(key); got != fetched {
		t.Fatalf("따라잡기 기록이 fetch 기록을 덮었다: %+v", got)
	}
}

// 원격에 다시 물을 때는 한 번도 물은 적 없거나 마지막 물음이 recheck 보다 오래되었을 때다.
func TestRepoRecordLookupDue(t *testing.T) {
	now := time.Unix(100_000, 0)
	cases := []struct {
		name   string
		record RepoRecord
		want   bool
	}{
		{"물은 적 없음", RepoRecord{}, true},
		{"방금 물음", RepoRecord{DefaultCheckedUnix: now.Add(-time.Minute).Unix()}, false},
		{"하루 안", RepoRecord{DefaultCheckedUnix: now.Add(-23 * time.Hour).Unix()}, false},
		{"하루 지남", RepoRecord{DefaultCheckedUnix: now.Add(-25 * time.Hour).Unix()}, true},
		{"알아냈어도 시각만 본다", RepoRecord{DefaultTrackingRef: "refs/remotes/origin/main", DefaultCheckedUnix: now.Add(-25 * time.Hour).Unix()}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.record.LookupDue(now, 24*time.Hour); got != tc.want {
				t.Fatalf("%+v -> %v, 기대값 %v", tc.record, got, tc.want)
			}
		})
	}
}

func TestRepoRecordRoundTrip(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	key := Key("어떤/저장소\x00origin")
	if got := store.LoadRepoRecord(key); got != (RepoRecord{}) {
		t.Fatalf("없는 기록은 비어 있어야 한다: %+v", got)
	}
	want := RepoRecord{DefaultRemoteRef: "refs/heads/main", DefaultTrackingRef: "refs/remotes/origin/main", DefaultCheckedUnix: 42}
	if err := store.SaveRepoRecord(key, want); err != nil {
		t.Fatal(err)
	}
	if got := store.LoadRepoRecord(key); got != want {
		t.Fatalf("읽어 온 저장소 기록이 다르다: %+v != %+v", got, want)
	}
	// fetch 기록과 다른 자리에 산다. 같은 열쇠로 서로를 덮어쓰지 않아야 한다.
	if got := store.LoadRecord(key); got != (Record{}) {
		t.Fatalf("저장소 기록이 fetch 기록 자리를 덮었다: %+v", got)
	}
}

// fetch 기록, 저장소 기록, 따라잡기 기록은 모든 서버가 나눠 쓰는 뿌리(Root)에 산다. Root 가 비어 있을 때만
// Dir 로 물러난다.
func TestSharedRootIsUsedForEveryRecordKind(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	store := Store{Dir: dir, Root: root}
	key := Key("어떤/저장소")
	if err := store.SaveRecord(key, Record{LastAttemptUnix: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRepoRecord(key, RepoRecord{DefaultCheckedUnix: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCatchupRecord(key, CatchupRecord{Result: "clean"}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"fetch", "repo", "catchup"} {
		if _, err := os.Stat(filepath.Join(root, kind, key+".json")); err != nil {
			t.Fatalf("%s 기록은 Root 아래에 있어야 한다: %v", kind, err)
		}
		if _, err := os.Stat(filepath.Join(dir, kind, key+".json")); err == nil {
			t.Fatalf("%s 기록이 서버 전용 디렉터리에도 쓰였다", kind)
		}
	}

	fallback := Store{Dir: dir}
	if err := fallback.SaveRecord(key, Record{LastAttemptUnix: 2}); err != nil {
		t.Fatal(err)
	}
	if err := fallback.SaveRepoRecord(key, RepoRecord{DefaultCheckedUnix: 2}); err != nil {
		t.Fatal(err)
	}
	if err := fallback.SaveCatchupRecord(key, CatchupRecord{Result: "clean"}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"fetch", "repo", "catchup"} {
		if _, err := os.Stat(filepath.Join(dir, kind, key+".json")); err != nil {
			t.Fatalf("Root 가 비면 %s 기록은 Dir 아래에 있어야 한다: %v", kind, err)
		}
	}
}

func TestLoadRepoRecordToleratesGarbage(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	key := Key("깨진 것")
	path := filepath.Join(store.Dir, "repo", key+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{이건 JSON이 아니다"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := store.LoadRepoRecord(key); got != (RepoRecord{}) {
		t.Fatalf("깨진 파일은 빈 기록으로 읽어야 한다: %+v", got)
	}
}
