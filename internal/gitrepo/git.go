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
	// "."은 같은 저장소 안의 다른 브랜치를 따라간다는 뜻이다(git branch --set-upstream-to=main).
	// 가져올 원격이 없으므로 이 플러그인이 할 일이 없다.
	if remote == "." {
		return Upstream{}, ErrNoUpstream
	}
	// 이름 모양으로 짐작하지 않고 git에게 등록된 원격인지 묻는다. 원격 이름에는 "/"가 들어갈 수 있어서
	// (예: team/upstream) 문자열만 보고 URL이라고 단정하면 멀쩡한 저장소를 건너뛰게 된다.
	if url, err := r.gitLine(ctx, repo.Root, "config", "--get", "remote."+remote+".url"); err != nil || url == "" {
		return Upstream{}, ErrNoUpstream
	}

	remoteRef, err := r.gitLine(ctx, repo.Root, "config", "--get", "branch."+branch+".merge")
	if err != nil || remoteRef == "" {
		return Upstream{}, ErrNoUpstream
	}

	trackingRef := r.trackingRef(ctx, repo, branch, remote, remoteRef)
	if trackingRef == "" {
		return Upstream{}, ErrNoUpstream
	}

	return Upstream{Branch: branch, Remote: remote, RemoteRef: remoteRef, TrackingRef: trackingRef}, nil
}

// trackingRef는 이 브랜치가 견주는 원격 추적 참조의 전체 이름을 알아낸다.
//
// 세 단계로 찾는다. 우선 git에게 직접 묻는다. 다만 이 물음은 참조가 이미 로컬에 있을 때만 답하므로,
// 아직 한 번도 가져온 적 없는 브랜치에서는 실패한다. 그 경우가 바로 이 플러그인이 도와야 할
// 상황이라서, 여기서 포기하면 정작 필요한 자리에서 아무 일도 하지 않게 된다.
// 그래서 다음으로 원격에 설정된 fetch 참조 사양을 보고 대응되는 이름을 계산하고,
// 그것도 없으면 관례대로 refs/remotes/<원격>/<브랜치>를 쓴다.
func (r Runner) trackingRef(ctx context.Context, repo Repo, branch, remote, remoteRef string) string {
	if ref, err := r.gitLine(ctx, repo.Root, "rev-parse", "--symbolic-full-name", branch+"@{upstream}"); err == nil && ref != "" {
		return ref
	}
	if ref := r.trackingRefFromRefspec(ctx, repo, remote, remoteRef); ref != "" {
		return ref
	}
	return "refs/remotes/" + remote + "/" + strings.TrimPrefix(remoteRef, "refs/heads/")
}

// trackingRefFromRefspec은 원격에 설정된 fetch 참조 사양에 remoteRef를 대입해 목적지 이름을 만든다.
// 참조 사양을 손댄 저장소에서는 refs/remotes/<원격>/<브랜치>라는 통념이 맞지 않기 때문에,
// 관례로 넘어가기 전에 실제 설정을 먼저 본다.
func (r Runner) trackingRefFromRefspec(ctx context.Context, repo Repo, remote, remoteRef string) string {
	out, err := r.git(ctx, repo.Root, localTimeout, "config", "--get-all", "remote."+remote+".fetch")
	if err != nil {
		return ""
	}
	for _, spec := range splitLines(out) {
		spec = strings.TrimPrefix(spec, "+")
		source, destination, found := strings.Cut(spec, ":")
		if !found || source == "" || destination == "" {
			continue
		}
		// 별표가 없는 사양은 한 참조만 짝짓는다.
		if !strings.Contains(source, "*") {
			if source == remoteRef {
				return destination
			}
			continue
		}
		prefix, suffix, _ := strings.Cut(source, "*")
		if !strings.HasPrefix(remoteRef, prefix) || !strings.HasSuffix(remoteRef, suffix) {
			continue
		}
		middle := remoteRef[len(prefix) : len(remoteRef)-len(suffix)]
		if !strings.Contains(destination, "*") {
			continue
		}
		return strings.Replace(destination, "*", middle, 1)
	}
	return ""
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
//
// --no-write-fetch-head는 사용자의 작업을 건드리지 않기 위한 것이다. 이것이 없으면 배경에서 도는
// 이 fetch가 FETCH_HEAD를 다시 쓰는데, 사용자가 방금 손으로 fetch한 뒤 `git merge FETCH_HEAD`를
// 하려던 참이었다면 엉뚱한 커밋을 병합하게 된다.
//
// 참조 사양 앞의 "+"는 강제 갱신을 뜻한다. 원격 추적 참조는 원격을 그대로 비추는 자리이고 git 자신의
// 기본 참조 사양도 강제이므로, 되감기 push가 일어난 뒤에도 숫자가 사실과 어긋나지 않게 하려면 필요하다.
func (r Runner) Fetch(ctx context.Context, repo Repo, up Upstream) error {
	refspec := "+" + up.RemoteRef + ":" + up.TrackingRef
	_, err := r.gitWithEnv(ctx, repo.Root, r.fetchTimeout(), r.fetchEnv(ctx, repo),
		"-c", "gc.auto=0",
		"fetch", "--quiet", "--no-tags", "--no-prune", "--no-prune-tags",
		"--no-recurse-submodules", "--no-write-fetch-head",
		"--", up.Remote, refspec,
	)
	return err
}

// fetchEnv는 fetch에 쓸 환경을 만든다.
//
// ssh 명령을 우리가 정하는 것은 사용자가 아무 설정도 해 두지 않았을 때뿐이다. 전용 키나 ProxyCommand를
// 쓰려고 GIT_SSH_COMMAND나 core.sshCommand를 지정해 둔 사람의 설정을 덮으면 fetch 자체가 실패한다.
// 사용자의 설정을 그대로 두면 암호 입력을 기다리며 멈출 수는 있지만, 그것은 제한 시간이 걷어 낸다.
func (r Runner) fetchEnv(ctx context.Context, repo Repo) []string {
	env := nonInteractiveEnv()
	if os.Getenv("GIT_SSH_COMMAND") != "" {
		return env
	}
	if configured, err := r.gitLine(ctx, repo.Root, "config", "--get", "core.sshCommand"); err == nil && configured != "" {
		return env
	}
	return append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10")
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
	return r.gitWithEnv(ctx, dir, timeout, nonInteractiveEnv(), args...)
}

// gitWithEnv는 환경을 지정해 git 명령을 실행한다.
func (r Runner) gitWithEnv(ctx context.Context, dir string, timeout time.Duration, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
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
//
// ssh 설정은 여기서 건드리지 않는다. 사용자가 정해 둔 것을 덮을 위험이 있어 fetchEnv가 따로 판단한다.
func nonInteractiveEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		// 대화형 자격 증명 관리자도 막는다.
		"GCM_INTERACTIVE=never",
		// 인덱스 같은 부가적인 잠금을 잡지 않아, 사용자가 동시에 쓰는 git과 부딪히지 않는다.
		"GIT_OPTIONAL_LOCKS=0",
	)
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
