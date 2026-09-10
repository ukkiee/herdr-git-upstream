package judge

// 이 파일은 worktree 화면의 판정 규칙이다. 재료(Facts)를 모으는 일은 화면의 컨트롤러가 하고, 여기는
// 재료에서 답을 내는 순수 함수만 둔다. 규칙이 한곳에 있어야 표 형식 시험으로 모든 가지를 덮을 수 있고,
// README 의 판정 표와 코드가 어긋나지 않는다.

import "strconv"

// Verdict는 worktree 화면이 worktree 마다 붙이는 네 값 중 하나다. 정의는 CONTEXT.md 의 "판정"에 있다.
type Verdict int

const (
	// Blocked는 지울 수 없다는 뜻이다. 본 체크아웃이거나, 잠겼거나, 디렉터리가 사라졌거나, 에이전트가 일하는 중이다.
	//
	// 영값이 Blocked 인 것은 일부러다. 판정을 채우지 못한 행(자료를 모으다 실패한 경우)이 있어도
	// 삭제 쪽으로 읽히지 않아야 한다. 지우는 일은 되돌릴 수 없으므로 모르면 막는 쪽이 옳다.
	Blocked Verdict = iota
	// Safe는 끝난 일이다. 손대지 않았고, merged 이거나 gone 이면서 앞선 커밋이 없음을 확인했다.
	Safe
	// Review는 사람이 봐야 한다. 손댄 것이 있거나, 따라잡을 때 충돌하거나, gone 인데 앞선 커밋이 있거나 확인할 수 없다.
	Review
	// Keep은 진행 중인 작업이다. 최신이거나 깨끗하게 따라잡을 수 있다.
	Keep
)

// String은 화면과 README 에 쓰는 이름이다.
func (v Verdict) String() string {
	switch v {
	case Safe:
		return "safe"
	case Review:
		return "review"
	case Keep:
		return "keep"
	default:
		// 모르는 값은 지울 수 없는 쪽으로 읽는다. Blocked 가 영값인 것과 같은 이유다.
		return "blocked"
	}
}

// Order는 화면의 정렬 순서다. 손볼 것이 먼저 오고 손댈 수 없는 것이 마지막이다: safe, review, keep, blocked.
//
// 상수의 순서(영값이 Blocked)와 정렬 순서가 다르므로 따로 둔다. 상수 값을 정렬에 그대로 쓰면
// "영값은 막는 쪽"이라는 안전장치를 잃는다.
func (v Verdict) Order() int {
	switch v {
	case Safe:
		return 0
	case Review:
		return 1
	case Keep:
		return 2
	default:
		return 3
	}
}

// Facts는 worktree 하나에 대해 판정에 쓰는 재료다. 모르는 것은 "모른다"고 적는다.
//
// "거짓"과 "모름"을 가르는 Known 필드가 있는 이유가 있다. 손댔는지 모르는 것(git status 실패)을
// 손대지 않은 것으로 읽으면 지우면 안 되는 것을 지우고, gone 인지 모르는 것을 gone 으로 읽으면
// 멀쩡한 작업이 review 에 오른다. 모르는 것은 언제나 지우지 않는 쪽으로 판정한다.
type Facts struct {
	// IsMain은 본 체크아웃(is_linked_worktree 가 거짓)이다.
	IsMain bool
	// Locked는 `git worktree lock` 으로 잠겼다.
	Locked bool
	// Prunable은 디렉터리가 사라졌다(is_prunable).
	Prunable bool
	// AgentWorking은 herdr 가 그 워크스페이스의 에이전트가 일하는 중이라고 본다.
	AgentWorking bool

	// Untouched는 추적되지 않은 파일까지 하나도 없다. UntouchedKnown 이 거짓이면 Untouched 는 뜻이 없다.
	Untouched      bool
	UntouchedKnown bool

	// Gone은 upstream 브랜치가 원격에서 사라졌다. GoneKnown 이 거짓이면 원격에 물은 결과도 데몬의 기록도
	// 없어 알 수 없다는 뜻이고, 그때 Gone 은 뜻이 없다.
	Gone      bool
	GoneKnown bool

	// Merged는 HEAD 의 내용이 이미 어느 원격 브랜치에 들어가 있다. 놓칠 수는 있어도 틀리지는 않으므로 Known 이 없다.
	Merged bool

	// Ahead 와 Behind 는 추적 참조 대비 앞뒤 커밋 수다. CountsKnown 이 거짓이면 셀 수 없었다는 뜻이다.
	// upstream 이 없거나, 추적 참조가 이미 지워졌거나, 한 번도 가져온 적 없는 경우다.
	Ahead       int
	Behind      int
	CountsKnown bool

	// Catchup은 따라잡을 때 충돌하는지다. 뒤처졌을 때만 판정하므로 그 밖에는 CatchupUnknown 이다.
	Catchup Catchup
}

// Assess는 재료에서 판정과 설명 조각을 낸다. 순수 함수다.
//
// 판정 표(docs/PLAN.md, CONTEXT.md)를 그대로 옮긴다. blocked 는 다른 무엇보다 먼저다. 지울 수 없는
// 것은 끝난 일이어도 지울 수 없다. 그다음 safe 를 review 보다 먼저 본다. 손대지 않은 merged 브랜치는
// 따라잡을 때 충돌하더라도 끝난 일이라 지워도 잃을 것이 없기 때문이다.
//
// 설명 조각은 화면의 설명 열에 쉼표로 이어 붙일 짧은 영문이다. blocked 는 막힌 이유만 말한다. 그 행에서
// 사람이 할 일은 없으므로 뒤처짐 같은 나머지 사실은 소음이다. 다른 판정은 근거가 된 사실을 정해진
// 순서(gone, merged, dirty, 뒤처짐, 앞섬, 따라잡기)로 말하고, 말할 것이 아무것도 없을 때만
// "up to date" 나 "no tracking ref" 로 그 자리를 채운다. 빈 설명 열은 판정이 덜 된 것처럼 보인다.
//
// 채움말이 "no upstream" 이 아닌 이유가 있다. CountsKnown 이 거짓인 까닭은 셋(upstream 이 없거나, 추적 참조가
// 지워졌거나, 아직 한 번도 가져오지 않았거나)인데 Facts 로는 가를 수 없다. 첫 로컬 그리기(FetchAll 전)나
// fetch 실패 뒤에는 upstream 이 있는 브랜치도 여기 떨어지므로, 셋 모두에 참인 말만 화면에 적는다.
func Assess(f Facts) (Verdict, []string) {
	if reasons := blockedReasons(f); len(reasons) > 0 {
		return Blocked, reasons
	}

	gone := f.GoneKnown && f.Gone
	dirty := f.UntouchedKnown && !f.Untouched
	// gone 인데 앞선 커밋이 0 임을 확인할 수 있어야 끝난 일이다. 추적 참조가 이미 지워져 셀 수 없으면
	// 올리지 않은 커밋이 있을지 모르므로 review 다.
	goneAndPushed := gone && f.CountsKnown && f.Ahead == 0
	goneAndUnsure := gone && (!f.CountsKnown || f.Ahead > 0)

	var pieces []string
	if gone {
		pieces = append(pieces, "gone")
	}
	if f.Merged {
		pieces = append(pieces, "merged")
	}
	switch {
	case dirty:
		pieces = append(pieces, "dirty")
	case !f.UntouchedKnown:
		pieces = append(pieces, "dirty?")
	}
	if f.CountsKnown {
		if f.Behind > 0 {
			pieces = append(pieces, "↓"+strconv.Itoa(f.Behind))
		}
		if f.Ahead > 0 {
			pieces = append(pieces, "↑"+strconv.Itoa(f.Ahead))
		}
	} else if gone && !f.Merged {
		// "올리지 않은 커밋이 있을지 모른다"는 review 의 근거다. merged 이면 내용이 이미 원격에 들어갔으므로
		// 그 물음은 답이 난 것이고, 판정도 merged 가 정한다. 그 행에 이 조각을 붙이면 safe 와 나란히 찍혀
		// 서로 어긋난다. 사용자가 `git fetch --prune` 을 직접 돌려 추적 참조가 지워진 저장소에서 squash
		// 병합을 merge-tree 가 잡는 경우가 바로 이 조합이라 드물지 않다.
		pieces = append(pieces, "unpushed?")
	}
	switch f.Catchup {
	case CatchupConflict:
		pieces = append(pieces, "conflicts")
	case CatchupClean:
		pieces = append(pieces, "clean catch-up")
	}
	if len(pieces) == 0 {
		if f.CountsKnown {
			pieces = append(pieces, "up to date")
		} else {
			pieces = append(pieces, "no tracking ref")
		}
	}

	switch {
	case f.UntouchedKnown && f.Untouched && (f.Merged || goneAndPushed):
		return Safe, pieces
	case dirty || !f.UntouchedKnown || f.Catchup == CatchupConflict || goneAndUnsure:
		return Review, pieces
	default:
		return Keep, pieces
	}
}

// blockedReasons는 지울 수 없는 이유들을 정해진 순서로 모은다. 하나도 없으면 nil 이다.
func blockedReasons(f Facts) []string {
	var reasons []string
	if f.IsMain {
		reasons = append(reasons, "main checkout")
	}
	if f.Locked {
		reasons = append(reasons, "locked")
	}
	if f.Prunable {
		reasons = append(reasons, "missing")
	}
	if f.AgentWorking {
		reasons = append(reasons, "agent working")
	}
	return reasons
}
