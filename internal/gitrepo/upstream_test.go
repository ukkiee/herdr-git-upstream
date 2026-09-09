package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 아직 한 번도 가져온 적 없는 브랜치야말로 이 플러그인이 도와야 할 자리다.
// git 은 그런 브랜치에서 @{upstream} 질문에 답하지 못하므로, 거기서 포기하면 아무 일도 하지 않게 된다.
func TestUpstreamResolvesBranchThatWasNeverFetched(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)

	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)
	// 원격에는 있지만 로컬에는 추적 참조가 없는 브랜치 설정을 손으로 만든다.
	run(t, work, "git", "checkout", "--quiet", "-b", "fresh")
	run(t, work, "git", "config", "branch.fresh.remote", "origin")
	run(t, work, "git", "config", "branch.fresh.merge", "refs/heads/fresh")

	ctx := context.Background()
	runner := Runner{Timeout: 30 * time.Second}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	// 전제 조건을 확인한다. git 자신은 이 물음에 답하지 못해야 한다.
	if _, err := runner.gitLine(ctx, repo.Root, "rev-parse", "--symbolic-full-name", "fresh@{upstream}"); err == nil {
		t.Skip("이 git 판에서는 추적 참조 없이도 @{upstream} 이 답한다")
	}

	up, err := runner.Upstream(ctx, repo)
	if err != nil {
		t.Fatalf("추적 참조가 없어도 upstream 을 알아내야 한다: %v", err)
	}
	if up.TrackingRef != "refs/remotes/origin/fresh" {
		t.Fatalf("추적 참조 이름이 예상과 다르다: %q", up.TrackingRef)
	}
}

// 사용자 지정 fetch 참조 사양을 쓰는 저장소에서는 refs/remotes/<원격>/<브랜치> 라는 통념이 맞지 않는다.
func TestTrackingRefFollowsCustomRefspec(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)

	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)
	run(t, work, "git", "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/mirror/*")
	run(t, work, "git", "checkout", "--quiet", "-b", "fresh")
	run(t, work, "git", "config", "branch.fresh.remote", "origin")
	run(t, work, "git", "config", "branch.fresh.merge", "refs/heads/fresh")

	ctx := context.Background()
	runner := Runner{Timeout: 30 * time.Second}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	got := runner.trackingRefFromRefspec(ctx, repo, "origin", "refs/heads/fresh")
	if got != "refs/remotes/mirror/fresh" {
		t.Fatalf("설정된 참조 사양을 따르지 않았다: %q", got)
	}
}

func TestTrackingRefFromRefspecHandlesExactMapping(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)

	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)
	// 별표 없이 한 참조만 짝짓는 사양.
	run(t, work, "git", "config", "remote.origin.fetch", "+refs/heads/main:refs/remotes/only/main")

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.trackingRefFromRefspec(ctx, repo, "origin", "refs/heads/main"); got != "refs/remotes/only/main" {
		t.Fatalf("한 참조 짝짓기를 처리하지 못했다: %q", got)
	}
	if got := runner.trackingRefFromRefspec(ctx, repo, "origin", "refs/heads/other"); got != "" {
		t.Fatalf("짝이 없는 참조에는 답하지 않아야 한다: %q", got)
	}
}

// `git branch --set-upstream-to=main` 은 remote 를 "." 으로 적는다. 가져올 원격이 없다.
func TestLocalTrackingBranchIsSkipped(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)

	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)
	run(t, work, "git", "checkout", "--quiet", "-b", "local-track")
	run(t, work, "git", "branch", "--set-upstream-to=main", "local-track")

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	if got := run2(t, work, "git", "config", "--get", "branch.local-track.remote"); got != "." {
		t.Skipf("이 git 판은 로컬 추적을 다르게 적는다: %q", got)
	}
	if _, err := runner.Upstream(ctx, repo); err != ErrNoUpstream {
		t.Fatalf("로컬 추적 브랜치는 건너뛰어야 한다: %v", err)
	}
}

// 원격 이름에는 "/" 가 들어갈 수 있다. 문자열 모양만 보고 URL 이라고 단정하면 멀쩡한 저장소를 놓친다.
func TestRemoteNameContainingSlashIsAccepted(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)

	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)
	run(t, work, "git", "remote", "add", "team/upstream", remote)
	run(t, work, "git", "config", "branch.main.remote", "team/upstream")
	run(t, work, "git", "config", "remote.team/upstream.fetch", "+refs/heads/*:refs/remotes/team/upstream/*")

	ctx := context.Background()
	runner := Runner{Timeout: 30 * time.Second}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	up, err := runner.Upstream(ctx, repo)
	if err != nil {
		t.Fatalf("\"/\" 가 든 원격 이름을 받아들여야 한다: %v", err)
	}
	if up.Remote != "team/upstream" {
		t.Fatalf("원격 이름이 다르다: %q", up.Remote)
	}
	if err := runner.Fetch(ctx, repo, up); err != nil {
		t.Fatalf("가져오지 못했다: %v", err)
	}
}

// 등록되지 않은 이름이 remote 자리에 적혀 있으면(대개 URL 을 직접 적은 경우) 갱신할 추적 참조가 없다.
func TestUnregisteredRemoteIsSkipped(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)

	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)
	run(t, work, "git", "config", "branch.main.remote", "https://example.invalid/x.git")

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Upstream(ctx, repo); err != ErrNoUpstream {
		t.Fatalf("등록되지 않은 원격은 건너뛰어야 한다: %v", err)
	}
}

// 배경 fetch 가 FETCH_HEAD 를 다시 쓰면, 손으로 fetch 한 뒤 merge FETCH_HEAD 하려던 사람이
// 엉뚱한 커밋을 병합하게 된다.
func TestFetchDoesNotDisturbFetchHead(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, seed := seedRemote(t, base)

	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)

	// 사용자가 손으로 fetch 해서 FETCH_HEAD 를 만들어 둔 상태를 흉내 낸다.
	run(t, work, "git", "fetch", "--quiet", "origin")
	fetchHead := filepath.Join(work, ".git", "FETCH_HEAD")
	before, err := os.ReadFile(fetchHead)
	if err != nil {
		t.Skipf("이 git 판은 FETCH_HEAD 를 남기지 않는다: %v", err)
	}

	// 그 사이 원격이 앞서 나간다.
	writeFile(t, filepath.Join(seed, "f"), "2")
	run(t, seed, "git", "commit", "--quiet", "-am", "two")
	run(t, seed, "git", "push", "--quiet", "origin", "HEAD:refs/heads/main")

	ctx := context.Background()
	runner := Runner{Timeout: 30 * time.Second}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	up, err := runner.Upstream(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Fetch(ctx, repo, up); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(fetchHead)
	if err != nil {
		t.Fatalf("FETCH_HEAD 가 사라졌다: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("배경 fetch 가 FETCH_HEAD 를 건드렸다:\n이전: %s\n이후: %s", before, after)
	}
	// 그러면서도 추적 참조는 갱신되어야 한다.
	counts, err := runner.CountsFor(ctx, repo, up)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Behind != 1 {
		t.Fatalf("추적 참조가 갱신되지 않았다: %+v", counts)
	}
}

// 전용 키나 ProxyCommand 를 쓰려고 ssh 명령을 정해 둔 사람의 설정을 덮으면 fetch 자체가 실패한다.
func TestFetchEnvLeavesUserSSHSettingsAlone(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("설정이 없으면 우리가 정한다", func(t *testing.T) {
		t.Setenv("GIT_SSH_COMMAND", "")
		if !hasSSHCommand(runner.fetchEnv(ctx, repo)) {
			t.Fatal("아무 설정도 없을 때는 비대화형 ssh 를 지정해야 한다")
		}
	})
	t.Run("환경변수가 있으면 두어야 한다", func(t *testing.T) {
		t.Setenv("GIT_SSH_COMMAND", "ssh -i /전용/키")
		for _, entry := range runner.fetchEnv(ctx, repo) {
			if strings.HasPrefix(entry, "GIT_SSH_COMMAND=") && entry != "GIT_SSH_COMMAND=ssh -i /전용/키" {
				t.Fatalf("사용자의 ssh 설정을 덮었다: %q", entry)
			}
		}
	})
	t.Run("core.sshCommand 가 있으면 두어야 한다", func(t *testing.T) {
		t.Setenv("GIT_SSH_COMMAND", "")
		run(t, work, "git", "config", "core.sshCommand", "ssh -i /전용/키")
		if hasSSHCommand(runner.fetchEnv(ctx, repo)) {
			t.Fatal("core.sshCommand 가 있으면 환경변수로 덮지 않아야 한다")
		}
	})
}

func hasSSHCommand(env []string) bool {
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_SSH_COMMAND=") && entry != "GIT_SSH_COMMAND=" {
			return true
		}
	}
	return false
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 이 없어 건너뛴다")
	}
}

// seedRemote는 커밋 하나가 든 bare 원격과 그것을 만든 작업 사본을 돌려준다.
func seedRemote(t *testing.T, base string) (remote, seed string) {
	t.Helper()
	remote = filepath.Join(base, "remote.git")
	run(t, base, "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)
	seed = filepath.Join(base, "seed")
	run(t, base, "git", "clone", "--quiet", remote, seed)
	configure(t, seed)
	writeFile(t, filepath.Join(seed, "f"), "1")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "--quiet", "-m", "one")
	run(t, seed, "git", "push", "--quiet", "origin", "HEAD:refs/heads/main")
	return remote, seed
}

// run2는 명령을 돌리고 첫 줄을 돌려준다.
func run2(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
