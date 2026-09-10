package gitrepo

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestCreateBranchInspectionAndUnsetUpstream(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	git := Runner{}
	ctx := context.Background()
	repo, err := git.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := git.BranchRefs(ctx, repo)
	if err != nil || !slices.Contains(refs, "refs/heads/main") || !slices.Contains(refs, "refs/remotes/origin/main") {
		t.Fatalf("전체 브랜치 참조: %v %v", refs, err)
	}
	for name, want := range map[string]bool{"main": true, "new": false, "origin/main": false} {
		if got, err := git.LocalBranchExists(ctx, repo, name); err != nil || got != want {
			t.Fatalf("로컬 브랜치 %s 존재 여부 %v %v, 기대값 %v", name, got, err, want)
		}
	}
	if _, err := git.Upstream(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := git.UnsetUpstream(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := git.Upstream(ctx, repo); !errors.Is(err, ErrNoUpstream) {
		t.Fatalf("upstream 이 남음: %v", err)
	}
	if _, err := git.LocalBranchExists(ctx, Repo{Root: t.TempDir()}, "main"); err == nil {
		t.Fatal("저장소 조회 실패를 브랜치 없음으로 읽으면 안 된다")
	}
}
