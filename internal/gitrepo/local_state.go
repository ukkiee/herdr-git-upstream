package gitrepo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// LocalPaths separates shared refs from each checkout's HEAD (including linked worktrees).
type LocalPaths struct {
	CommonDir string
	GitDir    string
}

func (r Runner) LocalPaths(ctx context.Context, repo Repo) (LocalPaths, error) {
	dir, err := r.gitLine(ctx, repo.Root, "rev-parse", "--git-dir")
	if err != nil {
		return LocalPaths{}, err
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repo.Root, dir)
	}
	return LocalPaths{CommonDir: repo.CommonDir, GitDir: canonical(dir)}, nil
}

// LocalStamp is a metadata snapshot. Reading it runs no Git commands and never scans work files
// or object contents. File identity also detects atomic replacements on coarse timestamp filesystems.
type LocalStamp struct {
	files map[string]os.FileInfo
}

func (s LocalStamp) Equal(other LocalStamp) bool {
	if len(s.files) != len(other.files) {
		return false
	}
	for path, before := range s.files {
		after, ok := other.files[path]
		if !ok || before.Size() != after.Size() || before.Mode() != after.Mode() ||
			!before.ModTime().Equal(after.ModTime()) || !os.SameFile(before, after) {
			return false
		}
	}
	return true
}

func SharedLocalState(commonDir string) (LocalStamp, error) {
	s := LocalStamp{files: map[string]os.FileInfo{}}
	for _, name := range []string{"config", "packed-refs", "shallow"} {
		if err := s.add(filepath.Join(commonDir, name)); err != nil {
			return s, err
		}
	}
	// refs may contain nested branch names; reftable stores refs in table files instead.
	for _, name := range []string{"refs", "reftable"} {
		err := filepath.WalkDir(filepath.Join(commonDir, name), func(path string, entry fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			return s.add(path)
		})
		if err != nil {
			return s, err
		}
	}
	return s, nil
}

func CheckoutLocalState(gitDir string) (LocalStamp, error) {
	s := LocalStamp{files: map[string]os.FileInfo{}}
	for _, name := range []string{"HEAD", "config.worktree"} {
		if err := s.add(filepath.Join(gitDir, name)); err != nil {
			return s, err
		}
	}
	return s, nil
}

func (s LocalStamp) add(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	s.files[path] = info
	return nil
}
