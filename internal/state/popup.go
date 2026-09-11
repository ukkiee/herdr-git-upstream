package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// 팝업 생성과 worktree.created 훅은 서로 다른 프로세스에서 돈다. 생성 응답 전에 훅이 시작될 수 있어
// API 호출 전에 의도를 원자적으로 기록한다. 같은 저장소를 여는 모든 herdr 세션이 이 기록을 공유한다.
func (s Store) popupPath(commonDir, branch string) string {
	return filepath.Join(s.sharedRoot(), "popup-creations", Key(commonDir+"\x00"+branch)+".json")
}

func (s Store) SuppressFreshening(commonDir, branch string) error {
	if commonDir == "" || branch == "" {
		return errors.New("생성 보호 기록에 저장소와 브랜치가 필요하다")
	}
	return s.withPopupLock(commonDir, branch, func() error {
		phase, err := s.popupPhase(commonDir, branch)
		if err != nil {
			return err
		}
		if phase == "pending" {
			return errors.New("a previous creation of this branch has not completed; choose another name")
		}
		return WriteFileAtomic(s.popupPath(commonDir, branch), []byte(`"pending"`))
	})
}

// FresheningSuppressed는 원격 기준 또는 기존 브랜치를 선택한 팝업 생성인지 확인한다.
// 손상되었거나 읽지 못한 보호 기록은 오류다. 모르는 상태에서 작업 디렉터리를 옮기지 않는다.
func (s Store) FresheningSuppressed(commonDir, branch string) (bool, error) {
	phase, err := s.popupPhase(commonDir, branch)
	return phase != "", err
}

func (s Store) popupPhase(commonDir, branch string) (string, error) {
	raw, err := os.ReadFile(s.popupPath(commonDir, branch))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var phase string
	if err := json.Unmarshal(raw, &phase); err != nil || (phase != "pending" && phase != "complete") {
		return "", errors.New("생성 보호 기록을 읽지 못했다")
	}
	return phase, nil
}

// CompleteFresheningSuppression은 실제 생성 응답 또는 생성 이벤트를 관측한 뒤 부른다.
// 기록은 남겨 중복 이벤트도 보호하되, 이후 명시적인 Current 생성에서는 이전 보호를 해제할 수 있다.
func (s Store) CompleteFresheningSuppression(commonDir, branch string) error {
	return s.withPopupLock(commonDir, branch, func() error {
		phase, err := s.popupPhase(commonDir, branch)
		if err != nil || phase != "pending" {
			return err
		}
		return WriteFileAtomic(s.popupPath(commonDir, branch), []byte(`"complete"`))
	})
}

// AllowFreshening은 같은 이름을 새 Current 브랜치로 다시 쓸 때 지난 완료 기록만 치운다.
// 미완료 생성은 CLI 시간 제한 후에도 서버에서 돌고 있을 수 있어 시간만으로 해제하지 않는다.
func (s Store) AllowFreshening(commonDir, branch string) error {
	return s.withPopupLock(commonDir, branch, func() error {
		phase, err := s.popupPhase(commonDir, branch)
		if err != nil || phase == "" {
			return err
		}
		if phase == "pending" {
			return errors.New("a previous creation of this branch has not completed; choose another name")
		}
		err = os.Remove(s.popupPath(commonDir, branch))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	})
}

// withPopupLock은 파일을 읽고 바꾸는 짧은 상태 전이를 프로세스 사이에서도 직렬화한다.
// complete를 읽은 뒤 다른 팝업이 쓴 pending을 지우면 선택 기준의 보호가 사라진다.
// 잠금이 남았다고 시간만으로 빼앗지 않는다. 실패하면 생성/최신화를 하지 않는 쪽으로 반환한다.
func (s Store) withPopupLock(commonDir, branch string, fn func() error) (err error) {
	path := s.popupPath(commonDir, branch) + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		lock, openErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr == nil {
			defer func() { err = errors.Join(err, lock.Close(), os.Remove(path)) }()
			return fn()
		}
		if !errors.Is(openErr, fs.ErrExist) || time.Now().After(deadline) {
			return openErr
		}
		time.Sleep(10 * time.Millisecond)
	}
}
