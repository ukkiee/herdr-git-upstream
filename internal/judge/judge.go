// Package judge는 "이 브랜치는 원격에서 끝난 작업인가"와 "따라잡으면 충돌하나"에 답하는 규칙을 둔다.
//
// git 호출은 gitrepo.Runner를 통해서만 하고, 여기에는 규칙만 둔다. 사이드바 토큰과 worktree 화면이
// 같은 답을 내야 하므로, 판정이 한곳에 있어야 두 자리가 어긋나지 않는다.
//
// 세 판정의 성격이 서로 다르다. gone은 fetch 기록만 보므로 값이 0이다. merged는 로컬 명령 몇 개로
// "놓칠 수는 있어도 틀리지는 않는" 답을 낸다. 이유는 ADR 0001(docs/adr/0001-merged-judgement.md)에 있다.
// catchup은 merge-tree가 값이 들어 결과를 기록에 캐시한다.
package judge

import (
	"context"
	"errors"
	"slices"
	"time"

	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/state"
)

// Gone은 upstream 브랜치가 원격에서 사라졌는지 답한다.
//
// 좁은 fetch가 "원격 참조 없음"으로 영구 실패했으면 사라진 것이다. ls-remote도 prune도 필요 없다.
// 지금 코드에서 LastErrorPermanent를 참으로 적는 곳은 daemon.fetchOne 하나뿐이고, 그것도
// gitrepo.IsMissingRemoteRef일 때만이다. 그래서 그 값이 곧 gone이다. 다른 종류의 영구 실패가
// 생기면 이 결합은 풀어야 하며, 그때는 기록에 이유를 따로 적어야 한다.
//
// 근거를 더 요구하지 않는다. 원격에서 지워지고 로컬에서 prune까지 된 뒤 데몬이 처음 본 브랜치도
// 이 기록 하나로 gone이어야 한다. gone은 끝난 작업의 주된 신호라 좁히면 잃는 것이 크다. 빈 원격을
// clone한 직후처럼 태어나지 않은 브랜치가 같은 실패를 내는 경우는 커밋이 하나도 없는 저장소이므로,
// 호출자가 HEAD가 있을 때만 이 답을 쓴다.
func Gone(record state.Record) bool {
	return record.LastErrorPermanent
}

// Merged는 merged 판정의 결과다.
type Merged struct {
	Yes bool
	// By는 근거다. "ancestor:refs/remotes/origin/dev"는 조상 검사로, "merge-tree:refs/remotes/origin/main"은
	// 병합해 봐도 아무것도 바뀌지 않아서 참이 되었다는 뜻이다. 로그와 화면에서 이유를 보여 주기 위한 것이다.
	By string
}

// Self는 판정 대상 브랜치 자신을 가리키는 원격 참조들이다. 자기 자신은 병합의 근거가 될 수 없다.
type Self struct {
	// Copies는 원격마다 있는 이 브랜치의 사본(refs/remotes/<원격>/<브랜치>)이다. push만 해도 생기므로,
	// 이것이 HEAD를 품는 것은 "올렸다"는 뜻이지 끝난 작업이라는 뜻이 아니다. 분리된 HEAD는 사본이 없다.
	Copies []string
	// Tracking은 upstream 추적 참조다. 자기 사본과 같을 수도(main → origin/main), 다른 브랜치일 수도
	// (feature → origin/main) 있다. 어느 쪽이든 조상 검사의 근거로는 삼지 않는다. 거기서 갈라져 나왔을
	// 뿐인 새 브랜치도 그 참조에 들어 있기 때문이다. 통합 브랜치라면 merge-tree 비교가 따로 본다.
	Tracking string
}

// SelfRefs는 판정 대상 브랜치 자신을 가리키는 원격 참조들을 모은다.
//
// 등록된 원격마다 이 브랜치 이름의 추적 참조를 만든다. upstream 없이 다른 원격(fork)에만 push한
// 브랜치도 그 사본이 HEAD를 품으므로, upstream의 원격 하나만 봐서는 부족하다. upstream이 이 브랜치의
// 자기 사본이면(원격 쪽 이름이 같으면) 그 추적 참조도 사본에 넣는다. 참조 사양이 별나서 이름이
// 계산과 다르게 잡힌 경우를 위한 것이다.
func SelfRefs(ctx context.Context, git gitrepo.Runner, repo gitrepo.Repo, branch string, upstream *gitrepo.Upstream) Self {
	var self Self
	if upstream != nil {
		self.Tracking = upstream.TrackingRef
	}
	if branch == "" {
		return self
	}
	remoteRef := "refs/heads/" + branch
	if remotes, err := git.Remotes(ctx, repo); err == nil {
		for _, remote := range remotes {
			self.Copies = append(self.Copies, git.TrackingRefFor(ctx, repo, remote, remoteRef))
		}
	}
	if upstream != nil && upstream.RemoteRef == remoteRef && upstream.TrackingRef != "" && !slices.Contains(self.Copies, upstream.TrackingRef) {
		self.Copies = append(self.Copies, upstream.TrackingRef)
	}
	return self
}

// JudgeMerged는 HEAD의 내용이 이미 어느 원격 브랜치에 들어가 있는지 판정한다.
//
// 두 단계다. 먼저 값이 싼 조상 검사를 모든 원격 브랜치의 추적 참조에 대고, 자기 참조가 아닌 것이
// HEAD를 품고 있으면 참이다. 아니면 통합 브랜치(targets) 각각에 병합해 보고 결과 트리가 통합 브랜치의
// 트리와 같으면(병합해도 아무것도 바뀌지 않으면) 참이다. squash 병합과 rebase 병합은 조상 관계를
// 남기지 않으므로 두 번째 단계가 없으면 사실상 아무것도 잡지 못한다.
//
// 자기 참조(self)는 근거에서 뺀다. 자기 사본에 자기 커밋이 들어 있는 것은 push 했다는 뜻이지 끝난
// 작업이라는 뜻이 아니다. 그리고 자기 사본이 통합 브랜치 가운데 하나이면 이 체크아웃은 통합 브랜치
// 자체(main 위의 main, dev 위의 dev)이므로 아예 판정하지 않는다. 통합 브랜치 체크아웃은 끝난 작업일 수
// 없는데, main에서 갈라져 나간 브랜치가 원격에 하나라도 있으면 그 참조가 HEAD를 품어 조상 검사에
// 걸리기 때문이다.
//
// 조상 검사에는 한계가 있다. "HEAD에서 갈라져 나간 브랜치"와 "HEAD가 병합된 브랜치"를 구별하지 못한다.
// 내 브랜치에서 남이 갈라져 나가 push하면 내 브랜치에 merged가 붙는다. 그 참조가 HEAD를 품는 것은
// 사실이므로 "틀리지는 않는" 범위 안이지만, 사람이 보기에는 이른 신호다.
//
// 결과를 캐시하지 않는다. 같은 두 커밋을 다시 병합하면 같은 트리 객체가 나와 객체 저장소가 자라지
// 않고, 조상 검사는 로컬 명령 하나라 값이 싸다.
func JudgeMerged(ctx context.Context, git gitrepo.Runner, repo gitrepo.Repo, head string, self Self, targets []string) (Merged, error) {
	for _, own := range self.Copies {
		if slices.Contains(targets, own) {
			return Merged{}, nil
		}
	}

	refs, err := git.ContainingRemoteRefs(ctx, repo, head)
	if err != nil {
		return Merged{}, err
	}
	for _, ref := range refs {
		if ref == self.Tracking || slices.Contains(self.Copies, ref) {
			continue
		}
		return Merged{Yes: true, By: "ancestor:" + ref}, nil
	}
	if len(targets) == 0 || !git.SupportsMergeTree(ctx) {
		return Merged{}, nil
	}

	// 통합 브랜치 하나에서 실패해도 나머지는 본다. merged는 놓쳐도 되는 신호라, 하나가 막혔다고
	// 전체를 포기하면 잡을 수 있던 것까지 놓친다. 아무것도 못 잡았을 때만, 실패한 통합 브랜치의
	// 오류를 모두 합쳐 돌려준다.
	var failed error
	for _, target := range targets {
		// 아직 한 번도 가져오지 않은 통합 브랜치는 로컬에 없다. 다음 회차의 fetch 뒤에 잡힌다.
		if _, err := git.CommitOf(ctx, repo, target); err != nil {
			continue
		}
		tree, conflict, err := git.MergeTree(ctx, repo, target, head)
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		if conflict {
			continue
		}
		targetTree, err := git.TreeOf(ctx, repo, target)
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		if tree == targetTree {
			return Merged{Yes: true, By: "merge-tree:" + target}, nil
		}
	}
	return Merged{}, failed
}

// Catchup은 따라잡을 때 충돌하는지의 판정이다.
type Catchup int

const (
	// CatchupUnknown은 판정하지 못했다는 뜻이다. git이 merge-tree를 지원하지 않거나 명령이 실패한 경우다.
	CatchupUnknown Catchup = iota
	// CatchupClean은 충돌 없이 따라잡을 수 있다.
	CatchupClean
	// CatchupConflict는 따라잡으면 충돌한다.
	CatchupConflict
)

// String은 기록에 적는 이름이다. 사람이 파일을 열어 봐도 읽히도록 숫자가 아니라 낱말로 둔다.
func (c Catchup) String() string {
	switch c {
	case CatchupClean:
		return "clean"
	case CatchupConflict:
		return "conflict"
	default:
		return "unknown"
	}
}

// parseCatchup은 기록에 적힌 이름을 되읽는다. 모르는 값이면 판정한 적 없는 것으로 본다.
// "unknown"도 유효한 기록이다. 판정하지 못했다는 사실 자체를 남겨야 같은 쌍에 다시 값을 치르지 않는다.
func parseCatchup(text string) (Catchup, bool) {
	switch text {
	case "clean":
		return CatchupClean, true
	case "conflict":
		return CatchupConflict, true
	case "unknown":
		return CatchupUnknown, true
	default:
		return CatchupUnknown, false
	}
}

// catchupRetry는 판정하지 못한 쌍을 다시 시도하기까지의 간격이다.
//
// clean과 conflict는 두 커밋만으로 정해지므로 쌍이 같으면 영원히 같지만, unknown은 그렇지 않다.
// 관계없는 역사처럼 다시 해도 같은 것도 있고, 제한 시간 초과처럼 그때 시스템이 바빴을 뿐인 것도
// 있는데 기록만 봐서는 가릴 수 없다. 그래서 unknown은 한동안만 쉰다. 뒤처져 있는 동안 회차마다(60초)
// 5초짜리 프로세스를 띄웠다 죽이는 일은 막으면서, 바빴을 뿐이라면 한 시간 뒤에는 답을 얻는다.
const catchupRetry = time.Hour

// JudgeCatchup은 HEAD를 추적 참조까지 끌어올릴 때 충돌하는지 판정한다.
//
// 기록의 (Head, Tracking)이 지금의 (head, trackingCommit)과 같으면 저장된 답을 그대로 쓴다. 판정은
// 두 커밋만으로 정해지므로 쌍이 같으면 답도 같다. 저장된 답이 unknown이면 catchupRetry 안에서만
// 그대로 쓴다. 아니면 merge-tree로 판정하고 기록에 쌍과 답과 시각을 적어 돌려준다. 판정하지 못했어도
// 적는다. 호출자가 그 기록을 저장해야 다음 회차가 캐시를 본다.
//
// 뒤처짐이 0보다 클 때만 부른다. 뒤처지지 않았으면 따라잡을 것이 없다.
func JudgeCatchup(ctx context.Context, git gitrepo.Runner, repo gitrepo.Repo, head, trackingCommit string, record state.CatchupRecord, now time.Time) (Catchup, state.CatchupRecord, error) {
	if record.Head == head && record.Tracking == trackingCommit {
		if cached, ok := parseCatchup(record.Result); ok {
			if cached != CatchupUnknown || now.Sub(time.Unix(record.CheckedUnix, 0)) < catchupRetry {
				return cached, record, nil
			}
		}
	}
	if !git.SupportsMergeTree(ctx) {
		return CatchupUnknown, record, nil
	}
	result := CatchupUnknown
	_, conflict, err := git.MergeTree(ctx, repo, trackingCommit, head)
	switch {
	case err != nil:
		// 아래에서 unknown으로 남긴다. 오류는 호출자가 로그에 적는다.
	case conflict:
		result = CatchupConflict
	default:
		result = CatchupClean
	}
	record.Head = head
	record.Tracking = trackingCommit
	record.Result = result.String()
	record.CheckedUnix = now.Unix()
	return result, record, err
}

// defaultBranchRecheck는 원격 기본 브랜치를 원격에 다시 물어보기까지의 간격이다.
// 기본 브랜치는 거의 바뀌지 않고, 물어보는 데 왕복 한 번이 든다.
const defaultBranchRecheck = 24 * time.Hour

// Integration은 저장소의 통합 브랜치 목록과, 그 목록이 어디까지 믿을 만한지다.
type Integration struct {
	// Branches는 원격 기본 브랜치(알아냈으면 맨 앞)와 mergeTarget을 합쳐 중복을 없앤 목록이다.
	Branches []gitrepo.Upstream
	// DefaultKnown이 거짓이면 원격 기본 브랜치를 로컬에서도 기록에서도 알지 못해 Branches에 없다.
	// 그때는 merged를 판정하지 않는다. 통합 브랜치 자체를 체크아웃한 자리(main 위의 main)를 가려낼 수
	// 없어서, main에서 갈라져 나간 원격 브랜치 하나만 있어도 조상 검사가 그 자리에 merged를 붙인다.
	DefaultKnown bool
	// LookupDue는 원격 기본 브랜치를 원격에 물을 때라는 뜻이다. 모르는 채이고, 마지막으로 물어본 지
	// 하루가 지났다. 묻는 일은 네트워크를 타므로 LookupDefaultBranch가 fetch와 같은 자리에서 한다.
	LookupDue bool
}

// IntegrationBranches는 저장소의 통합 브랜치 목록을 돌려준다.
//
// 원격 기본 브랜치 하나(알아낼 수 있으면)와 `git config git-upstream.mergeTarget`의 값들을 합쳐
// 중복을 없앤 것이다. 원격 기본 브랜치는 로컬에서만 찾는다. refs/remotes/<원격>/HEAD가 실제 참조를
// 가리키면 그것, 아니면 저장소 기록에 남긴 값이다. 둘 다 없으면 기본 브랜치 없이 지정된 통합 브랜치만
// 돌려주고, 원격에 묻는 일은 LookupDefaultBranch가 fetch와 같은 자리에서 한다. 네트워크를 타는 일이
// 워크스페이스를 훑는 흐름 안에 있으면, 닿지 않는 원격 하나가 제한 시간만큼 그 회차의 토큰 보고 전체를 막는다.
//
// "로컬에서 아는가"와 "원격에 물을 때인가"를 한 번에 답한다. 두 물음은 같은 재료(origin/HEAD와 저장소
// 기록)로 답하므로 따로 두면 호출자가 짝지어 불러야 한 답이 되고, 같은 git 명령이 두 번 돈다.
//
// mergeTarget 가운데 풀지 못한 것은 건너뛰고 나머지를 돌려주되, 오류도 함께 돌려준다. 오타 하나가
// 멀쩡한 나머지 판정까지 막아서는 안 되지만, 그 사실이 어디에도 남지 않아서도 안 된다.
func IntegrationBranches(ctx context.Context, git gitrepo.Runner, repo gitrepo.Repo, remote string, store state.Store, now time.Time) (Integration, error) {
	var result Integration
	seen := map[string]bool{}
	add := func(up gitrepo.Upstream) {
		if up.TrackingRef == "" || seen[up.TrackingRef] {
			return
		}
		seen[up.TrackingRef] = true
		result.Branches = append(result.Branches, up)
	}

	if up, ok := localDefaultBranch(ctx, git, repo, remote); ok {
		add(up)
		result.DefaultKnown = true
	} else {
		record := store.LoadRepoRecord(repoKey(repo, remote))
		if record.DefaultTrackingRef != "" {
			add(gitrepo.Upstream{
				Remote:      remote,
				RemoteRef:   record.DefaultRemoteRef,
				TrackingRef: record.DefaultTrackingRef,
			})
			result.DefaultKnown = true
		} else {
			result.LookupDue = record.LookupDue(now, defaultBranchRecheck)
		}
	}

	targets, err := git.MergeTargets(ctx, repo)
	if err != nil {
		return result, err
	}
	var skipped error
	for _, short := range targets {
		up, err := git.TargetFromShortRef(ctx, repo, short)
		if err != nil {
			skipped = errors.Join(skipped, err)
			continue
		}
		add(up)
	}
	return result, skipped
}

// repoKey는 저장소 기록의 열쇠다. 연결된 worktree들은 CommonDir를 나눠 쓰므로 기록도 나눠 쓴다.
func repoKey(repo gitrepo.Repo, remote string) string {
	return state.Key(repo.CommonDir + "\x00" + remote)
}

// localDefaultBranch는 refs/remotes/<원격>/HEAD가 가리키는 원격 기본 브랜치를 돌려준다.
//
// 가리키는 참조가 실제로 있어야 안다고 답한다. 원격이 기본 브랜치를 바꾼 뒤(master → main)에도
// origin/HEAD는 남는데, `git fetch --prune`은 추적 참조는 지우면서 이 별명은 그대로 두어 없는 참조를
// 가리키는 채가 된다. 그것을 믿으면 없는 브랜치를 회차마다 가져오다 영구 실패만 쌓고, 진짜 기본
// 브랜치는 영영 묻지 않는다. 모른다고 답해야 기록으로, 그다음 원격으로 넘어간다.
func localDefaultBranch(ctx context.Context, git gitrepo.Runner, repo gitrepo.Repo, remote string) (gitrepo.Upstream, bool) {
	tracking, err := git.RemoteHead(ctx, repo, remote)
	if err != nil {
		return gitrepo.Upstream{}, false
	}
	if _, err := git.CommitOf(ctx, repo, tracking); err != nil {
		return gitrepo.Upstream{}, false
	}
	return gitrepo.Upstream{
		Remote:      remote,
		RemoteRef:   git.RemoteRefFor(ctx, repo, remote, tracking),
		TrackingRef: tracking,
	}, true
}

// LookupDefaultBranch는 원격에 기본 브랜치를 물어 저장소 기록에 남긴다. 네트워크를 탄다.
//
// fetch와 같은 자리(병렬 상한과 스로틀 안)에서 부른다. 여기서 알아낸 답은 IntegrationBranches가
// 기록에서 읽는다. 호출자가 fetch 뒤에 IntegrationBranches를 다시 부르면 같은 회차에 쓸 수 있다.
// 시도한 시각은 결과와 무관하게 먼저 남긴다.
func LookupDefaultBranch(ctx context.Context, git gitrepo.Runner, repo gitrepo.Repo, remote string, store state.Store, now time.Time) error {
	key := repoKey(repo, remote)
	record := store.LoadRepoRecord(key)
	record.DefaultCheckedUnix = now.Unix()
	remoteRef, err := git.RemoteHeadFromRemote(ctx, repo, remote)
	if err == nil {
		record.DefaultRemoteRef = remoteRef
		record.DefaultTrackingRef = git.TrackingRefFor(ctx, repo, remote, remoteRef)
	}
	// 기록을 남기지 못하면 다음 회차가 한 번 더 물을 뿐이다. 물어본 결과의 오류가 더 중요하다.
	if saveErr := store.SaveRepoRecord(key, record); saveErr != nil && err == nil {
		return saveErr
	}
	return err
}

// RemoteFor는 판정에 쓸 원격을 고른다.
//
// upstream이 있으면 그 원격이다. 없으면(아직 push 하지 않은 새 브랜치) 원격이 하나뿐일 때 그것,
// 아니면 origin이 있을 때 그것이다. 그것도 없으면 어느 원격을 봐야 할지 알 수 없으므로 거짓이다.
// upstream이 없어도 판정하는 이유는 dev에서 방금 만든 빈 브랜치도 merged이기 때문이다.
// 사이드바와 worktree 화면이 같은 답을 내려면 같은 원격을 봐야 한다.
func RemoteFor(ctx context.Context, git gitrepo.Runner, repo gitrepo.Repo, upstream *gitrepo.Upstream) (string, bool) {
	if upstream != nil && upstream.Remote != "" {
		return upstream.Remote, true
	}
	remotes, err := git.Remotes(ctx, repo)
	if err != nil || len(remotes) == 0 {
		return "", false
	}
	if len(remotes) == 1 {
		return remotes[0], true
	}
	for _, name := range remotes {
		if name == "origin" {
			return name, true
		}
	}
	return "", false
}
