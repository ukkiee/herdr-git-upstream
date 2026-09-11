package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalStateDetectsRefsAndLinkedCheckoutHead(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	linked := filepath.Join(base, "linked")
	run(t, work, "git", "worktree", "add", "--quiet", "-b", "topic/nested", linked)
	runner := Runner{}
	ctx := context.Background()
	repo, err := runner.Discover(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := runner.LocalPaths(ctx, repo)
	if err != nil || sameDir(paths.GitDir, paths.CommonDir) || !sameDir(paths.CommonDir, filepath.Join(work, ".git")) {
		t.Fatalf("linked checkout paths: %+v %v", paths, err)
	}
	shared := func() LocalStamp {
		stamp, err := SharedLocalState(paths.CommonDir)
		if err != nil {
			t.Fatal(err)
		}
		return stamp
	}
	checkout := func() LocalStamp {
		stamp, err := CheckoutLocalState(paths.GitDir)
		if err != nil {
			t.Fatal(err)
		}
		return stamp
	}
	before, headBefore := shared(), checkout()
	writeFile(t, filepath.Join(linked, "untracked.txt"), "editing work files does not affect commit tokens")
	if !before.Equal(shared()) || !headBefore.Equal(checkout()) {
		t.Fatal("ordinary work files must not trigger commit token recalculation")
	}
	run(t, linked, "git", "checkout", "--quiet", "--detach", "HEAD")
	if headBefore.Equal(checkout()) || !before.Equal(shared()) {
		t.Fatal("detaching HEAD must change only the linked checkout state")
	}
	run(t, linked, "git", "update-ref", "refs/remotes/origin/deep/nested", "HEAD")
	if before.Equal(shared()) {
		t.Fatal("nested remote ref creation was missed")
	}
	before = shared()
	run(t, linked, "git", "pack-refs", "--all", "--prune")
	if before.Equal(shared()) {
		t.Fatal("packing refs was missed")
	}
	before = shared()
	run(t, linked, "git", "update-ref", "-d", "refs/remotes/origin/deep/nested")
	if before.Equal(shared()) {
		t.Fatal("packed ref deletion was missed")
	}
}

func TestLocalStateDetectsAtomicReplacementWithSameTimestamp(t *testing.T) {
	dir := t.TempDir()
	head := filepath.Join(dir, "HEAD")
	writeFile(t, head, "ref: refs/heads/first\n")
	before, err := CheckoutLocalState(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(head)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "HEAD.lock")
	writeFile(t, replacement, "ref: refs/heads/other\n")
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, head); err != nil {
		t.Fatal(err)
	}
	after, err := CheckoutLocalState(dir)
	if err != nil || before.Equal(after) {
		t.Fatalf("same-sized replacement with preserved timestamp must be detected: %v", err)
	}
}
