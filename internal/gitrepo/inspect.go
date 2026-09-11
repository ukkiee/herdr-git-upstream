package gitrepo

// 이 파일은 판정의 재료가 되는 읽기 전용 명령들을 모은다. 판정 규칙 자체는 internal/judge에 있다.
// 여기에는 "git이 무엇이라 답하는가"만 두고 "그래서 merged인가"는 두지 않는다. 그래야 규칙이 바뀔 때
// git 호출 코드를 건드리지 않고, 반대로 git 판이 바뀌어 출력이 달라져도 규칙은 그대로 둘 수 있다.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// ErrNoRemoteHead는 원격 기본 브랜치가 로컬에 비쳐 있지 않을 때 나온다.
// clone이 아니라 `git remote add`로 붙인 원격에는 refs/remotes/<원격>/HEAD가 없다.
var ErrNoRemoteHead = errors.New("원격 기본 브랜치가 로컬에 없다")

// ErrRemoteHasNoHead는 원격에 직접 물었는데도 기본 브랜치를 알리지 않을 때 나온다. 커밋이 하나도 없는
// 원격이거나 symref를 알리지 않는 옛 서버다. 로컬에 없는 경우(ErrNoRemoteHead)와 오류값을 나누는 이유는
// 이 문구가 데몬 로그에 그대로 찍히기 때문이다. 원격에 물은 결과를 "로컬에 없다"고 적으면 문제를
// 살펴보는 사람이 엉뚱하게 로컬 참조를 의심하게 된다.
var ErrRemoteHasNoHead = errors.New("원격이 기본 브랜치를 알리지 않는다")

// gitVersion은 한 번 알아낸 git 판을 프로세스 안에서 나눠 쓴다.
//
// sync.Once가 아니라 뮤텍스와 성공 표시를 쓰는 이유가 있다. 첫 호출이 제한 시간 같은 일시적인 이유로
// 실패했을 때 그 실패가 프로세스가 사는 내내 굳어 버리면, 데몬은 merge-tree를 쓸 수 있는 git 위에서도
// 끝까지 catchup 판정을 쉰다. 성공한 답만 굳히고 실패는 다음 호출이 다시 물어본다.
var gitVersion struct {
	mu    sync.Mutex
	known bool
	major int
	minor int
	text  string
}

// Version은 git의 판 번호를 돌려준다. 결과는 프로세스 안에서 한 번만 계산해 재사용한다.
func (r Runner) Version(ctx context.Context) (major, minor int, err error) {
	gitVersion.mu.Lock()
	defer gitVersion.mu.Unlock()
	if gitVersion.known {
		return gitVersion.major, gitVersion.minor, nil
	}
	out, err := r.git(ctx, "", localTimeout, "version")
	if err != nil {
		return 0, 0, err
	}
	major, minor, text, err := parseVersion(firstLine(out))
	if err != nil {
		return 0, 0, err
	}
	gitVersion.known, gitVersion.major, gitVersion.minor, gitVersion.text = true, major, minor, text
	return major, minor, nil
}

// VersionText는 `git version`이 말한 판 번호 그대로를 돌려준다(예: "2.39.5"). 알 수 없으면 빈 문자열이다.
// status 출력처럼 사람에게 보여 줄 자리를 위한 것이다.
func (r Runner) VersionText(ctx context.Context) string {
	if _, _, err := r.Version(ctx); err != nil {
		return ""
	}
	gitVersion.mu.Lock()
	defer gitVersion.mu.Unlock()
	return gitVersion.text
}

// parseVersion은 `git version` 첫 줄을 해석한다.
//
// 배포판마다 뒤에 붙이는 것이 다르다. 애플은 "git version 2.39.5 (Apple Git-154)", 윈도우는
// "git version 2.45.1.windows.1"이다. 그래서 "git version " 뒤의 첫 낱말만 취하고, 그것을 점으로
// 나눈 앞 두 조각만 숫자로 읽는다.
func parseVersion(line string) (major, minor int, text string, err error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "git version ")
	if !ok {
		return 0, 0, "", fmt.Errorf("git version 출력을 해석하지 못했다: %q", line)
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0, 0, "", fmt.Errorf("git version 출력을 해석하지 못했다: %q", line)
	}
	text = fields[0]
	parts := strings.Split(text, ".")
	if len(parts) < 2 {
		return 0, 0, "", fmt.Errorf("git 판 번호를 해석하지 못했다: %q", text)
	}
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, "", fmt.Errorf("git 판 번호를 해석하지 못했다: %q", text)
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, "", fmt.Errorf("git 판 번호를 해석하지 못했다: %q", text)
	}
	return major, minor, text, nil
}

// SupportsMergeTree는 `git merge-tree --write-tree`를 쓸 수 있는 git인지 답한다. 2.38에서 생겼다.
// 판을 알 수 없으면 거짓이다. 없는 옵션을 부르면 매 회차 오류만 쌓이므로, 모르면 쓰지 않는 쪽이 옳다.
func (r Runner) SupportsMergeTree(ctx context.Context) bool {
	major, minor, err := r.Version(ctx)
	if err != nil {
		return false
	}
	return supportsMergeTree(major, minor)
}

func supportsMergeTree(major, minor int) bool {
	return major > 2 || (major == 2 && minor >= 38)
}

// RemoteHead는 원격 기본 브랜치의 추적 참조를 돌려준다. refs/remotes/<원격>/HEAD가 가리키는 것이다.
// 로컬 명령 하나라 값이 싸고, clone으로 만든 저장소에는 거의 언제나 있다.
func (r Runner) RemoteHead(ctx context.Context, repo Repo, remote string) (trackingRef string, err error) {
	ref, err := r.gitLine(ctx, repo.Root, "symbolic-ref", "--quiet", "refs/remotes/"+remote+"/HEAD")
	if err != nil || ref == "" {
		return "", ErrNoRemoteHead
	}
	return ref, nil
}

// RemoteHeadFromRemote는 원격에 직접 물어 기본 브랜치의 원격 쪽 참조를 알아낸다. 예: refs/heads/main
//
// 네트워크를 탄다. `git ls-remote --symref <원격> HEAD`는 "ref: refs/heads/main\tHEAD" 한 줄로 답한다.
// fetch와 같은 환경과 제한 시간을 쓰는 이유는 같은 원격에 같은 방식으로 닿아야 하기 때문이다.
func (r Runner) RemoteHeadFromRemote(ctx context.Context, repo Repo, remote string) (remoteRef string, err error) {
	out, err := r.gitWithEnv(ctx, repo.Root, r.fetchTimeout(), r.fetchEnv(ctx, repo),
		"ls-remote", "--symref", "--", remote, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range splitLines(out) {
		target, ok := strings.CutPrefix(line, "ref: ")
		if !ok {
			continue
		}
		ref, name, found := strings.Cut(target, "\t")
		if found && name == "HEAD" && ref != "" {
			return ref, nil
		}
	}
	// 원격이 HEAD를 갖지 않는 경우(비어 있는 저장소)나 symref를 알리지 않는 옛 서버다.
	return "", ErrRemoteHasNoHead
}

// RemoteHeads는 원격에 지금 있는 브랜치들의 참조 이름(refs/heads/...)을 모은다. 네트워크를 탄다.
//
// worktree 화면의 gone 판정에 쓴다. 데몬의 fetch 기록은 열린 워크스페이스에만 있어서 나머지 worktree 의
// gone 을 알 수 없고, prune 은 사용자의 참조를 지우므로 하지 않는다. `git ls-remote --heads` 는 참조를
// 건드리지 않고 왕복 한 번으로 원격의 브랜치 목록을 준다. upstream 의 원격 쪽 참조가 이 목록에 없으면
// 그 브랜치는 원격에서 사라진 것이다.
//
// 한 줄은 `<해시>\t<참조 이름>` 이다. --heads 만 주었으므로 refs/heads/ 아래만 오지만, 그래도 그 앞부분을
// 확인해 다른 것이 섞여도 목록이 오염되지 않게 한다.
func (r Runner) RemoteHeads(ctx context.Context, repo Repo, remote string) (map[string]bool, error) {
	if remote == "" {
		return nil, fmt.Errorf("원격 이름이 비어 있다")
	}
	out, err := r.gitWithEnv(ctx, repo.Root, r.fetchTimeout(), r.fetchEnv(ctx, repo),
		"ls-remote", "--heads", "--", remote)
	if err != nil {
		return nil, err
	}
	heads := map[string]bool{}
	for _, line := range splitLines(out) {
		_, ref, found := strings.Cut(line, "\t")
		if !found || !strings.HasPrefix(ref, "refs/heads/") {
			continue
		}
		heads[ref] = true
	}
	return heads, nil
}

// Remotes는 등록된 원격 이름들을 돌려준다.
func (r Runner) Remotes(ctx context.Context, repo Repo) ([]string, error) {
	out, err := r.git(ctx, repo.Root, localTimeout, "remote")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range splitLines(out) {
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// mergeTargetKey는 저장소별 통합 브랜치를 적어 두는 git 설정 키다.
// 설정 파일이 아니라 git config에 두는 이유는 ADR 0001에 있다. 연결된 worktree 전부가 자동으로 나눠 쓴다.
const mergeTargetKey = "git-upstream.mergeTarget"

// MergeTargets는 저장소에 지정된 통합 브랜치들을 `origin/develop` 꼴 그대로 돌려준다.
// 설정이 없으면 빈 목록이고 오류가 아니다. 대부분의 저장소는 원격 기본 브랜치만으로 충분하다.
func (r Runner) MergeTargets(ctx context.Context, repo Repo) ([]string, error) {
	res, err := r.gitExit(ctx, repo.Root, localTimeout, nonInteractiveEnv(), "config", "--get-all", mergeTargetKey)
	if err != nil {
		return nil, err
	}
	// config --get-all은 키가 없으면 1로 끝난다. 그것은 "없다"는 답이지 실패가 아니다.
	if res.code == 1 {
		return nil, nil
	}
	if res.code != 0 {
		return nil, res.asError([]string{"config"})
	}
	var targets []string
	for _, line := range splitLines(res.stdout) {
		if line != "" {
			targets = append(targets, line)
		}
	}
	return targets, nil
}

// TargetFromShortRef는 `origin/widget-studio/dev` 같은 짧은 이름을 Upstream으로 바꾼다.
//
// 원격 이름에 "/"가 들어갈 수 있어서 첫 "/"에서 자를 수 없다. 등록된 원격 목록과 앞부분을 견주되
// 가장 긴 일치를 고른다. "team"과 "team/upstream"이 둘 다 원격이면 "team/upstream/dev"는 뒤쪽이다.
// 등록되지 않은 원격이면 오류다. 그런 값은 오타일 가능성이 높으므로 조용히 넘기지 않는다.
func (r Runner) TargetFromShortRef(ctx context.Context, repo Repo, short string) (Upstream, error) {
	short = strings.TrimPrefix(strings.TrimSpace(short), "refs/remotes/")
	remotes, err := r.Remotes(ctx, repo)
	if err != nil {
		return Upstream{}, err
	}
	remote, branch := "", ""
	for _, candidate := range remotes {
		rest, ok := strings.CutPrefix(short, candidate+"/")
		if !ok || rest == "" {
			continue
		}
		if len(candidate) > len(remote) {
			remote, branch = candidate, rest
		}
	}
	if remote == "" {
		return Upstream{}, fmt.Errorf("%s %q: 등록된 원격으로 시작하지 않는다", mergeTargetKey, short)
	}
	remoteRef := "refs/heads/" + branch
	return Upstream{
		Remote:      remote,
		RemoteRef:   remoteRef,
		TrackingRef: r.TrackingRefFor(ctx, repo, remote, remoteRef),
	}, nil
}

// ContainingRemoteRefs는 주어진 커밋을 품고 있는 원격 브랜치의 추적 참조들을 돌려준다.
//
// 참조가 수백 개여도 로컬 명령 하나라 값이 싸다. refs/remotes/<원격>/HEAD는 다른 참조를 가리키는
// 별명이라 같은 답이 두 번 나오므로 뺀다.
//
// 브랜치가 아닌 것도 뺀다. `+refs/pull/*/head:refs/remotes/origin/pr/*` 같은 참조 사양(GitHub와 gh가
// 흔히 넣는다)은 열린 PR의 head를 refs/remotes/ 아래로 가져오는데, 자기 PR의 head는 언제나 HEAD를
// 품고 있으므로 그것을 세면 병합되지 않은 브랜치가 merged가 된다. refs/* 같은 넓은 참조 사양도
// 브랜치와 PR을 함께 가져오므로, 각 추적 참조를 원격 쪽 출발지로 돌려서 브랜치인지 확인한다.
func (r Runner) ContainingRemoteRefs(ctx context.Context, repo Repo, commit string) ([]string, error) {
	out, err := r.git(ctx, repo.Root, localTimeout,
		"for-each-ref", "--format=%(refname)", "--contains", commit, "refs/remotes/")
	if err != nil {
		return nil, err
	}
	specs := r.allFetchRefspecs(ctx, repo)
	var refs []string
	for _, line := range splitLines(out) {
		if line == "" || strings.HasSuffix(line, "/HEAD") || isNonBranchTrackingRef(specs, line) {
			continue
		}
		refs = append(refs, line)
	}
	return refs, nil
}

// allFetchRefspecs는 등록된 원격 전부의 참조 사양을 모은다. PR 참조는 어느 원격에나 설정될 수 있다.
func (r Runner) allFetchRefspecs(ctx context.Context, repo Repo) []refspec {
	remotes, err := r.Remotes(ctx, repo)
	if err != nil {
		return nil
	}
	var specs []refspec
	for _, remote := range remotes {
		specs = append(specs, r.fetchRefspecs(ctx, repo, remote)...)
	}
	return specs
}

// 겹치는 사양 중 하나라도 브랜치 아닌 출발지를 가리키면 근거에서 뺀다. 같은 목적지를 넓은 브랜치
// 사양과 좁은 PR 사양이 함께 덮을 수 있으므로, 브랜치 사양 하나와 맞았다고 먼저 허용하면 안 된다.
func isNonBranchTrackingRef(specs []refspec, ref string) bool {
	for _, spec := range specs {
		if source, ok := substituteRefspec(spec.destination, spec.source, ref); ok && !strings.HasPrefix(source, "refs/heads/") {
			return true
		}
	}
	return false
}

// TreeOf는 참조가 가리키는 트리 객체의 해시를 돌려준다.
func (r Runner) TreeOf(ctx context.Context, repo Repo, ref string) (string, error) {
	return r.gitLine(ctx, repo.Root, "rev-parse", "--verify", "--quiet", ref+"^{tree}")
}

// CommitOf는 참조가 가리키는 커밋의 해시를 돌려준다. 참조가 로컬에 없으면 오류다.
func (r Runner) CommitOf(ctx context.Context, repo Repo, ref string) (string, error) {
	return r.gitLine(ctx, repo.Root, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
}

// MergeTree는 작업 트리를 건드리지 않고 두 커밋을 병합해 본 결과를 돌려준다.
//
// 종료 코드가 답이다. 0이면 깨끗하게 병합되며 결과 트리를, 1이면 충돌이 있고 첫 줄이 충돌 표시가
// 든 결과 트리다. 그 밖의 종료 코드는 판정 불가다. git 2.38 이상에서만 있는 명령이므로 호출자는
// SupportsMergeTree를 먼저 본다.
//
// 결과 트리 객체는 객체 저장소에 남는다. 같은 두 커밋을 다시 병합하면 같은 트리가 나와 새로 쓰이지
// 않으므로 반복 호출로 저장소가 자라지는 않는다. 그래도 자동 정리(gc)가 깨어나는 일은 막아 둔다.
func (r Runner) MergeTree(ctx context.Context, repo Repo, base, head string) (tree string, conflict bool, err error) {
	res, err := r.gitExit(ctx, repo.Root, localTimeout, nonInteractiveEnv(),
		"-c", "gc.auto=0", "merge-tree", "--write-tree", "--", base, head)
	if err != nil {
		return "", false, err
	}
	switch res.code {
	case 0, 1:
		tree = firstLine(res.stdout)
		if tree == "" {
			return "", false, fmt.Errorf("git merge-tree: 결과 트리를 읽지 못했다")
		}
		return tree, res.code == 1, nil
	default:
		return "", false, res.asError([]string{"merge-tree"})
	}
}

// IsUntouched는 추적되지 않은 파일까지 하나도 없는지 답한다.
//
// IsClean과 다른 물음이다. 깨끗함(추적 파일에 변경 없음)은 빨리 감기의 조건이고, 손대지 않음은
// worktree를 지워도 잃을 것이 없다는 뜻이다. herdr가 삭제 전에 보는 것도 이쪽이다.
func (r Runner) IsUntouched(ctx context.Context, repo Repo) (bool, error) {
	out, err := r.git(ctx, repo.Root, localTimeout, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "", nil
}
