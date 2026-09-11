package state

import (
	"os"
	"testing"
	"time"
)

func TestPopupSuppressionIsScopedAndDurable(t *testing.T) {
	s := Store{Root: t.TempDir()}
	if err := s.SuppressFreshening("repo", "feature"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		repo, branch string
		want         bool
	}{
		{"repo", "feature", true},
		{"other", "feature", false},
		{"repo", "other", false},
	} {
		got, err := s.FresheningSuppressed(tc.repo, tc.branch)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %v %v", tc, got, err)
		}
	}
	// 느린 서버 생성이나 다른 세션의 지연 이벤트도 시간 경과만으로 보호를 잃지 않는다.
	past := time.Unix(1, 0)
	if err := os.Chtimes(s.popupPath("repo", "feature"), past, past); err != nil {
		t.Fatal(err)
	}
	otherSession := Store{Root: s.Root, Dir: t.TempDir()}
	if got, err := otherSession.FresheningSuppressed("repo", "feature"); err != nil || !got {
		t.Fatalf("지연 이벤트의 공유 보호: %v, %v", got, err)
	}
	if err := os.WriteFile(s.popupPath("repo", "feature"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FresheningSuppressed("repo", "feature"); err == nil {
		t.Fatal("손상된 보호 기록은 오류여야 한다")
	}
}

func TestCurrentCreationOnlyClearsCompletedSuppression(t *testing.T) {
	s := Store{Root: t.TempDir()}
	if err := s.AllowFreshening("repo", "new"); err != nil {
		t.Fatal(err)
	}
	if err := s.SuppressFreshening("repo", "feature"); err != nil {
		t.Fatal(err)
	}
	if err := s.AllowFreshening("repo", "feature"); err == nil {
		t.Fatal("완료되지 않은 생성의 보호를 해제하면 안 된다")
	}
	if err := s.CompleteFresheningSuppression("repo", "feature"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.FresheningSuppressed("repo", "feature"); err != nil || !got {
		t.Fatalf("완료 뒤에도 이벤트 보호: %v %v", got, err)
	}
	if err := s.AllowFreshening("repo", "feature"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.FresheningSuppressed("repo", "feature"); err != nil || got {
		t.Fatalf("새 Current 생성에는 기존 보호 해제: %v %v", got, err)
	}
	// 늦은 응답의 완료 기록은 이미 해제한 보호를 다시 만들지 않는다.
	if err := s.CompleteFresheningSuppression("repo", "feature"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.FresheningSuppressed("repo", "feature"); err != nil || got {
		t.Fatalf("해제 뒤 지연 응답: %v %v", got, err)
	}
}

// 한 전이가 complete를 읽은 동안 다른 프로세스의 pending 기록은 끝날 수 없다.
func TestPopupTransitionsAreSerialized(t *testing.T) {
	s := Store{Root: t.TempDir()}
	if err := s.SuppressFreshening("repo", "feature"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteFresheningSuppression("repo", "feature"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	err := s.withPopupLock("repo", "feature", func() error {
		phase, err := s.popupPhase("repo", "feature")
		if err != nil || phase != "complete" {
			t.Fatalf("완료 기록: %s %v", phase, err)
		}
		go func() { close(started); finished <- s.SuppressFreshening("repo", "feature") }()
		<-started
		select {
		case err := <-finished:
			t.Fatalf("진행 중인 상태 전이에 끼어들면 안 된다: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		return os.Remove(s.popupPath("repo", "feature"))
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("전이 잠금이 해제되지 않음")
	}
	if phase, err := s.popupPhase("repo", "feature"); err != nil || phase != "pending" {
		t.Fatalf("뒤따른 새 생성의 pending 보존: %s %v", phase, err)
	}
}
