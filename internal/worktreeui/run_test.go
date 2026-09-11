package worktreeui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/herdrcli"
	"herdr-git-upstream/internal/judge"
	"herdr-git-upstream/internal/tui"
)

// 컨트롤러는 터미널 없이 loop 를 직접 돌려 시험한다. 키는 채널로 넣고, 그려진 모델은 afterDraw 로 받아 본다.
// herdr 는 가짜(fakeHerdr)라 실행 중인 herdr 에는 아무것도 보내지 않는다.

type harness struct {
	cancel context.CancelFunc
	keys   chan tui.Key
	models chan Model
	done   chan error
	out    *syncBuffer
}

type waitingRemovalHerdr struct {
	*fakeHerdr
	entered chan context.Context
	release chan struct{}
}

func (h *waitingRemovalHerdr) WorktreeRemove(ctx context.Context, id string) error {
	h.entered <- ctx
	select {
	case <-h.release:
		return h.fakeHerdr.WorktreeRemove(ctx, id)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestLoopWaitsForRemovalAfterRepeatedQuit(t *testing.T) {
	f := newFixture(t)
	path := f.safeWorktree(t, "done")
	f.fetch(t)
	herdr := &waitingRemovalHerdr{fakeHerdr: newFakeHerdr(f), entered: make(chan context.Context, 1), release: make(chan struct{})}
	herdr.open(path, "wDONE")
	_, h := start(t, f, herdr, f.work)
	h.waitFor(t, "safe 행", func(m Model) bool { return len(m.SafeRows()) == 1 })
	h.press(t, key('d'))
	var removalCtx context.Context
	select {
	case removalCtx = <-herdr.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("삭제가 시작되지 않았다")
	}
	h.press(t, key('q'))
	h.press(t, key('q'))
	select {
	case err := <-h.done:
		close(herdr.release)
		t.Fatalf("삭제가 끝나기 전에 화면을 닫으면 안 된다: %v", err)
	case <-removalCtx.Done():
		close(herdr.release)
		t.Fatal("진행 중인 삭제를 취소하면 안 된다")
	case <-time.After(100 * time.Millisecond):
	}
	close(herdr.release)
	if err := h.finished(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("삭제 완료 뒤 닫혀야 한다: %v", err)
	}
}

// syncBuffer는 loop 고루틴이 쓰고 시험이 읽는 출력이다.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func start(t *testing.T, f *fixture, herdr Herdr, cwd string) (*controller, *harness) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c, err := newController(ctx, Options{CWD: cwd, Herdr: herdr, Git: f.git, Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{cancel: cancel, keys: make(chan tui.Key), models: make(chan Model, 256), done: make(chan error, 1), out: &syncBuffer{}}
	c.afterDraw = func(m Model) {
		select {
		case h.models <- m:
		default:
		}
	}
	go func() {
		h.done <- c.loop(ctx, h.keys, h.out, func() (int, int) { return 100, 20 }, func(fn func()) { go fn() })
	}()
	return c, h
}

// waitFor는 조건에 맞는 모델이 그려질 때까지 기다린다.
func (h *harness) waitFor(t *testing.T, what string, ok func(Model) bool) Model {
	t.Helper()
	deadline := time.After(60 * time.Second)
	var last Model
	for {
		select {
		case m := <-h.models:
			last = m
			if ok(m) {
				return m
			}
		case err := <-h.done:
			t.Fatalf("%s 를 기다리는 동안 loop 가 끝났다: %v\n마지막 모델: %+v", what, err, last)
		case <-deadline:
			t.Fatalf("%s 를 기다리다 시간이 다 됐다\n마지막 모델: %+v", what, last)
		}
	}
}

func (h *harness) press(t *testing.T, k tui.Key) {
	t.Helper()
	select {
	case h.keys <- k:
	case <-time.After(10 * time.Second):
		t.Fatal("키를 받지 않는다")
	}
}

func (h *harness) finished(t *testing.T) error {
	t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("loop 가 끝나지 않는다")
		return nil
	}
}

func hasRows(m Model) bool { return len(m.Rows) > 0 && !m.Assessing }

func hasLabel(label string) func(Model) bool {
	return func(m Model) bool { _, ok := byLabel(m.Rows)[label]; return ok && !m.Assessing }
}

func lacksLabel(label string) func(Model) bool {
	return func(m Model) bool { _, ok := byLabel(m.Rows)[label]; return hasRows(m) && !ok }
}

// 첫 표는 로컬 자료로 그려지고, 그 뒤 fetch 가 끝나면 ls-remote 결과로 gone 이 판정된다. d 는 herdr 에 열려 있는
// safe worktree 를 herdr 에게 지우게 하고, 지운 뒤에는 표에서 사라진다. 에이전트가 일하는 것은 blocked 다.
func TestLoopRemovesSelectedThroughHerdr(t *testing.T) {
	f := newFixture(t)
	done := f.safeWorktree(t, "done")
	dirty := f.dirtyWorktree(t, "dirty")
	gone := f.addWorktree(t, "gone")
	f.commitIn(t, gone, "gone.txt", "1")
	f.pushed(t, gone, "gone")
	run(t, f.seed, "git", "push", "--quiet", "origin", "--delete", "gone")
	herdr := newFakeHerdr(f)
	herdr.open(done, "wDONE")
	herdr.open(dirty, "wDIRTY")
	herdr.open(f.work, "wMAIN")
	herdr.working["wDIRTY"] = true

	_, h := start(t, f, herdr, f.work)
	// 로컬 자료: done 은 아직 fetch 전이라 origin/main 이 낡아 merged 가 아니다. gone 은 모른다.
	m := h.waitFor(t, "첫 표", hasRows)
	if m.HerdrUnavailable || m.RepoName != "fake" {
		t.Fatalf("herdr 의 목록이어야 한다: %+v", m)
	}
	expectRow(t, m.Rows, "dirty", "blocked", "agent working")
	// fetch 뒤: done 은 merged, gone 은 gone. 제목 줄은 fetched.
	m = h.waitFor(t, "fetch 뒤의 표", func(m Model) bool {
		return m.Fetch == FetchDone && byLabel(m.Rows)["gone"].Verdict == judge.Safe && byLabel(m.Rows)["done"].Verdict == judge.Safe
	})
	expectRow(t, m.Rows, "done", "safe", "merged")
	expectRow(t, m.Rows, "gone", "safe", "gone")
	expectRow(t, m.Rows, "main", "blocked", "main checkout")
	selected, ok := m.Selected()
	if !ok || canonical(selected.Path) != canonical(dirty) || m.Rows[0].Branch != "done" || m.FetchedAgo == "" {
		t.Fatalf("초기 선택과 safe 우선 정렬을 유지하고 fetch 시각이 있어야 한다: %+v", m)
	}
	if !strings.Contains(h.out.String(), "\x1b[1;1H") {
		t.Fatal("화면은 Frame 으로 그려야 한다")
	}

	for range m.Cursor {
		h.press(t, key('k'))
	}
	h.waitFor(t, "done 선택", func(m Model) bool { row, ok := m.Selected(); return ok && canonical(row.Path) == canonical(done) })
	h.press(t, key('d'))
	m = h.waitFor(t, "done 이 사라진 표", lacksLabel("done"))
	if _, err := os.Stat(done); !os.IsNotExist(err) {
		t.Fatalf("worktree 디렉터리가 사라져야 한다: %v", err)
	}
	if got := herdr.recorded(); !reflect.DeepEqual(got, []string{"remove wDONE"}) {
		t.Fatalf("herdr 에 열린 것은 herdr 에게 지우게 해야 한다: %v", got)
	}
	if !strings.HasPrefix(m.Message, "removed 1 worktree") {
		t.Fatalf("지운 결과가 안내에 보여야 한다: %q", m.Message)
	}
	// 브랜치는 남는다(ADR 0002).
	if _, err := f.git.CommitOf(context.Background(), f.main, "refs/heads/done"); err != nil {
		t.Fatalf("worktree 를 지워도 브랜치는 남아야 한다: %v", err)
	}

	h.press(t, key('q'))
	if err := h.finished(t); err != nil {
		t.Fatalf("q 는 오류 없이 닫아야 한다: %v", err)
	}
}

// d 는 safe 가 아닌 행에서는 아무것도 지우지 않고 이유만 보인다.
func TestLoopRefusesToRemoveUnsafeRow(t *testing.T) {
	f := newFixture(t)
	dirty := f.dirtyWorktree(t, "dirty")
	herdr := newFakeHerdr(f)
	herdr.open(dirty, "wDIRTY")

	_, h := start(t, f, herdr, f.work)
	h.waitFor(t, "첫 표", hasLabel("dirty"))
	h.press(t, key('d'))
	m := h.waitFor(t, "거절 안내", func(m Model) bool { return strings.Contains(m.Message, "only safe worktrees") })
	if !strings.HasPrefix(m.Message, "dirty is review (dirty)") {
		t.Fatalf("이유가 보여야 한다: %q", m.Message)
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatalf("지우면 안 된다: %v", err)
	}
	if got := herdr.recorded(); len(got) != 0 {
		t.Fatalf("herdr 에 아무것도 시키지 않아야 한다: %v", got)
	}
	h.press(t, tui.Key{Kind: tui.KeyEsc})
	if err := h.finished(t); err != nil {
		t.Fatal(err)
	}
}

// D 는 한 번 묻고 safe 전부를 지운다. herdr 에 열린 것은 herdr 에게, 디스크에만 있는 것은 git 에게 시킨다.
// n 으로 거절하면 아무것도 지우지 않는다.
func TestLoopRemovesAllSafeAfterConfirm(t *testing.T) {
	f := newFixture(t)
	a := f.safeWorktree(t, "a-done")
	b := f.safeWorktree(t, "b-done")
	f.dirtyWorktree(t, "dirty")
	f.fetch(t)
	herdr := newFakeHerdr(f)
	herdr.open(a, "wA")

	_, h := start(t, f, herdr, f.work)
	h.waitFor(t, "safe 둘", func(m Model) bool { return len(m.SafeRows()) == 2 })

	h.press(t, key('D'))
	m := h.waitFor(t, "물음", func(m Model) bool { return m.Confirm != nil })
	if m.Confirm.Count != 2 {
		t.Fatalf("safe 수를 물어야 한다: %+v", m.Confirm)
	}
	h.press(t, key('n'))
	m = h.waitFor(t, "물음이 내려감", func(m Model) bool { return m.Confirm == nil })
	if len(m.SafeRows()) != 2 || len(herdr.recorded()) != 0 {
		t.Fatalf("거절하면 아무것도 지우지 않는다: %+v %v", m.Rows, herdr.recorded())
	}

	h.press(t, key('D'))
	h.waitFor(t, "물음", func(m Model) bool { return m.Confirm != nil })
	h.press(t, key('y'))
	m = h.waitFor(t, "safe 가 사라진 표", func(m Model) bool { return hasRows(m) && len(m.SafeRows()) == 0 })
	for _, path := range []string{a, b} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s 가 사라져야 한다: %v", path, err)
		}
	}
	if got := herdr.recorded(); !reflect.DeepEqual(got, []string{"remove wA"}) {
		t.Fatalf("herdr 에 열린 것만 herdr 에게: %v", got)
	}
	if !strings.HasPrefix(m.Message, "removed 2 worktrees") {
		t.Fatalf("결과 안내: %q", m.Message)
	}
	expectRow(t, m.Rows, "dirty", "review", "dirty")

	h.press(t, tui.Key{Kind: tui.KeyCtrlC})
	if err := h.finished(t); err != nil {
		t.Fatal(err)
	}
}

// 삭제가 하나 막혀도 나머지는 지우고, 막힌 이유가 안내에 보인다.
func TestLoopReportsPartialRemoveFailure(t *testing.T) {
	f := newFixture(t)
	a := f.safeWorktree(t, "a-done")
	b := f.safeWorktree(t, "b-done")
	f.fetch(t)
	herdr := newFakeHerdr(f)

	_, h := start(t, f, herdr, f.work)
	h.waitFor(t, "safe 둘", func(m Model) bool { return len(m.SafeRows()) == 2 })
	// 판정과 삭제 사이에 사람이 파일을 만들었다. git 이 마지막 문턱에서 거절해야 한다.
	writeFile(t, filepath.Join(a, "late.txt"), "late")

	h.press(t, key('D'))
	h.waitFor(t, "물음", func(m Model) bool { return m.Confirm != nil })
	h.press(t, key('y'))
	// 결과 안내는 지우자마자 보이고, 표는 다시 모은 뒤에 바뀐다. 둘 다 갖춘 그림을 기다린다.
	m := h.waitFor(t, "결과", func(m Model) bool {
		return strings.HasPrefix(m.Message, "removed 1 worktree, 1 failed: a-done") && byLabel(m.Rows)["a-done"].Verdict == judge.Review
	})
	if _, err := os.Stat(a); err != nil {
		t.Fatalf("손댄 것은 남아야 한다: %v", err)
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Fatalf("나머지는 지워야 한다: %v", err)
	}
	expectRow(t, m.Rows, "a-done", "review", "merged, dirty")

	h.press(t, key('q'))
	if err := h.finished(t); err != nil {
		t.Fatal(err)
	}
}

// Enter 는 herdr 에 열려 있으면 그 워크스페이스로, 아니면 herdr 에게 열게 하고 화면을 닫는다.
// 본 체크아웃 행은 source_workspace_id 로 간다.
func TestLoopOpens(t *testing.T) {
	cases := []struct {
		name   string
		opened bool
		cursor int
		want   string
	}{
		{"열려 있으면 focus", true, 0, "focus wDONE"},
		{"디스크에만 있으면 open", false, 0, "open done"},
		{"본 체크아웃은 source 의 워크스페이스로 focus", true, 1, "focus wMAIN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			done := f.safeWorktree(t, "done")
			f.fetch(t)
			herdr := newFakeHerdr(f)
			herdr.open(f.work, "wMAIN")
			if tc.opened {
				herdr.open(done, "wDONE")
			}
			// 연결된 worktree에서 열어도 source는 본 체크아웃이어야 한다.
			_, h := start(t, f, herdr, done)
			h.waitFor(t, "표", func(m Model) bool { return len(m.Rows) == 2 })
			for i := 0; i < tc.cursor; i++ {
				h.press(t, tui.Key{Kind: tui.KeyDown})
			}
			h.press(t, tui.Key{Kind: tui.KeyEnter})
			if err := h.finished(t); err != nil {
				t.Fatalf("열고 나면 오류 없이 닫혀야 한다: %v", err)
			}
			if got := herdr.recorded(); !reflect.DeepEqual(got, []string{tc.want}) {
				t.Fatalf("%v, 기대값 %v", got, []string{tc.want})
			}
		})
	}
}

// herdr 에 닿지 않으면 git 만으로 표를 그리고 제목 줄에 그렇다고 보인다. Enter 는 갈 곳이 없어 안내만 하고,
// 삭제는 git 으로 한다.
func TestLoopWithoutHerdr(t *testing.T) {
	f := newFixture(t)
	done := f.safeWorktree(t, "done")
	f.fetch(t)
	herdr := newFakeHerdr(f)
	herdr.listErr = errors.New("herdr: no server")

	_, h := start(t, f, herdr, done)
	m := h.waitFor(t, "표", hasLabel("done"))
	if !m.HerdrUnavailable || m.RepoName != "work" {
		t.Fatalf("git 만으로 만든 표여야 한다: %+v", m)
	}
	if title := Render(m, 100, 5)[0]; !strings.Contains(title, tui.Dim("herdr unavailable")) {
		t.Fatalf("제목 줄에 herdr unavailable 이 흐리게 보여야 한다: %q", title)
	}
	h.press(t, tui.Key{Kind: tui.KeyEnter})
	h.waitFor(t, "안내", func(m Model) bool { return strings.Contains(m.Message, "herdr unavailable") })

	// 화면을 지우려는 worktree 의 경로로 열었어도 목록은 본 체크아웃에서 다시 묻는다. 지운 뒤에도 표가 남아야 한다.
	// (시험 프로세스의 cwd 는 그 안이 아니라 git 이 거절하지 않는다.)
	h.press(t, key('d'))
	m = h.waitFor(t, "done 이 사라진 표", lacksLabel("done"))
	if _, err := os.Stat(done); !os.IsNotExist(err) {
		t.Fatalf("git 으로 지워야 한다: %v", err)
	}
	if got := herdr.recorded(); len(got) != 0 {
		t.Fatalf("herdr 에 닿지 않는데 시키면 안 된다: %v", got)
	}
	h.press(t, key('q'))
	if err := h.finished(t); err != nil {
		t.Fatal(err)
	}
}

// r 은 다시 fetch 한다. 그 사이 원격에서 지워진 브랜치가 gone 이 된다.
func TestLoopRefreshFetchesAgain(t *testing.T) {
	f := newFixture(t)
	wip := f.addWorktree(t, "wip")
	f.commitIn(t, wip, "wip.txt", "1")
	f.pushed(t, wip, "wip")
	herdr := newFakeHerdr(f)

	_, h := start(t, f, herdr, f.work)
	m := h.waitFor(t, "첫 fetch", func(m Model) bool { return m.Fetch == FetchDone && hasLabel("wip")(m) })
	expectRow(t, m.Rows, "wip", "keep", "up to date")

	run(t, f.seed, "git", "push", "--quiet", "origin", "--delete", "wip")
	h.press(t, key('r'))
	h.waitFor(t, "fetch 중", func(m Model) bool { return m.Fetch == FetchFetching })
	m = h.waitFor(t, "gone", func(m Model) bool { return m.Fetch == FetchDone && byLabel(m.Rows)["wip"].Verdict == judge.Safe })
	expectRow(t, m.Rows, "wip", "safe", "gone")

	h.press(t, key('q'))
	if err := h.finished(t); err != nil {
		t.Fatal(err)
	}
}

// 원격에 닿지 않으면 제목 줄이 fetch failed 이고 표는 로컬 자료 그대로다.
func TestLoopShowsFetchFailure(t *testing.T) {
	f := newFixture(t)
	f.addWorktree(t, "wip")
	run(t, f.work, "git", "remote", "set-url", "origin", filepath.Join(f.base, "no-such-remote.git"))
	herdr := newFakeHerdr(f)

	_, h := start(t, f, herdr, f.work)
	m := h.waitFor(t, "fetch 실패", func(m Model) bool { return m.Fetch == FetchFailed })
	if !hasLabel("wip")(m) {
		t.Fatalf("fetch 가 실패해도 표는 있어야 한다: %+v", m.Rows)
	}
	if title := Render(m, 100, 5)[0]; !strings.Contains(plain(title), "fetch failed") {
		t.Fatalf("제목 줄: %q", title)
	}
	h.press(t, key('q'))
	if err := h.finished(t); err != nil {
		t.Fatal(err)
	}
}

// 저장소가 아닌 자리에서는 터미널을 열기 전에 오류다. 명령이 그 문구를 그대로 stderr 에 적는다.
func TestNewControllerRejectsNonRepository(t *testing.T) {
	f := newFixture(t)
	herdr := newFakeHerdr(f)
	herdr.listErr = errors.New("herdr: no server")
	empty := t.TempDir()
	_, err := newController(context.Background(), Options{CWD: empty, Herdr: herdr, Git: f.git, Store: f.store})
	if err == nil || err.Error() != "not a git repository: "+empty || !errors.Is(err, gitrepo.ErrNotRepository) {
		t.Fatalf("저장소가 아니라는 오류여야 한다: %v", err)
	}
	if errors.Is(err, ErrNoTerminal) {
		t.Fatal("터미널 오류와 섞이면 안 된다")
	}
}

// 입력이 끝나면(EOF) 화면은 닫힌다. 키를 받을 길이 없는 화면은 매달려 있을 수 없다.
func TestLoopEndsWhenInputCloses(t *testing.T) {
	f := newFixture(t)
	_, h := start(t, f, newFakeHerdr(f), f.work)
	h.waitFor(t, "표", hasRows)
	close(h.keys)
	if err := h.finished(t); err != nil {
		t.Fatal(err)
	}
}

// EOF 와 부모 취소도 이미 시작한 삭제를 끊으면 안 된다.
func TestLoopWaitsForRemovalOnInputEnd(t *testing.T) {
	for _, ending := range []string{"eof", "cancel"} {
		t.Run(ending, func(t *testing.T) {
			f := newFixture(t)
			path := f.safeWorktree(t, "done")
			f.fetch(t)
			herdr := &waitingRemovalHerdr{fakeHerdr: newFakeHerdr(f), entered: make(chan context.Context, 1), release: make(chan struct{})}
			herdr.open(path, "wDONE")
			_, h := start(t, f, herdr, f.work)
			h.waitFor(t, "safe 행", func(m Model) bool { return len(m.SafeRows()) == 1 })
			h.press(t, key('d'))
			var removalCtx context.Context
			select {
			case removalCtx = <-herdr.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("삭제가 시작되지 않았다")
			}
			if ending == "eof" {
				close(h.keys)
			} else {
				h.cancel()
			}
			select {
			case err := <-h.done:
				close(herdr.release)
				t.Fatalf("삭제 중 반환: %v", err)
			case <-removalCtx.Done():
				close(herdr.release)
				t.Fatal("삭제 컨텍스트가 취소됨")
			case <-time.After(100 * time.Millisecond):
			}
			close(herdr.release)
			err := h.finished(t)
			if ending == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("취소 결과: %v", err)
			}
			if ending == "eof" && err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("삭제 미완료: %v", err)
			}
		})
	}
}

func TestRemoveRechecksDetachedHead(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.base, "detached")
	run(t, f.work, "git", "worktree", "add", "--quiet", "--detach", path, "main")
	c, err := newController(context.Background(), Options{CWD: f.work, Herdr: newFakeHerdr(f), Git: f.git, Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	before := c.gather(context.Background(), nil)
	var selected Row
	for _, row := range before.rows {
		if canonical(row.Path) == canonical(path) {
			selected = row
		}
	}
	if selected.Verdict != judge.Safe {
		t.Fatalf("초기 판정: %+v", selected)
	}
	f.commitIn(t, path, "new.txt", "new detached work")
	result := c.remove(context.Background(), []Row{selected}, nil)
	if !strings.Contains(result.message, "failed") {
		t.Fatalf("새 커밋 삭제를 거절해야 한다: %s", result.message)
	}
	if _, err := os.Stat(filepath.Join(path, "new.txt")); err != nil {
		t.Fatalf("새 작업 보존: %v", err)
	}
	// 새 HEAD 도 원격 참조에 들어 있으면 재판정은 safe 다. 그래도 확인했던 HEAD 가 아니므로 거절해야 한다.
	run(t, path, "git", "update-ref", "refs/remotes/origin/other", "HEAD")
	after := c.gather(context.Background(), nil)
	for _, row := range after.rows {
		if canonical(row.Path) == canonical(path) && row.Verdict != judge.Safe {
			t.Fatalf("HEAD 비교에 민감하도록 새 커밋도 safe 여야 한다: %+v", row)
		}
	}
	result = c.remove(context.Background(), []Row{selected}, nil)
	if !strings.Contains(result.message, "worktree changed") {
		t.Fatalf("새 HEAD 가 safe 여도 확인 대상이 달라졌으므로 거절: %s", result.message)
	}
	if _, err := os.Stat(filepath.Join(path, "new.txt")); err != nil {
		t.Fatalf("확인하지 않은 커밋의 작업 디렉터리 보존: %v", err)
	}
}

type heldRefreshHerdr struct {
	*fakeHerdr
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

type heldInitialGatherHerdr struct {
	*fakeHerdr
	calls   atomic.Int32
	release chan struct{}
}

func (h *heldInitialGatherHerdr) WorktreeList(ctx context.Context, id, cwd string) (herdrcli.WorktreeListResult, error) {
	if h.calls.Add(1) == 2 {
		select {
		case <-h.release:
		case <-ctx.Done():
			return herdrcli.WorktreeListResult{}, ctx.Err()
		}
	}
	return h.fakeHerdr.WorktreeList(ctx, id, cwd)
}

func TestLoopShowsInventoryBeforeAssessment(t *testing.T) {
	f := newFixture(t)
	f.addWorktree(t, "a-work")
	f.addWorktree(t, "z-work")
	herdr := &heldInitialGatherHerdr{fakeHerdr: newFakeHerdr(f), release: make(chan struct{})}
	_, h := start(t, f, herdr, f.work)
	var first Model
	select {
	case first = <-h.models:
	case <-time.After(5 * time.Second):
		t.Fatal("first frame did not arrive")
	}
	if len(first.Rows) != 3 {
		t.Fatalf("first frame must show all worktrees before their status is available; got %d", len(first.Rows))
	}
	if len(first.SafeRows()) != 0 {
		t.Fatal("unassessed rows must not be removable")
	}
	if _, action := Update(first, key('d')); action != ActionNone {
		t.Fatal("pending selected row can be removed")
	}
	if _, action := Update(first, key('D')); action != ActionNone {
		t.Fatal("pending rows enter removal confirmation")
	}
	h.press(t, key('j'))
	selected := h.waitFor(t, "pending main selected", func(m Model) bool {
		row, ok := m.Selected()
		return ok && row.IsMain
	})
	path := selected.Rows[selected.Cursor].Path
	close(herdr.release)
	ready := h.waitFor(t, "assessment completed", func(m Model) bool { return len(m.SafeRows()) > 0 })
	row, _ := ready.Selected()
	if row.Path != path {
		t.Fatalf("assessment reordering changed the user's selected worktree: %q -> %q", path, row.Path)
	}
	h.press(t, key('q'))
	if err := h.finished(t); err != nil {
		t.Fatal(err)
	}
}

func TestLoopPendingDeleteRetryKeepsTarget(t *testing.T) {
	f := newFixture(t)
	f.dirtyWorktree(t, "a-work")
	f.addWorktree(t, "z-work")
	herdr := &heldInitialGatherHerdr{fakeHerdr: newFakeHerdr(f), release: make(chan struct{})}
	_, h := start(t, f, herdr, f.work)
	first := h.waitFor(t, "pending inventory", func(m Model) bool { return m.Assessing && len(m.Rows) == 3 })
	before, _ := first.Selected()
	h.press(t, key('d'))
	h.waitFor(t, "pending delete refusal", func(m Model) bool { return strings.Contains(m.Message, "try again") })
	close(herdr.release)
	ready := h.waitFor(t, "assessment finished", hasRows)
	after, _ := ready.Selected()
	if before.Path != after.Path {
		t.Fatalf("retry after pending delete refusal changes target: %s -> %s", before.Label(), after.Label())
	}
	if _, action := Update(ready, key('d')); action != ActionNone {
		t.Fatal("retry must still target the dirty worktree")
	}
	h.press(t, key('q'))
	if err := h.finished(t); err != nil {
		t.Fatal(err)
	}
}

func (h *heldRefreshHerdr) WorktreeList(ctx context.Context, id, cwd string) (herdrcli.WorktreeListResult, error) {
	if h.calls.Add(1) == 3 {
		close(h.entered)
		select {
		case <-h.release:
		case <-ctx.Done():
			return herdrcli.WorktreeListResult{}, ctx.Err()
		}
	}
	return h.fakeHerdr.WorktreeList(ctx, id, cwd)
}

func TestLoopConfirmationKeepsOriginalTargets(t *testing.T) {
	f := newFixture(t)
	a := f.safeWorktree(t, "a-done")
	f.fetch(t)
	b := f.safeWorktree(t, "b-done")
	herdr := &heldRefreshHerdr{fakeHerdr: newFakeHerdr(f), entered: make(chan struct{}), release: make(chan struct{})}
	_, h := start(t, f, herdr, f.work)
	h.waitFor(t, "갱신 전 safe 하나", func(m Model) bool { return len(m.SafeRows()) == 1 })
	select {
	case <-herdr.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("배경 갱신이 시작되지 않음")
	}
	h.press(t, key('D'))
	h.waitFor(t, "한 개 삭제 확인", func(m Model) bool { return m.Confirm != nil && m.Confirm.Count == 1 })
	close(herdr.release)
	h.waitFor(t, "확인 중 safe 둘", func(m Model) bool { return m.Confirm != nil && len(m.SafeRows()) == 2 })
	h.press(t, key('y'))
	h.waitFor(t, "확인한 대상만 삭제", func(m Model) bool {
		return strings.HasPrefix(m.Message, "removed 1 worktree") && lacksLabel("a-done")(m)
	})
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Fatalf("확인 대상이 남음: %v", err)
	}
	if _, err := os.Stat(b); err != nil {
		t.Fatalf("확인하지 않은 대상 삭제: %v", err)
	}
	h.press(t, key('q'))
	if err := h.finished(t); err != nil {
		t.Fatal(err)
	}
}
