package gitrepo

// 이 파일은 worktree 목록과 삭제를 다룬다. herdr 에 닿을 수 있으면 `herdr worktree list` 가 더 많은 것
// (어느 워크스페이스에 열려 있는가)을 알려 주므로 그쪽이 먼저고, 여기는 herdr 없이 터미널에서 화면을
// 열었을 때의 대안이다. 다만 잠김(locked)은 herdr 가 알려 주지 않으므로 그때도 여기서 읽는다.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WorktreeEntry는 `git worktree list --porcelain`의 한 항목이다.
type WorktreeEntry struct {
	// Path는 worktree 의 작업 디렉터리다. git 이 기록해 둔 절대 경로 그대로다.
	Path string
	// Head는 체크아웃된 커밋이다. bare 저장소 항목에는 없다.
	Head string
	// Branch는 체크아웃된 브랜치의 짧은 이름이다(refs/heads/ 를 뗀 것). 분리된 HEAD 면 비어 있다.
	// herdr 의 목록도 짧은 이름을 주므로, 두 길로 얻은 목록이 같은 모양이어야 화면이 하나의 코드로 그린다.
	Branch string
	// Detached는 HEAD 가 브랜치를 가리키지 않는다는 뜻이다.
	Detached bool
	// Bare는 작업 트리가 없는 항목이다. `git worktree list` 는 bare 저장소 자신도 한 줄로 낸다.
	Bare bool
	// Locked는 `git worktree lock` 으로 잠겼다는 뜻이다. 잠긴 것은 `git worktree remove` 가 거절한다.
	Locked bool
	// Prunable은 디렉터리가 사라져 `git worktree prune` 이 치울 수 있다는 뜻이다.
	Prunable bool
}

// Worktrees는 저장소에 연결된 worktree 전부를 돌려준다. 본 체크아웃이 첫 항목이다.
func (r Runner) Worktrees(ctx context.Context, repo Repo) ([]WorktreeEntry, error) {
	out, err := r.git(ctx, repo.Root, localTimeout, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktreeList(out), nil
}

// parseWorktreeList는 `git worktree list --porcelain` 의 출력을 해석한다.
//
// 항목은 빈 줄로 나뉘고, 항목의 첫 줄은 언제나 `worktree <경로>` 다. 그 뒤로 `HEAD <해시>`,
// `branch <참조>`, 그리고 낱말 하나짜리 표시(`bare`, `detached`, `locked`, `prunable`)가 온다.
// `locked` 와 `prunable` 뒤에는 공백과 이유가 붙을 수 있다. 이유는 쓰지 않으므로 앞 낱말만 본다.
// 모르는 줄은 건너뛴다. git 이 나중에 줄을 더해도 목록이 깨지지 않아야 한다.
func parseWorktreeList(out []byte) []WorktreeEntry {
	var entries []WorktreeEntry
	var current *WorktreeEntry
	for _, line := range splitLines(out) {
		if line == "" {
			current = nil
			continue
		}
		word, rest, _ := strings.Cut(line, " ")
		if word == "worktree" {
			entries = append(entries, WorktreeEntry{Path: rest})
			current = &entries[len(entries)-1]
			continue
		}
		if current == nil {
			continue
		}
		switch word {
		case "HEAD":
			current.Head = rest
		case "branch":
			current.Branch = strings.TrimPrefix(rest, "refs/heads/")
		case "bare":
			current.Bare = true
		case "detached":
			current.Detached = true
		case "locked":
			current.Locked = true
		case "prunable":
			current.Prunable = true
		}
	}
	return entries
}

// removeTimeout은 `git worktree remove` 의 제한 시간이다.
//
// localTimeout 을 쓰지 않는다. 삭제는 네트워크가 아니라 디스크 I/O 에 매이고, 걸리는 시간은 파일 수에
// 비례한다. node_modules 를 가진 JS 모노레포의 worktree 는 파일이 십만 개를 넘는데, 그 규모면 캐시가 따뜻한
// APFS 에서도 5초를 넘긴다(150,000 파일에 6.7초를 실측했다). 도중에 죽이면 git 이 작업 디렉터리를 반쯤 지운
// 채 관리 디렉터리(.git/worktrees/<id>)는 남겨, 그 worktree 는 다음 화면에서 dirty(추적 파일 삭제됨)로 보이고
// --force 없이는 다시 지울 수 없다. 강제 삭제는 없으므로(ADR 0002) 사람이 손으로 치워야 하는 상태다.
// 넉넉히 기다리는 편이 낫다.
const removeTimeout = 60 * time.Second

// RemoveWorktree는 worktree 하나를 지운다. herdr 에 열려 있지 않아 herdr 가 지워 주지 못하는 것을 위한 길이다.
//
// --force 는 붙이지 않는다. git 은 손댄 것이 있거나 잠긴 worktree 를 거절하는데, 그것이 곧 이 플러그인의
// 약속이다(ADR 0002). 화면이 safe 로 판정한 것만 여기 오지만, 판정과 삭제 사이에 사용자가 파일을 만들었을
// 수 있으므로 마지막 문턱은 git 에게 맡긴다. 브랜치는 남는다. worktree 를 지워도 커밋은 잃지 않는다.
//
// repo 는 지우려는 worktree 가 아닌 체크아웃(본 체크아웃)이어야 한다. git 은 repo.Root 를 작업 디렉터리로 삼아
// 도는데, 그 자리가 지워지는 디렉터리 자신이면 윈도우에서는 어느 프로세스의 cwd 든 RemoveDirectory 가 거절해
// 내용물과 관리 디렉터리만 지워지고 빈 최상위 디렉터리가 남은 채 실패한다. 이 프로세스 자신의 cwd 가 그
// worktree 안이어도 같은 이유로 막힌다(터미널에서 그 worktree 에 들어가 화면을 연 경우). macOS 와 리눅스는
// 그런 삭제를 허용해 시험으로 드러나지 않으므로, git 이 아니라 여기서 두 경우를 거절한다. 화면의 컨트롤러는
// worktree 마다 Discover 한 repo 가 아니라 본 체크아웃의 repo 로 이 함수를 불러야 한다.
//
// 경로 앞에 "--" 를 두어 "-" 로 시작하는 경로가 옵션으로 읽히지 않게 한다.
func (r Runner) RemoveWorktree(ctx context.Context, repo Repo, path string) error {
	if path == "" {
		return fmt.Errorf("worktree 경로가 비어 있다")
	}
	target := canonical(path)
	if canonical(repo.Root) == target {
		return fmt.Errorf("worktree 를 지우려면 본 체크아웃에서 불러야 한다: %s", path)
	}
	if cwd, err := os.Getwd(); err == nil && within(canonical(cwd), target) {
		return fmt.Errorf("지우려는 worktree 안에서는 지울 수 없다: %s", path)
	}
	_, err := r.git(ctx, repo.Root, removeTimeout, "worktree", "remove", "--", path)
	return err
}

// within은 dir 이 root 자신이거나 그 아래인지 답한다. 둘 다 canonical 을 거친 경로여야 한다.
//
// 문자열 접두어로 보지 않는다. "/a/b" 는 "/a/bc" 의 접두어지만 그 아래가 아니다. 상대 경로를 구해 ".." 로
// 시작하지 않으면 아래다. 드라이브가 다른 경로(윈도우)는 상대 경로를 구할 수 없으므로 아래가 아니다.
func within(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
