package gitrepo

import "context"

// BranchRefs는 로컬 브랜치와 원격 추적 참조의 전체 이름을 돌려준다. 짧은 이름은 로컬 origin/main 과
// 원격 origin/main 을 구분하지 못하므로 자동 이름의 빈자리 검사에서는 전체 이름을 사용한다.
func (r Runner) BranchRefs(ctx context.Context, repo Repo) ([]string, error) {
	out, err := r.git(ctx, repo.Root, localTimeout, "for-each-ref", "--format=%(refname)", "refs/heads/", "refs/remotes/")
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, ref := range splitLines(out) {
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

// LocalBranchExists는 로컬 브랜치가 있는지 답한다. 조회 실패를 없음으로 읽으면 기존 브랜치의
// upstream 을 새 worktree 정리 과정에서 지울 수 있으므로 종료 코드 1 만 없음으로 다룬다.
func (r Runner) LocalBranchExists(ctx context.Context, repo Repo, branch string) (bool, error) {
	args := []string{"show-ref", "--verify", "--quiet", "refs/heads/" + branch}
	result, err := r.gitExit(ctx, repo.Root, localTimeout, nonInteractiveEnv(), args...)
	if err != nil {
		return false, err
	}
	switch result.code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, result.asError(args)
	}
}

// UnsetUpstream은 지금 브랜치의 upstream 설정만 푼다. 원격 추적 참조에서 새 브랜치를 만들면 git 이
// 그 원격 브랜치를 upstream 으로 잡는데, 생성 팝업은 처음 push 할 때 사용자가 정하도록 이것을 푼다.
func (r Runner) UnsetUpstream(ctx context.Context, repo Repo) error {
	_, err := r.git(ctx, repo.Root, localTimeout, "branch", "--unset-upstream")
	return err
}
