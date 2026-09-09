// Package state는 재시작을 넘겨 기억해야 하는 것들을 파일로 남긴다.
//
// 두 가지를 다룬다. 하나는 저장소마다의 fetch 기록으로, 스로틀과 실패 표시의 근거가 된다.
// 다른 하나는 데몬 잠금으로, 같은 데몬이 여러 개 뜨는 것을 막는다.
//
// 데몬 잠금에 프로세스 생존 확인을 쓰지 않고 하트비트를 쓴 이유가 있다. 프로세스가 살아 있는지
// 확인하는 방법이 플랫폼마다 다른데(유닉스는 신호 0번, 윈도우는 OpenProcess), 하트비트는 파일
// 수정 시각만 보면 되므로 어디서나 같은 코드로 돈다.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store는 상태 파일들이 사는 디렉터리다.
type Store struct {
	Dir string
}

// Dir는 상태 디렉터리를 정한다. herdr가 플러그인마다 하나씩 마련해 HERDR_PLUGIN_STATE_DIR로 알려 준다.
// 플러그인 밖에서 직접 실행하는 경우를 위해 사용자 캐시 디렉터리를 대안으로 둔다.
func Dir() string {
	if dir := os.Getenv("HERDR_PLUGIN_STATE_DIR"); dir != "" {
		return dir
	}
	if cache, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cache, "herdr-pull-status")
	}
	return filepath.Join(os.TempDir(), "herdr-pull-status")
}

// New는 기본 위치를 쓰는 Store를 만든다.
func New() Store {
	return Store{Dir: Dir()}
}

// Record는 저장소 하나에 대한 fetch 기록이다.
type Record struct {
	LastAttemptUnix int64  `json:"last_attempt_unix"`
	LastSuccessUnix int64  `json:"last_success_unix"`
	LastError       string `json:"last_error,omitempty"`
	// LastErrorPermanent는 다시 시도해도 똑같이 실패할 오류를 뜻한다. 원격에서 브랜치가 지워진
	// 경우가 대표적이다. 이런 것은 사용자가 손쓸 여지가 없으므로 사이드바에 경고로 띄우지 않는다.
	LastErrorPermanent bool `json:"last_error_permanent,omitempty"`
	// FailingSinceUnix는 지금 이어지고 있는 실패가 처음 난 시각이다. 성공하면 0으로 돌아간다.
	// 마지막 성공 시각이 아니라 이 값을 기준으로 삼아야, 한 번도 성공한 적 없는 저장소가
	// 첫 실패만으로 곧장 경고를 띄우는 일이 없다.
	FailingSinceUnix int64 `json:"failing_since_unix,omitempty"`
}

// DueAt은 스로틀을 고려해 지금 fetch해도 되는지 답한다.
func (r Record) DueAt(now time.Time, throttle time.Duration) bool {
	if r.LastAttemptUnix == 0 {
		return true
	}
	return now.Sub(time.Unix(r.LastAttemptUnix, 0)) >= throttle
}

// MarkSuccess는 성공을 기록하고 실패 흔적을 지운다.
func (r Record) MarkSuccess(now time.Time) Record {
	r.LastSuccessUnix = now.Unix()
	r.LastError = ""
	r.LastErrorPermanent = false
	r.FailingSinceUnix = 0
	return r
}

// MarkFailure는 실패를 기록한다. 이미 실패가 이어지고 있었다면 시작 시각은 그대로 둔다.
func (r Record) MarkFailure(now time.Time, message string, permanent bool) Record {
	r.LastError = message
	r.LastErrorPermanent = permanent
	if r.FailingSinceUnix == 0 {
		r.FailingSinceUnix = now.Unix()
	}
	return r
}

// Stale은 원격 정보를 더는 믿을 수 없는 상태인지 답한다.
//
// 실패가 after보다 오래 이어졌을 때만 참이다. 잠시 끊긴 네트워크나 아직 연결하지 않은 VPN 때문에
// 사이드바가 곧바로 경고를 띄우면, 정작 사람이 손봐야 하는 상황과 구별되지 않는다.
func (r Record) Stale(now time.Time, after time.Duration) bool {
	if r.LastError == "" || r.LastErrorPermanent || r.FailingSinceUnix == 0 {
		return false
	}
	return now.Sub(time.Unix(r.FailingSinceUnix, 0)) >= after
}

// Key는 저장소 식별자를 파일 이름으로 쓸 수 있는 형태로 바꾼다.
// 경로를 그대로 파일 이름에 넣을 수 없고(구분자와 대소문자 문제), 길이 제한도 있어 해시를 쓴다.
func Key(identifier string) string {
	sum := sha256.Sum256([]byte(identifier))
	return hex.EncodeToString(sum[:])
}

func (s Store) recordPath(key string) string {
	return filepath.Join(s.Dir, "fetch", key+".json")
}

// LoadRecord는 기록을 읽는다. 파일이 없거나 깨져 있으면 빈 기록을 돌려준다.
// 깨진 파일을 오류로 만들지 않는 이유는, 그 경우 "한 번도 시도하지 않음"으로 보고 다시 시도하는 것이
// 사용자 입장에서 옳은 동작이기 때문이다.
func (s Store) LoadRecord(key string) Record {
	raw, err := os.ReadFile(s.recordPath(key))
	if err != nil {
		return Record{}
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}
	}
	return record
}

// SaveRecord는 기록을 남긴다.
func (s Store) SaveRecord(key string, record Record) error {
	path := s.recordPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw)
}

// daemonLock은 데몬 잠금 파일의 내용이다.
type daemonLock struct {
	PID           int   `json:"pid"`
	StartedUnix   int64 `json:"started_unix"`
	HeartbeatUnix int64 `json:"heartbeat_unix"`
}

func (s Store) lockPath() string {
	return filepath.Join(s.Dir, "daemon.lock")
}

// ErrDaemonRunning은 이미 살아 있는 데몬이 있어 잠금을 얻지 못했을 때 나온다.
var ErrDaemonRunning = errors.New("데몬이 이미 돌고 있다")

// AcquireDaemonLock은 데몬 잠금을 얻는다.
//
// 잠금 파일을 O_EXCL로 만들어 경쟁을 한 번에 정리한다. 파일이 이미 있으면 하트비트를 본다.
// 하트비트가 staleAfter보다 오래되었으면 앞선 데몬이 죽은 것으로 보고 파일을 치운 뒤 한 번 더
// 만들어 본다. 이 재시도도 O_EXCL이므로, 동시에 여러 프로세스가 넘겨받으려 해도 하나만 이긴다.
func (s Store) AcquireDaemonLock(staleAfter time.Duration) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if err := s.createLock(); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrExist) {
		return err
	}

	existing, err := s.readLock()
	// 읽지 못하거나 깨져 있으면 남겨진 잔재로 본다.
	if err == nil && time.Since(time.Unix(existing.HeartbeatUnix, 0)) < staleAfter {
		return ErrDaemonRunning
	}
	if err := os.Remove(s.lockPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := s.createLock(); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// 치우자마자 다른 프로세스가 먼저 가져갔다. 그쪽에 맡기고 물러난다.
			return ErrDaemonRunning
		}
		return err
	}
	return nil
}

func (s Store) createLock() error {
	file, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	now := time.Now().Unix()
	raw, err := json.Marshal(daemonLock{PID: os.Getpid(), StartedUnix: now, HeartbeatUnix: now})
	if err != nil {
		return err
	}
	_, err = file.Write(raw)
	return err
}

func (s Store) readLock() (daemonLock, error) {
	raw, err := os.ReadFile(s.lockPath())
	if err != nil {
		return daemonLock{}, err
	}
	var lock daemonLock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return daemonLock{}, err
	}
	return lock, nil
}

// Heartbeat는 데몬이 살아 있음을 알린다. 잠금이 우리 것이 아니게 되었으면 false를 돌려주며,
// 그때 데몬은 스스로 물러나야 한다. 넘겨받기가 잘못 일어났을 때 데몬이 둘 도는 것을 막는 마지막 방어선이다.
func (s Store) Heartbeat() bool {
	lock, err := s.readLock()
	if err != nil {
		return false
	}
	if lock.PID != os.Getpid() {
		return false
	}
	lock.HeartbeatUnix = time.Now().Unix()
	raw, err := json.Marshal(lock)
	if err != nil {
		return false
	}
	return writeFileAtomic(s.lockPath(), raw) == nil
}

// ReleaseDaemonLock은 잠금이 우리 것일 때만 치운다.
func (s Store) ReleaseDaemonLock() {
	if lock, err := s.readLock(); err == nil && lock.PID == os.Getpid() {
		_ = os.Remove(s.lockPath())
	}
}

// DaemonAlive는 살아 있는 데몬이 있는지 답한다. 포커스 이벤트가 데몬을 되살릴지 판단할 때 쓴다.
func (s Store) DaemonAlive(staleAfter time.Duration) bool {
	lock, err := s.readLock()
	if err != nil {
		return false
	}
	return time.Since(time.Unix(lock.HeartbeatUnix, 0)) < staleAfter
}

// RequestStop은 데몬에게 멈추라고 알린다. 신호를 쓰지 않고 파일을 쓰는 이유는 플랫폼 차이 때문이다.
// 데몬은 매 회차마다 이 파일을 확인한다.
func (s Store) RequestStop() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(s.stopPath(), []byte(time.Now().UTC().Format(time.RFC3339)))
}

// StopRequested는 멈추라는 요청이 있었는지 보고, 있었으면 그 표시를 지운다.
func (s Store) StopRequested() bool {
	if _, err := os.Stat(s.stopPath()); err != nil {
		return false
	}
	_ = os.Remove(s.stopPath())
	return true
}

// ClearStop은 남아 있는 중지 요청을 지운다. 데몬이 시작할 때 호출해, 지난 요청 때문에
// 방금 띄운 데몬이 곧바로 꺼지는 일을 막는다.
func (s Store) ClearStop() {
	_ = os.Remove(s.stopPath())
}

func (s Store) stopPath() string {
	return filepath.Join(s.Dir, "daemon.stop")
}

// LogPath는 데몬 로그 파일의 경로다.
func (s Store) LogPath() string {
	return filepath.Join(s.Dir, "daemon.log")
}

// writeFileAtomic은 같은 디렉터리에 임시 파일을 쓰고 이름을 바꾼다.
// 데몬과 이벤트 훅이 같은 파일을 동시에 건드릴 수 있으므로, 반쯤 쓰인 파일이 읽히지 않게 한다.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() {
		// 이름 바꾸기가 성공하면 이 경로는 이미 사라졌으므로 아래 Remove는 아무 일도 하지 않는다.
		_ = os.Remove(tempName)
	}()
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// 윈도우에서는 대상 파일이 있으면 Rename이 실패할 수 있어, 먼저 치우고 옮긴다.
	if err := os.Rename(tempName, path); err != nil {
		if !strings.Contains(err.Error(), "exist") {
			return err
		}
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return err
		}
		return os.Rename(tempName, path)
	}
	return nil
}
