package freshen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"herdr-git-upstream/internal/config"
	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/state"
)

// herdr는 원본 체크아웃의 HEAD를 기준으로 worktree를 만들고 fetch는 하지 않는다.
// 그래서 손에 쥔 브랜치가 뒤처져 있으면 새 worktree도 뒤처진 자리에서 시작한다.
func TestNewWorktreeIsMovedToTheLatestBase(t *testing.T) {
	fixture := newFixture(t)
	fixture.advanceRemote(t, "B")
	worktree := fixture.addWorktree(t, "feature")

	before := fixture.commitAt(t, worktree)
	if before == fixture.remoteTip(t) {
		t.Fatal("시험 준비가 잘못되었다. 새 worktree 가 이미 최신이다")
	}

	result := fixture.run(t, worktree, "feature")
	if !result.Moved {
		t.Fatal("최신으로 옮겼어야 한다")
	}
	if got := fixture.commitAt(t, worktree); got != fixture.remoteTip(t) {
		t.Fatalf("원격의 최신 커밋이어야 한다: %s", got)
	}
	// 브랜치는 그대로여야 한다. 기준만 앞당기는 것이지 브랜치를 바꾸는 것이 아니다.
	if branch := fixture.branchAt(t, worktree); branch != "feature" {
		t.Fatalf("브랜치가 바뀌면 안 된다: %s", branch)
	}
}

// 이미 최신이면 아무 일도 일어나지 않아야 한다.
func TestAlreadyCurrentWorktreeIsLeftAlone(t *testing.T) {
	fixture := newFixture(t)
	worktree := fixture.addWorktree(t, "feature")
	before := fixture.commitAt(t, worktree)

	result := fixture.run(t, worktree, "feature")
	if result.Moved {
		t.Fatal("옮길 것이 없는데 옮겼다고 답했다")
	}
	if fixture.commitAt(t, worktree) != before {
		t.Fatal("커밋이 움직이면 안 된다")
	}
}

// 새 브랜치에 이미 커밋이 있으면 빨리 감기가 되지 않는다. 그때는 손대지 않는 것이 옳다.
func TestWorktreeWithItsOwnCommitsIsNotTouched(t *testing.T) {
	fixture := newFixture(t)
	fixture.advanceRemote(t, "B")
	worktree := fixture.addWorktree(t, "feature")
	// 사용자가 벌써 작업을 시작했다.
	writeFile(t, filepath.Join(worktree, "mine.txt"), "내 작업")
	run(t, worktree, "git", "add", ".")
	run(t, worktree, "git", "commit", "--quiet", "-m", "내 커밋")
	mine := fixture.commitAt(t, worktree)

	_, err := fixture.attempt(t, worktree, "feature")
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("갈라진 브랜치는 건너뛰어야 한다: %v", err)
	}
	if fixture.commitAt(t, worktree) != mine {
		t.Fatal("사용자의 커밋이 사라졌다")
	}
	if _, err := os.Stat(filepath.Join(worktree, "mine.txt")); err != nil {
		t.Fatal("사용자의 파일이 사라졌다")
	}
}

// 작업 트리에 손댄 것이 있으면 우리가 아는 상황이 아니다.
func TestDirtyWorktreeIsNotTouched(t *testing.T) {
	fixture := newFixture(t)
	fixture.advanceRemote(t, "B")
	worktree := fixture.addWorktree(t, "feature")
	writeFile(t, filepath.Join(worktree, "f"), "고치는 중")
	before := fixture.commitAt(t, worktree)

	_, err := fixture.attempt(t, worktree, "feature")
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("더러운 작업 트리는 건너뛰어야 한다: %v", err)
	}
	if fixture.commitAt(t, worktree) != before {
		t.Fatal("커밋이 움직이면 안 된다")
	}
	if body := readFile(t, filepath.Join(worktree, "f")); body != "고치는 중" {
		t.Fatalf("고치던 내용이 사라졌다: %q", body)
	}
}

// 같은 커밋에 upstream 이 다른 브랜치가 둘 있으면 어느 것이 기준인지 알 수 없다.
// 짐작해서 옮기면 사용자가 의도하지 않은 브랜치 위에서 일하게 되므로 그대로 둔다.
func TestAmbiguousBaseIsLeftAlone(t *testing.T) {
	fixture := newFixture(t)
	// main 과 같은 커밋을 가리키면서 upstream 이 다른 브랜치를 하나 더 만든다.
	run(t, fixture.work, "git", "branch", "release", "main")
	run(t, fixture.work, "git", "config", "branch.release.remote", "origin")
	run(t, fixture.work, "git", "config", "branch.release.merge", "refs/heads/release")
	fixture.advanceRemote(t, "B")
	worktree := fixture.addWorktree(t, "feature")
	before := fixture.commitAt(t, worktree)

	_, err := fixture.attempt(t, worktree, "feature")
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("기준이 모호하면 건너뛰어야 한다: %v", err)
	}
	if fixture.commitAt(t, worktree) != before {
		t.Fatal("모호한데도 옮겼다")
	}
}

// upstream 이 없는 브랜치에서 갈라져 나왔다면 맞출 대상이 없다.
func TestWorktreeWithoutAnyBaseUpstreamIsSkipped(t *testing.T) {
	fixture := newFixture(t)
	run(t, fixture.work, "git", "config", "--unset", "branch.main.remote")
	run(t, fixture.work, "git", "config", "--unset", "branch.main.merge")
	worktree := fixture.addWorktree(t, "feature")

	if _, err := fixture.attempt(t, worktree, "feature"); !errors.Is(err, ErrSkipped) {
		t.Fatalf("기준이 없으면 건너뛰어야 한다: %v", err)
	}
}

// 설정으로 끌 수 있어야 한다.
func TestDisabledByConfig(t *testing.T) {
	fixture := newFixture(t)
	fixture.advanceRemote(t, "B")
	worktree := fixture.addWorktree(t, "feature")
	before := fixture.commitAt(t, worktree)

	setEvent(t, worktree, "feature")
	cfg := fixture.config(t)
	cfg.FreshWorktrees = false
	if _, err := WorktreeCreated(context.Background(), cfg, quietLogger()); !errors.Is(err, ErrSkipped) {
		t.Fatalf("꺼져 있으면 건너뛰어야 한다: %v", err)
	}
	if fixture.commitAt(t, worktree) != before {
		t.Fatal("꺼져 있는데 옮겼다")
	}
}

// 분리된 HEAD 로 만든 worktree 는 앞으로 감을 브랜치가 없다.
func TestDetachedWorktreeIsSkipped(t *testing.T) {
	fixture := newFixture(t)
	worktree := filepath.Join(fixture.base, "detached")
	run(t, fixture.work, "git", "worktree", "add", "--quiet", "--detach", worktree, "HEAD")

	raw, err := json.Marshal(map[string]any{
		"event": "worktree.created",
		"data": map[string]any{
			"type":     "worktree_created",
			"worktree": map[string]any{"path": worktree, "branch": "", "is_detached": true, "is_bare": false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_EVENT_JSON", string(raw))
	if _, err := WorktreeCreated(context.Background(), fixture.config(t), quietLogger()); !errors.Is(err, ErrSkipped) {
		t.Fatalf("분리된 HEAD 는 건너뛰어야 한다: %v", err)
	}
}

func TestMissingEventIsSkipped(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_EVENT_JSON", "")
	if _, err := WorktreeCreated(context.Background(), defaultConfig(t), quietLogger()); !errors.Is(err, ErrSkipped) {
		t.Fatalf("이벤트가 없으면 건너뛰어야 한다: %v", err)
	}
}

// --- 시험 도구 ---------------------------------------------------------------

type fixture struct {
	base   string
	remote string
	seed   string
	work   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 이 없어 건너뛴다")
	}
	base := t.TempDir()
	f := &fixture{
		base:   base,
		remote: filepath.Join(base, "remote.git"),
		seed:   filepath.Join(base, "seed"),
		work:   filepath.Join(base, "work"),
	}
	run(t, base, "git", "init", "--quiet", "--bare", "--initial-branch=main", f.remote)
	run(t, base, "git", "clone", "--quiet", f.remote, f.seed)
	configure(t, f.seed)
	writeFile(t, filepath.Join(f.seed, "f"), "A")
	run(t, f.seed, "git", "add", ".")
	run(t, f.seed, "git", "commit", "--quiet", "-m", "A")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")

	run(t, base, "git", "clone", "--quiet", f.remote, f.work)
	configure(t, f.work)
	return f
}

// advanceRemote는 원격만 앞서 나가게 한다. work 는 그 사실을 아직 모른다.
func (f *fixture) advanceRemote(t *testing.T, body string) {
	t.Helper()
	writeFile(t, filepath.Join(f.seed, "f"), body)
	run(t, f.seed, "git", "commit", "--quiet", "-am", body)
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")
}

// addWorktree는 herdr 와 같은 방식으로, 즉 fetch 없이 HEAD 를 기준으로 worktree 를 만든다.
func (f *fixture) addWorktree(t *testing.T, branch string) string {
	t.Helper()
	path := filepath.Join(f.base, "wt-"+branch)
	run(t, f.work, "git", "worktree", "add", "--quiet", "-b", branch, path, "HEAD")
	return path
}

func (f *fixture) config(t *testing.T) config.Resolved {
	t.Helper()
	return defaultConfig(t)
}

func (f *fixture) run(t *testing.T, worktree, branch string) Result {
	t.Helper()
	result, err := f.attempt(t, worktree, branch)
	if err != nil {
		t.Fatalf("맞추지 못했다: %v", err)
	}
	return result
}

func (f *fixture) attempt(t *testing.T, worktree, branch string) (Result, error) {
	t.Helper()
	setEvent(t, worktree, branch)
	return WorktreeCreated(context.Background(), f.config(t), quietLogger())
}

func (f *fixture) commitAt(t *testing.T, dir string) string {
	t.Helper()
	return output(t, dir, "git", "rev-parse", "HEAD")
}

func (f *fixture) branchAt(t *testing.T, dir string) string {
	t.Helper()
	return output(t, dir, "git", "rev-parse", "--abbrev-ref", "HEAD")
}

func (f *fixture) remoteTip(t *testing.T) string {
	t.Helper()
	return output(t, f.seed, "git", "rev-parse", "main")
}

func setEvent(t *testing.T, worktree, branch string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"event": "worktree.created",
		"data": map[string]any{
			"type": "worktree_created",
			"worktree": map[string]any{
				"path": worktree, "branch": branch, "is_detached": false, "is_bare": false,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_EVENT_JSON", string(raw))
}

func defaultConfig(t *testing.T) config.Resolved {
	t.Helper()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.FetchTimeout = 30 * time.Second
	return cfg
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s 실패: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func output(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s 실패: %v", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
}

func configure(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "config", "user.email", "t@example.invalid")
	run(t, dir, "git", "config", "user.name", "t")
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 원격 release를 고른 팝업이나 기존 브랜치 열기는 같은 HEAD의 main을 따라 앞으로 감으면 안 된다.
func TestPopupCreationPreservesChosenBaseAndExistingBranch(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%v", existing), func(t *testing.T) {
			fixture := newFixture(t)
			t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(fixture.base, "state"))
			git := gitrepo.Runner{}
			repo, err := git.Discover(context.Background(), fixture.work)
			if err != nil {
				t.Fatal(err)
			}
			run(t, fixture.seed, "git", "push", "--quiet", "origin", "main:release")
			run(t, fixture.work, "git", "fetch", "--quiet", "origin")
			fixture.advanceRemote(t, "B")
			if existing {
				run(t, fixture.work, "git", "branch", "--track", "chosen", "origin/release")
			}
			// 기록은 생성 전에 존재해야 한다. 이벤트가 API 응답보다 먼저 실행되어도 보호한다.
			if err := SuppressCreation(state.New(), repo, "chosen"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fixture.base, "chosen")
			if existing {
				run(t, fixture.work, "git", "worktree", "add", "--quiet", path, "chosen")
			} else {
				run(t, fixture.work, "git", "worktree", "add", "--quiet", "-b", "chosen", path, "refs/remotes/origin/release")
				run(t, path, "git", "branch", "--unset-upstream")
			}
			before := fixture.commitAt(t, path)
			if _, err := fixture.attempt(t, path, "chosen"); !errors.Is(err, ErrSkipped) {
				t.Fatalf("선택 기준 보호: %v", err)
			}
			if got := fixture.commitAt(t, path); got != before {
				t.Fatalf("main의 최신 커밋으로 이동하면 안 된다: %s -> %s", before, got)
			}
			if existing {
				if up := output(t, path, "git", "rev-parse", "--abbrev-ref", "@{upstream}"); up != "origin/release" {
					t.Fatalf("기존 upstream 보존: %s", up)
				}
			}
			// 느리게 도착하거나 중복된 이벤트도 응답 후 보호 기록을 읽을 수 있다.
			if _, err := fixture.attempt(t, path, "chosen"); !errors.Is(err, ErrSkipped) {
				t.Fatalf("지연된 이벤트 보호: %v", err)
			}
		})
	}
}

func TestCurrentCreationCanReuseFinishedPopupName(t *testing.T) {
	fixture := newFixture(t)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(fixture.base, "state"))
	repo, err := (gitrepo.Runner{}).Discover(context.Background(), fixture.work)
	if err != nil {
		t.Fatal(err)
	}
	if err := SuppressCreation(state.New(), repo, "feature"); err != nil {
		t.Fatal(err)
	}
	if err := CompleteCreation(state.New(), repo, "feature"); err != nil {
		t.Fatal(err)
	}
	if err := AllowCreation(state.New(), repo, "feature"); err != nil {
		t.Fatal(err)
	}
	fixture.advanceRemote(t, "B")
	path := fixture.addWorktree(t, "feature")
	result := fixture.run(t, path, "feature")
	if !result.Moved || fixture.commitAt(t, path) != fixture.remoteTip(t) {
		t.Fatal("완료된 이름의 Current 재사용은 정상 최신화해야 한다")
	}
}
