// Package state는 재시작을 넘겨 기억해야 하는 것들을 파일로 남긴다.
//
// 두 가지를 다룬다. 하나는 저장소마다의 fetch 기록으로, 스로틀과 실패 표시의 근거가 된다.
// 다른 하나는 데몬 잠금으로, 같은 데몬이 여러 개 뜨는 것을 막는다.
//
// 데몬 잠금에 프로세스 생존 확인을 쓰지 않고 하트비트를 쓴 이유가 있다. 프로세스가 살아 있는지
// 확인하는 방법이 플랫폼마다 다른데(유닉스는 신호 0번, 윈도우는 OpenProcess), 하트비트는 파일에
// 적힌 시각만 보면 되므로 어디서나 같은 코드로 돈다.
//
// 잠금 관련 파일은 herdr 서버마다 따로 둔다. herdr는 세션마다 소켓을 따로 두지만 플러그인 상태
// 디렉터리는 세션과 무관하게 하나이고, 데몬 하나는 자기가 물려받은 소켓 하나만 섬긴다. 잠금을
// 나눠 쓰면 먼저 뜬 데몬이 다른 세션의 데몬까지 막아 그 세션에는 영영 토큰이 오지 않는다.
// 반면 fetch 기록은 저장소로 키를 잡으므로 서버가 달라도 나눠 쓰는 편이 옳다.
package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"herdr-git-upstream/internal/herdrpaths"
)

// Store는 상태 파일들이 사는 자리다.
type Store struct {
	// Dir는 이 herdr 서버 전용 디렉터리다. 데몬 잠금과 중지 표시, 쪽지, 로그가 여기 산다.
	Dir string
	// Root는 모든 서버가 나눠 쓰는 뿌리다. fetch 기록이 여기 산다.
	Root string
}

// Dir는 플러그인 상태의 뿌리를 돌려준다.
func Dir() string {
	return herdrpaths.StateDir()
}

// New는 지금 프로세스가 말을 거는 herdr 서버에 맞춘 Store를 만든다.
func New() Store {
	root := Dir()
	return Store{Dir: filepath.Join(root, "servers", serverKey()), Root: root}
}

// serverKey는 이 프로세스가 어느 herdr 서버를 섬기는지 나타내는 짧은 이름이다.
// 소켓 경로를 그대로 디렉터리 이름에 넣을 수 없으므로 해시의 앞부분을 쓴다.
func serverKey() string {
	socket := os.Getenv("HERDR_SOCKET_PATH")
	if socket == "" {
		return "default"
	}
	sum := sha256.Sum256([]byte(socket))
	return hex.EncodeToString(sum[:6])
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
	root := s.Root
	if root == "" {
		root = s.Dir
	}
	return filepath.Join(root, "fetch", key+".json")
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
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.recordPath(key), raw)
}

// lockRecordSize는 잠금 파일의 고정 길이다.
//
// 하트비트는 이 크기 그대로 제자리에 덮어쓴다. 길이가 변하지 않으므로 읽는 쪽이 반쯤 쓰인 파일을
// 보는 구간이 아예 없다. 이 코드에서 깨진 잠금 파일은 단순한 오류가 아니라 "앞선 데몬이 죽었다"는
// 신호로 읽히기 때문에, 그런 순간이 생기면 멀쩡한 데몬이 쫓겨난다.
const lockRecordSize = 160

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
	raw, err := encodeLock(daemonLock{PID: os.Getpid(), StartedUnix: now, HeartbeatUnix: now})
	if err != nil {
		return err
	}
	_, err = file.Write(raw)
	return err
}

// encodeLock은 잠금 내용을 언제나 같은 길이로 만든다. 뒤를 공백으로 채우며, JSON 해석기는
// 뒤따르는 공백을 무시하므로 읽는 쪽은 아무 준비 없이 그대로 읽으면 된다.
func encodeLock(lock daemonLock) ([]byte, error) {
	raw, err := json.Marshal(lock)
	if err != nil {
		return nil, err
	}
	if len(raw) > lockRecordSize {
		return nil, errors.New("잠금 기록이 정해진 길이를 넘었다")
	}
	padded := make([]byte, lockRecordSize)
	copy(padded, raw)
	for i := len(raw); i < lockRecordSize; i++ {
		padded[i] = ' '
	}
	return padded, nil
}

func (s Store) readLock() (daemonLock, error) {
	raw, err := os.ReadFile(s.lockPath())
	if err != nil {
		return daemonLock{}, err
	}
	var lock daemonLock
	if err := json.Unmarshal(bytes.TrimRight(raw, " \x00\n"), &lock); err != nil {
		return daemonLock{}, err
	}
	return lock, nil
}

// Heartbeat는 데몬이 살아 있음을 알리고, 잠금이 아직 우리 것인지 답한다.
//
// 거짓은 오직 "넘겨받기가 일어나 주인이 바뀌었다"는 뜻이어야 한다. 파일을 잠깐 읽지 못했다거나
// 쓰기가 한 번 실패한 것을 소유권 상실로 읽으면, 멀쩡히 일하던 데몬이 스스로 물러난다.
// 윈도우에서는 다른 프로세스가 잠금 파일을 읽는 동안 쓰기가 막히는 일이 드물지 않으므로
// 이 구분이 특히 중요하다.
//
// 파일을 통째로 갈아치우지 않고 제자리에 덮어쓴다. 갈아치우면 그 사이에 넘겨받기가 끝났을 때
// 뒤늦은 쓰기가 새 주인의 잠금을 옛 주인 것으로 되돌려 놓는다.
func (s Store) Heartbeat() bool {
	file, err := os.OpenFile(s.lockPath(), os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// 누군가 잠금을 치웠다. 우리가 다시 세워 두되, 그 사이 남이 가져갔으면 조용히 실패한다.
			if createErr := s.createLock(); createErr != nil {
				return !errors.Is(createErr, fs.ErrExist)
			}
			return true
		}
		// 일시적인 접근 실패다. 다음 회차에 다시 본다.
		return true
	}
	defer file.Close()

	buffer := make([]byte, lockRecordSize)
	n, _ := file.ReadAt(buffer, 0)
	var lock daemonLock
	if err := json.Unmarshal(bytes.TrimRight(buffer[:n], " \x00\n"), &lock); err != nil {
		return true
	}
	if lock.PID != os.Getpid() {
		return false
	}

	lock.HeartbeatUnix = time.Now().Unix()
	raw, err := encodeLock(lock)
	if err != nil {
		return true
	}
	if _, err := file.WriteAt(raw, 0); err != nil {
		return true
	}
	return true
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
func (s Store) RequestStop() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return WriteFileAtomic(s.stopPath(), []byte(time.Now().UTC().Format(time.RFC3339)))
}

// StopRequested는 멈추라는 요청이 있었는지 보고, 있었으면 그 표시를 지운다.
//
// 확인과 소비가 한 번의 시스템 호출로 끝나야 한다. 확인한 뒤 지우면 그 사이에 들어온 요청을
// 못 본 채 지우게 된다. Remove의 성공 여부 자체를 답으로 쓰면 그 창이 없다.
func (s Store) StopRequested() bool {
	return os.Remove(s.stopPath()) == nil
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

// WriteFileAtomic은 같은 디렉터리에 임시 파일을 쓰고 이름을 바꾼다.
// 데몬과 이벤트 훅이 같은 파일을 동시에 건드릴 수 있으므로, 반쯤 쓰인 파일이 읽히지 않게 한다.
//
// 윈도우에서는 다른 프로세스가 대상 파일을 열어 둔 동안 이름 바꾸기가 막힌다. Go는 파일을 열 때
// 삭제 공유를 허용하지 않기 때문에, 이 플러그인처럼 여러 프로세스가 같은 파일을 자주 읽는
// 구조에서는 드물지 않게 부딪힌다. 잠깐 뒤 다시 해 보면 대개 풀리므로 몇 번 다시 시도한다.
func WriteFileAtomic(path string, data []byte) error {
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

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if err := os.Rename(tempName, path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if !errors.Is(lastErr, fs.ErrPermission) {
			return lastErr
		}
		time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
	}
	return lastErr
}
