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

func TestRemoteBranchesPreserveFetchMapping(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	run(t, work, "git", "remote", "rename", "origin", "team/upstream")
	run(t, work, "git", "config", "remote.team.url", remote)
	run(t, work, "git", "config", "--add", "remote.team/upstream.fetch", "+refs/heads/release:refs/remotes/team/upstream/published")
	run(t, work, "git", "config", "--add", "remote.team/upstream.fetch", "+refs/pull/*/head:refs/remotes/team/upstream/pr/*")
	// The exact mapping precedes the catch-all mapping, just as git resolves it.
	run(t, work, "git", "config", "--unset-all", "remote.team/upstream.fetch", "heads/\\*")
	run(t, work, "git", "config", "--add", "remote.team/upstream.fetch", "+refs/heads/*:refs/remotes/team/upstream/*")
	run(t, work, "git", "update-ref", "refs/remotes/team/upstream/published", "HEAD")
	run(t, work, "git", "update-ref", "refs/remotes/team/upstream/pr/17", "HEAD")
	git := Runner{}
	ctx := context.Background()
	repo, err := git.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	got, err := git.RemoteBranches(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []Upstream{
		{Remote: "team/upstream", RemoteRef: "refs/heads/main", TrackingRef: "refs/remotes/team/upstream/main"},
		{Remote: "team/upstream", RemoteRef: "refs/heads/release", TrackingRef: "refs/remotes/team/upstream/published"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("branch identity, aliases, or refspec mapping: got %+v, want %+v", got, want)
	}
}

func TestRemoteBranchesExcludeNonBranchesAfterDefaultRefspec(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	for _, kind := range []string{"pull", "tags"} {
		run(t, work, "git", "config", "--add", "remote.origin.fetch", "+refs/"+kind+"/*:refs/remotes/origin/"+kind+"/*")
		run(t, work, "git", "update-ref", "refs/remotes/origin/"+kind+"/17", "HEAD")
	}
	git := Runner{}
	repo, err := git.Discover(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	got, err := git.RemoteBranches(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TrackingRef != "refs/remotes/origin/main" {
		t.Fatalf("non-branches matched by a later refspec entered the dropdown: %+v", got)
	}
}

func TestRemoteBranchesIncludeCustomDestination(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	run(t, work, "git", "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/cache/*")
	run(t, work, "git", "fetch", "--quiet", "origin")
	git := Runner{}
	repo, err := git.Discover(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	got, err := git.RemoteBranches(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	want := Upstream{Remote: "origin", RemoteRef: "refs/heads/main", TrackingRef: "refs/remotes/cache/main"}
	if !slices.Contains(got, want) {
		t.Fatalf("custom fetch destination missing: %+v", got)
	}
}
