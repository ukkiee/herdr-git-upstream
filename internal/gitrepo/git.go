// Package gitrepo는 저장소를 찾아내고, 원격 추적 참조를 갱신하고, 앞뒤 커밋 수를 센다.
//
// 여기서 하는 일은 herdr가 사이드바에 그리는 값과 정확히 같은 근거를 만드는 것이다. herdr는
// HEAD와 원격 추적 참조(refs/remotes/...)를 비교해 ↑↓를 그리지만, 그 참조를 스스로 갱신하지는
// 않는다. 그래서 이 패키지가 fetch로 참조를 최신으로 만들어 주면 herdr의 표시가 실제 원격 상태와
// 맞아떨어진다.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrNotRepository는 주어진 디렉터리가 git 저장소 안이 아닐 때 나온다.
var ErrNotRepository = errors.New("git 저장소가 아니다")

// ErrNoUpstream은 비교할 대상이 없을 때 나온다. HEAD가 분리되어 있거나 upstream이 설정되지 않은 경우다.
var ErrNoUpstream = errors.New("upstream이 없다")

// Repo는 한 작업 디렉터리가 속한 저장소를 가리킨다.
type Repo struct {
	// Root는 작업 트리의 최상위다. git 명령을 이 위치에서 실행한다.
	Root string
	// CommonDir는 참조를 보관하는 디렉터리다. 연결된 worktree들은 이 값을 공유한다.
	// 같은 참조를 두 번 가져오지 않도록 중복을 거를 때 쓴다.
	CommonDir string
}

// Upstream은 현재 브랜치가 따라가는 원격 브랜치다.
type Upstream struct {
	// Branch는 현재 로컬 브랜치 이름이다.
	Branch string
	// Remote는 원격 이름이다. 흔히 origin이지만 그렇지 않은 저장소도 있어 설정에서 읽는다.
	Remote string
	// RemoteRef는 원격 쪽 참조다. 예: refs/heads/main
	RemoteRef string
	// TrackingRef는 로컬에 저장된 원격 추적 참조다. 예: refs/remotes/origin/main
	// herdr가 비교 대상으로 삼는 것이 바로 이 참조이므로, fetch는 이 참조를 갱신해야 의미가 있다.
	TrackingRef string
}

// FetchKey는 같은 fetch를 두 번 하지 않기 위한 식별자다.
// 연결된 worktree들은 CommonDir를 공유하지만 서로 다른 브랜치에 있을 수 있으므로,
// 참조 사양까지 포함해야 한 저장소의 여러 브랜치를 각각 갱신할 수 있다.
func (u Upstream) FetchKey(commonDir string) string {
	return commonDir + "\x00" + u.Remote + "\x00" + u.RemoteRef + "\x00" + u.TrackingRef
}

// Counts는 upstream 대비 앞뒤 커밋 수다.
type Counts struct {
	Ahead  int
	Behind int
}

// Runner는 git 명령을 실행한다.
type Runner struct {
	// Timeout은 명령 하나당 제한 시간이다.
	Timeout time.Duration
}

// IsMissingRemoteRef는 원격에서 그 브랜치가 사라진 경우인지 답한다.
//
// 다시 시도해도 결과가 같은 실패를 가려내기 위한 것이다. 병합되어 지워진 브랜치에 남아 있으면
// 이 오류가 매번 나는데, 그때마다 사이드바에 경고를 띄우면 사용자가 손쓸 수 없는 소음만 쌓인다.
// git이 이 상황에서 내는 문구가 안정적이라 그것을 근거로 삼는다.
func IsMissingRemoteRef(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "couldn't find remote ref")
}

const (
	defaultTimeout = 20 * time.Second
	// 로컬 정보만 읽는 명령은 네트워크를 타지 않으므로 짧게 끊는다.
	localTimeout = 5 * time.Second
)

// Discover는 디렉터리가 속한 저장소를 찾는다.
func (r Runner) Discover(ctx context.Context, dir string) (Repo, error) {
	if dir == "" {
		return Repo{}, ErrNotRepository
	}
	// 한 번의 호출로 작업 트리 최상위와 공용 참조 디렉터리를 모두 받는다.
	// --path-format=absolute가 없으면 공용 디렉터리가 상대 경로로 나올 수 있어, 중복 제거의 열쇠로 쓸 수 없다.
	out, err := r.git(ctx, dir, localTimeout, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return Repo{}, ErrNotRepository
	}
	lines := splitLines(out)
	if len(lines) < 2 || lines[0] == "" || lines[1] == "" {
		return Repo{}, ErrNotRepository
	}
	return Repo{Root: lines[0], CommonDir: lines[1]}, nil
}

// Upstream은 현재 브랜치가 따라가는 원격 브랜치를 알아낸다.
func (r Runner) Upstream(ctx context.Context, repo Repo) (Upstream, error) {
	// symbolic-ref는 HEAD가 분리되어 있으면 실패한다. 그 상태에서는 비교할 브랜치가 없으므로
	// 여기서 끝내는 것이 맞다.
	out, err := r.git(ctx, repo.Root, localTimeout, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return Upstream{}, ErrNoUpstream
	}
	branch := firstLine(out)
	if branch == "" {
		return Upstream{}, ErrNoUpstream
	}

	remote, err := r.gitLine(ctx, repo.Root, "config", "--get", "branch."+branch+".remote")
	if err != nil || remote == "" {
		return Upstream{}, ErrNoUpstream
	}
	// 원격 이름이 아니라 URL이 적혀 있는 경우가 있다. 이때는 fetch로 갱신할 원격 추적 참조가 없으므로
	// 이 플러그인이 할 수 있는 일이 없다.
	if strings.Contains(remote, "/") || strings.Contains(remote, ":") {
		return Upstream{}, ErrNoUpstream
	}

	remoteRef, err := r.gitLine(ctx, repo.Root, "config", "--get", "branch."+branch+".merge")
	if err != nil || remoteRef == "" {
		return Upstream{}, ErrNoUpstream
	}

	// 추적 참조의 이름을 규칙으로 지어내지 않고 git에게 묻는다. fetch 참조 사양을 손댄 저장소에서는
	// refs/remotes/<remote>/<branch>라는 통념이 맞지 않을 수 있기 때문이다.
	trackingRef, err := r.gitLine(ctx, repo.Root, "rev-parse", "--symbolic-full-name", branch+"@{upstream}")
	if err != nil || trackingRef == "" {
		return Upstream{}, ErrNoUpstream
	}

	return Upstream{Branch: branch, Remote: remote, RemoteRef: remoteRef, TrackingRef: trackingRef}, nil
}

// CountsFor는 HEAD와 추적 참조 사이의 앞뒤 커밋 수를 센다.
// 추적 참조가 아직 로컬에 없으면(한 번도 fetch하지 않은 브랜치) 셀 수 없으므로 오류를 돌려준다.
func (r Runner) CountsFor(ctx context.Context, repo Repo, up Upstream) (Counts, error) {
	out, err := r.gitLine(ctx, repo.Root, "rev-list", "--left-right", "--count", "HEAD..."+up.TrackingRef)
	if err != nil {
		return Counts{}, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return Counts{}, fmt.Errorf("rev-list 출력을 해석하지 못했다: %q", out)
	}
	ahead, err := strconv.Atoi(fields[0])
	if err != nil {
		return Counts{}, fmt.Errorf("앞선 커밋 수를 해석하지 못했다: %w", err)
	}
	behind, err := strconv.Atoi(fields[1])
	if err != nil {
		return Counts{}, fmt.Errorf("뒤처진 커밋 수를 해석하지 못했다: %w", err)
	}
	return Counts{Ahead: ahead, Behind: behind}, nil
}

// Fetch는 현재 브랜치의 원격 추적 참조 하나만 갱신한다.
//
// 좁게 가져오는 것이 핵심이다. 태그와 다른 브랜치까지 끌어오면 이 주기로 반복하기에는 비용이 크고,
// prune은 남의 작업 중인 참조를 지울 수 있다. 자동 정리(gc)도 꺼 둔다. 주기적으로 도는 작업이
// 사용자가 모르는 사이에 무거운 정리 작업을 반복해서 깨우면 곤란하기 때문이다.
func (r Runner) Fetch(ctx context.Context, repo Repo, up Upstream) error {
	refspec := "+" + up.RemoteRef + ":" + up.TrackingRef
	_, err := r.git(ctx, repo.Root, r.fetchTimeout(),
		"-c", "gc.auto=0",
		"fetch", "--quiet", "--no-tags", "--no-prune", "--no-prune-tags", "--no-recurse-submodules",
		"--", up.Remote, refspec,
	)
	return err
}

func (r Runner) fetchTimeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return defaultTimeout
}

func (r Runner) gitLine(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := r.git(ctx, dir, localTimeout, args...)
	if err != nil {
		return "", err
	}
	return firstLine(out), nil
}

// subcommand는 오류 메시지에 쓸 이름을 고른다.
// -c 같은 전역 옵션이 앞에 붙을 수 있어 args[0]을 그대로 쓰면 "git -c: 실패" 같은 문구가 나온다.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "-C", "--git-dir", "--work-tree", "--namespace":
			// 값을 받는 전역 옵션이므로 다음 인자까지 건너뛴다.
			i++
		default:
			if !strings.HasPrefix(args[i], "-") {
				return args[i]
			}
		}
	}
	return "git"
}

// git은 명령 하나를 실행하고 표준 출력을 돌려준다.
func (r Runner) git(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = nonInteractiveEnv()
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 자식을 자기 프로세스 그룹에 넣고, 제한 시간이 지나면 그룹째 정리한다.
	// git fetch는 git-remote-https 같은 도우미 프로세스를 낳는데, 부모만 죽이면 그 도우미가 남는다.
	// 주기적으로 도는 작업에서 이런 잔재가 쌓이면 눈에 띄는 문제가 된다.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	// 종료 신호를 보낸 뒤에도 붙잡고 있지 않도록, 잠깐 기다렸다가 파이프를 놓는다.
	cmd.WaitDelay = 2 * time.Second

	if err := cmd.Run(); err != nil {
		name := subcommand(args)
		detail := strings.TrimSpace(stderr.String())
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
			return nil, fmt.Errorf("git %s: 제한 시간 초과", name)
		}
		if detail != "" {
			return nil, fmt.Errorf("git %s: %w: %s", name, err, detail)
		}
		return nil, fmt.Errorf("git %s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

// nonInteractiveEnv는 git이 사람에게 무언가를 물어보다 멈추는 일이 없도록 환경을 다듬는다.
// 배경에서 도는 작업이 자격 증명 프롬프트를 띄우면 제한 시간까지 그대로 매달려 있게 된다.
func nonInteractiveEnv() []string {
	env := os.Environ()
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		// 인증서나 호스트 키 확인으로 멈추지 않도록 ssh도 비대화형으로 돌린다.
		// 사용자가 이미 GIT_SSH_COMMAND를 정해 두었다면 그쪽이 뒤에 오도록 두어야 하지만,
		// os.Environ()이 앞에 있으므로 뒤에 붙는 이 값이 이긴다. 배경 작업에서는 그 편이 안전하다.
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10",
		// 대화형 자격 증명 관리자도 막는다.
		"GCM_INTERACTIVE=never",
		// 인덱스 같은 부가적인 잠금을 잡지 않아, 사용자가 동시에 쓰는 git과 부딪히지 않는다.
		"GIT_OPTIONAL_LOCKS=0",
	)
	return env
}

func splitLines(out []byte) []string {
	text := strings.ReplaceAll(string(out), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return lines
}

func firstLine(out []byte) string {
	lines := splitLines(out)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}
