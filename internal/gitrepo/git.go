// Package gitrepo는 저장소를 찾아내고, 원격 추적 참조를 갱신하고, 앞뒤 커밋 수를 세고,
// 판정(internal/judge)에 쓸 재료를 git에게 묻는다.
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
	"path/filepath"
	"slices"
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
	//
	// --path-format=absolute를 쓰면 간단하지만 그 옵션은 git 2.31에서 생겼다. 우분투 20.04나
	// 데비안 11처럼 오래 쓰이는 배포판은 그보다 낮은 git을 싣고 있고, rev-parse는 모르는 옵션을
	// 오류로 만들지 않고 그대로 되뱉기 때문에 그런 환경에서는 이상한 첫 줄이 섞여 들어온다.
	// 그러면 저장소를 못 찾은 것으로 조용히 넘어가, 플러그인이 아무 일도 하지 않는 이유를
	// 어디에서도 알 수 없게 된다. 그래서 옵션을 쓰지 않고, 상대 경로는 여기서 절대 경로로 맞춘다.
	out, err := r.git(ctx, dir, localTimeout, "rev-parse", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return Repo{}, ErrNotRepository
	}
	lines := splitLines(out)
	// 줄 수를 정확히 둘로 못 박아, 옛 git이 옵션을 되뱉은 경우를 걸러 낸다.
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return Repo{}, ErrNotRepository
	}
	// --git-common-dir는 git을 실행한 자리를 기준으로 한 상대 경로로 나올 수 있다.
	// 중복 제거의 열쇠로 쓰려면 절대 경로여야 한다.
	common := lines[1]
	if !filepath.IsAbs(common) {
		base := dir
		if abs, err := filepath.Abs(dir); err == nil {
			base = abs
		}
		common = filepath.Join(base, common)
	}
	return Repo{Root: lines[0], CommonDir: canonical(common)}, nil
}

// canonical은 같은 자리를 가리키는 경로를 하나의 문자열로 모은다.
//
// 이것이 필요한 이유는 중복 제거 때문이다. 본 저장소에서는 git이 공용 디렉터리를 상대 경로로
// 답하고(그래서 우리가 실행 위치에 붙인다) 연결된 worktree에서는 이미 풀어 놓은 절대 경로로
// 답하는데, 그 둘이 심볼릭 링크를 사이에 두면 다른 문자열이 된다. macOS의 임시 디렉터리처럼
// /var가 /private/var를 가리키는 자리가 흔하다. 문자열이 갈리면 같은 저장소를 두 번 가져간다.
func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// CurrentBranch는 체크아웃된 브랜치 이름을 돌려준다. HEAD가 분리되어 있으면 빈 문자열이고 오류가 아니다.
//
// "분리됨"과 "저장소가 아님"을 가른다. symbolic-ref --quiet는 분리된 HEAD에서 1로, 그 밖의 실패에서
// 128로 끝나므로 종료 코드로 구별한다. upstream이 없어도 브랜치 이름은 필요하다. merged 판정이
// 자기 사본(refs/remotes/<원격>/<브랜치>)을 근거에서 빼려면 이름을 알아야 하기 때문이다.
func (r Runner) CurrentBranch(ctx context.Context, repo Repo) (string, error) {
	res, err := r.gitExit(ctx, repo.Root, localTimeout, nonInteractiveEnv(), "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	switch res.code {
	case 0:
		return firstLine(res.stdout), nil
	case 1:
		return "", nil
	default:
		return "", res.asError([]string{"symbolic-ref"})
	}
}

// Upstream은 현재 브랜치가 따라가는 원격 브랜치를 알아낸다.
func (r Runner) Upstream(ctx context.Context, repo Repo) (Upstream, error) {
	// HEAD가 분리되어 있으면 비교할 브랜치가 없으므로 여기서 끝내는 것이 맞다.
	branch, err := r.CurrentBranch(ctx, repo)
	if err != nil || branch == "" {
		return Upstream{}, ErrNoUpstream
	}
	return r.UpstreamFor(ctx, repo, branch)
}

// UpstreamFor는 지정한 브랜치가 따라가는 원격 브랜치를 알아낸다.
func (r Runner) UpstreamFor(ctx context.Context, repo Repo, branch string) (Upstream, error) {
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
	return r.TrackingRefFor(ctx, repo, remote, remoteRef)
}

// TrackingRefFor는 로컬 브랜치 없이 원격과 원격 참조만으로 추적 참조 이름을 만든다.
//
// 통합 브랜치처럼 로컬에 대응하는 브랜치가 없는 참조에 쓴다. 그런 참조는 @{upstream}으로 물을 수
// 없으므로, 원격에 설정된 fetch 참조 사양에 대입하고 그것도 없으면 관례를 따른다.
func (r Runner) TrackingRefFor(ctx context.Context, repo Repo, remote, remoteRef string) string {
	if ref := r.trackingRefFromRefspec(ctx, repo, remote, remoteRef); ref != "" {
		return ref
	}
	return "refs/remotes/" + remote + "/" + strings.TrimPrefix(remoteRef, "refs/heads/")
}

// TrackingRefsFor는 같은 원격 참조를 비추는 목적지를 모두 돌려준다. 한 출발지를 여러 목적지에
// 가져오는 참조 사양에서는 전부 자기 사본이므로 merged 판정에서 빠져야 한다. 일치하는 사양이
// 없으면 TrackingRefFor와 같은 관례를 쓴다. 결과는 항상 하나 이상이며 설정 순서를 따른다.
func (r Runner) TrackingRefsFor(ctx context.Context, repo Repo, remote, remoteRef string) []string {
	var refs []string
	for _, spec := range r.fetchRefspecs(ctx, repo, remote) {
		if ref, ok := substituteRefspec(spec.source, spec.destination, remoteRef); ok && !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		refs = append(refs, "refs/remotes/"+remote+"/"+strings.TrimPrefix(remoteRef, "refs/heads/"))
	}
	return refs
}

// RemoteRefFor는 추적 참조 이름에서 원격 쪽 참조 이름을 거꾸로 알아낸다.
//
// refs/remotes/<원격>/HEAD가 가리키는 것은 추적 참조인데, fetch 하려면 원격 쪽 이름이 있어야 한다.
// TrackingRefFor와 같은 참조 사양을 거꾸로 대입하고, 그것도 없으면 관례를 거꾸로 적용한다.
// 어느 쪽으로도 짝을 못 찾으면 빈 문자열이다. 그때는 fetch 만 못 할 뿐 로컬 판정에는 쓸 수 있다.
func (r Runner) RemoteRefFor(ctx context.Context, repo Repo, remote, trackingRef string) string {
	if ref := r.remoteRefFromRefspec(ctx, repo, remote, trackingRef); ref != "" {
		return ref
	}
	if rest, ok := strings.CutPrefix(trackingRef, "refs/remotes/"+remote+"/"); ok && rest != "" {
		return "refs/heads/" + rest
	}
	return ""
}

// remoteRefFromRefspec은 trackingRefFromRefspec의 역방향이다. 목적지에 추적 참조를 맞춰 보고 출발지를 만든다.
func (r Runner) remoteRefFromRefspec(ctx context.Context, repo Repo, remote, trackingRef string) string {
	for _, spec := range r.fetchRefspecs(ctx, repo, remote) {
		if ref, ok := substituteRefspec(spec.destination, spec.source, trackingRef); ok {
			return ref
		}
	}
	return ""
}

// trackingRefFromRefspec은 원격에 설정된 fetch 참조 사양에 remoteRef를 대입해 첫 목적지를 만든다.
func (r Runner) trackingRefFromRefspec(ctx context.Context, repo Repo, remote, remoteRef string) string {
	for _, spec := range r.fetchRefspecs(ctx, repo, remote) {
		if ref, ok := substituteRefspec(spec.source, spec.destination, remoteRef); ok {
			return ref
		}
	}
	return ""
}

// refspec은 fetch 참조 사양 하나다. 출발지는 원격 쪽 이름, 목적지는 로컬에 비출 이름이다.
type refspec struct {
	source      string
	destination string
}

// fetchRefspecs는 원격에 설정된 fetch 참조 사양들을 읽는다.
//
// 참조 사양을 읽고 해석하는 규칙은 여기 한 곳에만 둔다. 정방향(원격 참조 → 추적 참조)과 역방향이
// 각자 읽으면 규칙을 손볼 때 한쪽만 고쳐져 두 방향이 어긋난다. 앞의 "+"는 강제 갱신 표시라 뗀다.
// ":"가 없거나 한쪽이 빈 사양(부정 사양 "^..." 포함)은 짝지을 수 없으므로 뺀다.
func (r Runner) fetchRefspecs(ctx context.Context, repo Repo, remote string) []refspec {
	out, err := r.git(ctx, repo.Root, localTimeout, "config", "--get-all", "remote."+remote+".fetch")
	if err != nil {
		return nil
	}
	var specs []refspec
	for _, line := range splitLines(out) {
		source, destination, found := strings.Cut(strings.TrimPrefix(line, "+"), ":")
		if !found || source == "" || destination == "" {
			continue
		}
		specs = append(specs, refspec{source: source, destination: destination})
	}
	return specs
}

// substituteRefspec은 ref를 pattern에 맞춰 보고, 맞으면 template의 별표 자리에 대입한 이름을 돌려준다.
//
// 별표가 없는 pattern은 한 참조만 짝지으므로 template을 그대로 돌려준다. pattern에는 별표가 있는데
// template에는 없으면 대입할 자리가 없으니 짝이 아니다.
func substituteRefspec(pattern, template, ref string) (string, bool) {
	middle, ok := matchRefspecSide(pattern, ref)
	if !ok {
		return "", false
	}
	if !strings.Contains(pattern, "*") {
		return template, true
	}
	if !strings.Contains(template, "*") {
		return "", false
	}
	return strings.Replace(template, "*", middle, 1), true
}

// matchRefspecSide는 참조 사양의 한쪽(출발지나 목적지)에 ref를 맞춰 보고, 별표 자리에 든 부분을 돌려준다.
// 별표가 없으면 정확히 같아야 한다. 앞뒤가 겹칠 만큼 짧은 ref는 맞지 않는 것으로 본다.
func matchRefspecSide(pattern, ref string) (middle string, ok bool) {
	if !strings.Contains(pattern, "*") {
		return "", pattern == ref
	}
	prefix, suffix, _ := strings.Cut(pattern, "*")
	if len(ref) < len(prefix)+len(suffix) || !strings.HasPrefix(ref, prefix) || !strings.HasSuffix(ref, suffix) {
		return "", false
	}
	return ref[len(prefix) : len(ref)-len(suffix)], true
}

// CountsFor는 지금 체크아웃된 HEAD와 추적 참조 사이의 앞뒤 커밋 수를 센다.
// 추적 참조가 아직 로컬에 없으면(한 번도 fetch하지 않은 브랜치) 셀 수 없으므로 오류를 돌려준다.
func (r Runner) CountsFor(ctx context.Context, repo Repo, up Upstream) (Counts, error) {
	return r.CountsBetween(ctx, repo, "HEAD", up.TrackingRef)
}

// CountsBetween은 주어진 커밋과 추적 참조 사이의 앞뒤 커밋 수를 센다.
//
// 데몬은 "HEAD"라는 이름 대신 회차 첫머리에 읽어 둔 커밋을 넘긴다. 한 회차는 fetch 제한 시간만큼
// 길어질 수 있어 그 사이 사용자가 브랜치를 바꾸는 일이 드물지 않은데, 이름으로 세면 뒤처짐은 새 HEAD를,
// 같은 회차의 merged와 catchup은 옛 HEAD를 말해 한 행의 토큰들이 서로 다른 커밋 이야기를 하게 된다.
func (r Runner) CountsBetween(ctx context.Context, repo Repo, head, trackingRef string) (Counts, error) {
	out, err := r.gitLine(ctx, repo.Root, "rev-list", "--left-right", "--count", head+"..."+trackingRef)
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

// HeadCommit은 지금 체크아웃된 커밋의 전체 해시를 돌려준다.
func (r Runner) HeadCommit(ctx context.Context, repo Repo) (string, error) {
	return r.gitLine(ctx, repo.Root, "rev-parse", "HEAD")
}

// BaseUpstream은 이 worktree가 갈라져 나온 브랜치의 원격 추적 대상을 짐작한다.
//
// 새 worktree의 브랜치는 원본 체크아웃의 HEAD에서 방금 만들어졌으므로, 아직 그 기준 커밋에 그대로
// 머물러 있다. 그래서 같은 커밋을 가리키면서 upstream이 설정된 로컬 브랜치를 찾으면 그것이 기준이다.
//
// 후보가 여럿이면 아무것도 고르지 않는다. 어느 쪽이 기준이었는지 알 수 없는데 하나를 골라 작업
// 트리를 옮기면, 사용자가 의도하지 않은 브랜치 위에서 일하게 된다. 짐작해서 옮기느니 그대로 두는 편이 낫다.
func (r Runner) BaseUpstream(ctx context.Context, repo Repo, exclude string) (Upstream, error) {
	head, err := r.HeadCommit(ctx, repo)
	if err != nil || head == "" {
		return Upstream{}, ErrNoUpstream
	}
	out, err := r.git(ctx, repo.Root, localTimeout,
		"for-each-ref", "--format=%(objectname)\t%(refname:short)\t%(upstream)", "refs/heads/")
	if err != nil {
		return Upstream{}, ErrNoUpstream
	}

	candidates := map[string]string{} // 추적 참조 -> 로컬 브랜치
	for _, line := range splitLines(out) {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		commit, branch, tracking := fields[0], fields[1], fields[2]
		if commit != head || tracking == "" || branch == exclude {
			continue
		}
		candidates[tracking] = branch
	}
	if len(candidates) != 1 {
		return Upstream{}, ErrNoUpstream
	}
	for _, branch := range candidates {
		return r.UpstreamFor(ctx, repo, branch)
	}
	return Upstream{}, ErrNoUpstream
}

// IsClean은 작업 트리에 손댄 것이 없는지 답한다.
func (r Runner) IsClean(ctx context.Context, repo Repo) (bool, error) {
	out, err := r.git(ctx, repo.Root, localTimeout, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "", nil
}

// FastForward는 현재 브랜치를 추적 참조까지 앞으로만 옮긴다.
//
// --ff-only 여야 한다. 이 명령은 사용자가 만들어 둔 것을 절대 버리지 않아야 하는데, 빨리 감기는
// 정의상 잃을 것이 없는 이동이기 때문이다. 브랜치가 이미 갈라졌다면 명령이 실패하고, 그때는
// 아무것도 하지 않는 것이 옳은 결과다.
func (r Runner) FastForward(ctx context.Context, repo Repo, trackingRef string) error {
	_, err := r.git(ctx, repo.Root, localTimeout,
		"merge", "--ff-only", "--no-autostash", "--quiet", "--", trackingRef)
	return err
}

// Fetch는 현재 브랜치의 원격 추적 참조 하나만 갱신한다.
//
// 좁게 가져오는 것이 핵심이다. 태그와 다른 브랜치까지 끌어오면 이 주기로 반복하기에는 비용이 크다.
// 나머지 옵션과 그 이유는 fetchArgs 에 있다.
//
// 참조 사양 앞의 "+"는 강제 갱신을 뜻한다. 원격 추적 참조는 원격을 그대로 비추는 자리이고 git 자신의
// 기본 참조 사양도 강제이므로, 되감기 push가 일어난 뒤에도 숫자가 사실과 어긋나지 않게 하려면 필요하다.
func (r Runner) Fetch(ctx context.Context, repo Repo, up Upstream) error {
	refspec := "+" + up.RemoteRef + ":" + up.TrackingRef
	_, err := r.gitWithEnv(ctx, repo.Root, r.fetchTimeout(), r.fetchEnv(ctx, repo), fetchArgs(up.Remote, refspec)...)
	return err
}

// FetchAll은 원격의 브랜치 전부를 기본 참조 사양으로 가져온다. worktree 화면이 열릴 때 한 번 부른다.
//
// Fetch 가 참조 하나씩 좁게 가져오는 것과 반대다. 데몬은 열린 워크스페이스만 돌기 때문에 나머지
// worktree 의 추적 참조는 낡아 있는데, 화면은 그 전부를 한눈에 견주는 자리라 왕복 한 번으로 모두
// 갱신하는 편이 참조 수십 개를 하나씩 가져오는 것보다 싸다. 옵션은 fetchArgs 가 Fetch 와 같게 만든다.
// 특히 prune 은 하지 않는다. 사라진 브랜치의 판정은 RemoteHeads 가 맡고, 사용자의 참조는 건드리지 않는다.
//
// 제한 시간은 좁은 fetch 의 세 배다. 브랜치가 수백 개인 저장소에서 한 번의 fetch 가 좁은 fetch 보다
// 오래 걸리는 것은 당연한데, 같은 제한 시간을 두면 큰 저장소에서만 화면이 늘 "fetch failed" 가 된다.
func (r Runner) FetchAll(ctx context.Context, repo Repo, remote string) error {
	if remote == "" {
		return fmt.Errorf("원격 이름이 비어 있다")
	}
	_, err := r.gitWithEnv(ctx, repo.Root, fetchAllTimeoutFactor*r.fetchTimeout(), r.fetchEnv(ctx, repo), fetchArgs(remote)...)
	return err
}

// fetchAllTimeoutFactor는 전체 fetch 의 제한 시간이 좁은 fetch 의 몇 배인지다.
const fetchAllTimeoutFactor = 3

// fetchArgs는 fetch 명령의 인자를 만든다. 데몬의 좁은 fetch(Fetch)와 화면의 전체 fetch(FetchAll)가 함께 쓴다.
//
// 한곳에 두는 이유는 두 fetch 가 같은 약속을 지켜야 하기 때문이다. 목록을 두 곳에 따로 적으면 한쪽만 고쳐져
// 데몬과 화면이 서로 다른 약속(prune, FETCH_HEAD)을 갖게 되고, 그 차이는 시험이 아니라 사용자가 먼저 알아챈다.
//
// prune은 하지 않는다. 남의 작업 중인 참조를 지울 수 있다. 자동 정리(gc)도 꺼 둔다. 사용자가 모르는 사이에
// 도는 작업이 무거운 정리 작업을 반복해서 깨우면 곤란하기 때문이다. 태그와 하위 모듈도 가져오지 않는다.
// 이 플러그인이 견주는 것은 브랜치의 추적 참조뿐이다.
//
// --no-write-fetch-head는 사용자의 작업을 건드리지 않기 위한 것이다. 이것이 없으면 배경에서 도는
// 이 fetch가 FETCH_HEAD를 다시 쓰는데, 사용자가 방금 손으로 fetch한 뒤 `git merge FETCH_HEAD`를
// 하려던 참이었다면 엉뚱한 커밋을 병합하게 된다.
//
// refspecs 가 비어 있으면 원격에 설정된 기본 참조 사양(remote.<이름>.fetch)으로 가져온다.
func fetchArgs(remote string, refspecs ...string) []string {
	args := []string{
		"-c", "gc.auto=0",
		"fetch", "--quiet", "--no-tags", "--no-prune", "--no-prune-tags",
		"--no-recurse-submodules", "--no-write-fetch-head",
		"--", remote,
	}
	return append(args, refspecs...)
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

// gitWithEnv는 환경을 지정해 git 명령을 실행한다. 종료 코드가 0이 아니면 오류다.
func (r Runner) gitWithEnv(ctx context.Context, dir string, timeout time.Duration, env []string, args ...string) ([]byte, error) {
	res, err := r.gitExit(ctx, dir, timeout, env, args...)
	if err != nil {
		return nil, err
	}
	if res.code != 0 {
		return nil, res.asError(args)
	}
	return res.stdout, nil
}

// gitResult는 끝까지 돈 git 명령의 결과다.
type gitResult struct {
	stdout []byte
	stderr string
	code   int
	// exit는 종료 코드가 0이 아닐 때의 원래 오류다. 메시지에 "exit status N"을 그대로 남기기 위해 둔다.
	exit error
}

// asError는 0이 아닌 종료 코드를 지금까지 쓰던 것과 같은 모양의 오류로 만든다.
// IsMissingRemoteRef가 이 문구 안의 stderr를 보고 판단하므로 모양을 바꾸면 안 된다.
func (res gitResult) asError(args []string) error {
	name := subcommand(args)
	if res.stderr != "" {
		return fmt.Errorf("git %s: %w: %s", name, res.exit, res.stderr)
	}
	return fmt.Errorf("git %s: %w", name, res.exit)
}

// gitExit는 git 명령을 끝까지 돌리고 종료 코드까지 돌려준다.
//
// 종료 코드가 뜻을 갖는 명령이 있어서 따로 둔다. merge-tree --write-tree는 1로 "충돌"을 알리고,
// config --get-all은 1로 "그런 키가 없다"를 알린다. 둘 다 실패가 아니라 답이다. 오류는 명령을 띄우지
// 못했거나, 제한 시간이나 취소로 끝까지 돌지 못했을 때만 돌려준다.
func (r Runner) gitExit(ctx context.Context, dir string, timeout time.Duration, env []string, args ...string) (gitResult, error) {
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

	err := cmd.Run()
	res := gitResult{stdout: stdout.Bytes(), stderr: strings.TrimSpace(stderr.String())}
	if err == nil {
		return res, nil
	}
	name := subcommand(args)
	// 컨텍스트가 끝났으면 제한 시간이든 취소든 언제나 실패다. 우리가 죽인 프로세스의 종료 코드를
	// 답으로 읽으면 안 된다. 유닉스에서는 신호로 죽어 -1이라 아래에서 걸러지지만, 윈도우는
	// TerminateProcess가 종료 코드 1을 남기므로 데몬이 꺼지는 순간 config --get-all은 "키 없음"으로,
	// merge-tree는 "충돌"로 읽힌다.
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return gitResult{}, fmt.Errorf("git %s: 제한 시간 초과", name)
		}
		return gitResult{}, fmt.Errorf("git %s: 취소됨", name)
	}
	// 신호로 죽은 경우는 종료 코드가 -1이다. 그것은 답이 아니라 실패다.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		res.code = exitErr.ExitCode()
		res.exit = err
		return res, nil
	}
	if res.stderr != "" {
		return gitResult{}, fmt.Errorf("git %s: %w: %s", name, err, res.stderr)
	}
	return gitResult{}, fmt.Errorf("git %s: %w", name, err)
}

// nonInteractiveEnv는 git이 사람에게 무언가를 물어보다 멈추는 일이 없도록 환경을 다듬는다.
// 배경에서 도는 작업이 자격 증명 프롬프트를 띄우면 제한 시간까지 그대로 매달려 있게 된다.
//
// ssh 설정은 여기서 건드리지 않는다. 사용자가 정해 둔 것을 덮을 위험이 있어 fetchEnv가 따로 판단한다.
//
// 같은 이름이 겹치면 exec는 뒤에 오는 값을 쓴다. 그래서 물려받은 환경 뒤에 덧붙이는 것으로 덮어쓴다.
func nonInteractiveEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		// 대화형 자격 증명 관리자도 막는다.
		"GCM_INTERACTIVE=never",
		// 인덱스 같은 부가적인 잠금을 잡지 않아, 사용자가 동시에 쓰는 git과 부딪히지 않는다.
		"GIT_OPTIONAL_LOCKS=0",
		// git의 문구를 영어로 고정한다. 이 플러그인이 git 출력을 문구로 판단하는 자리는
		// IsMissingRemoteRef 하나뿐인데, 그것이 gone 판정의 유일한 근거다. 데몬이 물려받은 로캘이
		// 독일어나 프랑스어면 git이 번역된 문구를 내어 지워진 브랜치를 알아보지 못한다. 사용자가
		// LC_ALL을 이미 두었을 수 있으므로 LC_MESSAGES만으로는 부족하다. 나머지 출력은 해시와 참조
		// 이름이라 로캘을 고정해도 해석이 달라지지 않는다. git 자신의 시험 묶음도 이렇게 한다.
		"LC_ALL=C",
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
