package judge

import (
	"reflect"
	"testing"
)

// 판정 표(docs/PLAN.md, CONTEXT.md)의 모든 가지를 표로 덮는다. 설명 조각도 함께 본다. 화면이 그대로 그리기 때문이다.
func TestAssess(t *testing.T) {
	// clean 은 손대지 않았고 앞뒤가 0 인 흔한 바탕이다. 각 경우는 여기서 필요한 것만 바꾼다.
	clean := Facts{Untouched: true, UntouchedKnown: true, GoneKnown: true, CountsKnown: true}
	with := func(edit func(f *Facts)) Facts {
		f := clean
		edit(&f)
		return f
	}

	cases := []struct {
		name    string
		facts   Facts
		verdict Verdict
		pieces  []string
	}{
		// blocked: 다른 무엇보다 먼저이고, 막힌 이유만 말한다.
		{"본 체크아웃", with(func(f *Facts) { f.IsMain = true }), Blocked, []string{"main checkout"}},
		{"잠김", with(func(f *Facts) { f.Locked = true }), Blocked, []string{"locked"}},
		{"디렉터리가 사라짐", with(func(f *Facts) { f.Prunable = true }), Blocked, []string{"missing"}},
		{"에이전트가 일하는 중", with(func(f *Facts) { f.AgentWorking = true }), Blocked, []string{"agent working"}},
		{"막힌 이유가 여럿이면 정해진 순서로 전부", Facts{IsMain: true, Locked: true, Prunable: true, AgentWorking: true}, Blocked, []string{"main checkout", "locked", "missing", "agent working"}},
		{"본 체크아웃은 끝난 일이어도 막힘", with(func(f *Facts) { f.IsMain, f.Merged, f.Gone = true, true, true }), Blocked, []string{"main checkout"}},
		{"막힌 행은 뒤처짐을 말하지 않음", with(func(f *Facts) { f.AgentWorking, f.Behind, f.Catchup = true, 12, CatchupConflict }), Blocked, []string{"agent working"}},

		// safe: 손대지 않았고, merged 이거나 gone 이면서 앞선 커밋이 0 임을 확인했다.
		{"merged", with(func(f *Facts) { f.Merged = true }), Safe, []string{"merged"}},
		{"gone 이고 앞선 커밋 없음", with(func(f *Facts) { f.Gone = true }), Safe, []string{"gone"}},
		{"gone 이고 merged", with(func(f *Facts) { f.Gone, f.Merged = true, true }), Safe, []string{"gone", "merged"}},
		{"gone 이고 뒤처졌지만 앞선 커밋 없음", with(func(f *Facts) { f.Gone, f.Behind = true, 3 }), Safe, []string{"gone", "↓3"}},
		{"merged 인데 따라잡으면 충돌해도 끝난 일", with(func(f *Facts) { f.Merged, f.Behind, f.Catchup = true, 5, CatchupConflict }), Safe, []string{"merged", "↓5", "conflicts"}},
		{"merged 이고 upstream 없음", with(func(f *Facts) { f.Merged, f.CountsKnown = true, false }), Safe, []string{"merged"}},
		// merged 가 판정을 정한 행에는 "올리지 않은 커밋이 있을지 모른다"는 조각이 붙으면 안 된다. safe 와 모순이다.
		{"gone 이고 merged 인데 추적 참조 없음", with(func(f *Facts) { f.Gone, f.Merged, f.CountsKnown = true, true, false }), Safe, []string{"gone", "merged"}},

		// review: 손댔거나, 충돌하거나, gone 인데 앞선 커밋이 있거나 확인할 수 없다.
		{"손댐", with(func(f *Facts) { f.Untouched = false }), Review, []string{"dirty"}},
		{"손댔으면 merged 여도 review", with(func(f *Facts) { f.Untouched, f.Merged = false, true }), Review, []string{"merged", "dirty"}},
		{"손댔는지 모르면 review", with(func(f *Facts) { f.UntouchedKnown = false }), Review, []string{"dirty?"}},
		{"손댔는지 모르고 merged", with(func(f *Facts) { f.UntouchedKnown, f.Merged = false, true }), Review, []string{"merged", "dirty?"}},
		{"따라잡으면 충돌", with(func(f *Facts) { f.Behind, f.Catchup = 12, CatchupConflict }), Review, []string{"↓12", "conflicts"}},
		{"gone 인데 앞선 커밋 있음", with(func(f *Facts) { f.Gone, f.Ahead = true, 2 }), Review, []string{"gone", "↑2"}},
		{"gone 인데 앞선 커밋 수를 확인할 수 없음", with(func(f *Facts) { f.Gone, f.CountsKnown = true, false }), Review, []string{"gone", "unpushed?"}},
		{"gone 이고 merged 인데 손댐", with(func(f *Facts) { f.Gone, f.Merged, f.Untouched = true, true, false }), Review, []string{"gone", "merged", "dirty"}},
		{"손댔고 뒤처지고 충돌", with(func(f *Facts) { f.Untouched, f.Behind, f.Catchup = false, 4, CatchupConflict }), Review, []string{"dirty", "↓4", "conflicts"}},
		{"손댔고 앞뒤 모두", with(func(f *Facts) { f.Untouched, f.Behind, f.Ahead = false, 1, 2 }), Review, []string{"dirty", "↓1", "↑2"}},

		// keep: 그 밖의 전부.
		{"최신", clean, Keep, []string{"up to date"}},
		{"깨끗하게 따라잡을 수 있음", with(func(f *Facts) { f.Behind, f.Catchup = 3, CatchupClean }), Keep, []string{"↓3", "clean catch-up"}},
		{"뒤처졌는데 따라잡기 판정을 못 함", with(func(f *Facts) { f.Behind = 3 }), Keep, []string{"↓3"}},
		{"앞서기만 함", with(func(f *Facts) { f.Ahead = 2 }), Keep, []string{"↑2"}},
		{"앞뒤 모두", with(func(f *Facts) { f.Ahead, f.Behind, f.Catchup = 2, 3, CatchupClean }), Keep, []string{"↓3", "↑2", "clean catch-up"}},
		// 추적 참조가 없는 까닭(upstream 없음, 지워짐, 아직 안 가져옴)은 Facts 로 가를 수 없으므로 셋 모두에 참인 말을 쓴다.
		{"추적 참조 없음", with(func(f *Facts) { f.CountsKnown = false }), Keep, []string{"no tracking ref"}},
		{"gone 인지 모르면 gone 이 아님", with(func(f *Facts) { f.GoneKnown, f.Gone = false, true }), Keep, []string{"up to date"}},
		{"gone 인지 모르고 추적 참조도 없음", with(func(f *Facts) { f.GoneKnown, f.CountsKnown = false, false }), Keep, []string{"no tracking ref"}},
		{"원격에 아직 있음", with(func(f *Facts) { f.Gone = false }), Keep, []string{"up to date"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict, pieces := Assess(tc.facts)
			if verdict != tc.verdict || !reflect.DeepEqual(pieces, tc.pieces) {
				t.Fatalf("%+v\n -> %s %q\n기대값 %s %q", tc.facts, verdict, pieces, tc.verdict, tc.pieces)
			}
		})
	}
}

// 영값은 Blocked 다. 판정을 채우지 못한 행이 삭제 쪽으로 읽히면 안 된다.
func TestVerdictZeroValueIsBlocked(t *testing.T) {
	var v Verdict
	if v != Blocked || v.String() != "blocked" {
		t.Fatalf("영값은 blocked 여야 한다: %d %q", v, v)
	}
}

func TestVerdictStringAndOrder(t *testing.T) {
	cases := []struct {
		verdict Verdict
		text    string
		order   int
	}{
		{Safe, "safe", 0},
		{Review, "review", 1},
		{Keep, "keep", 2},
		{Blocked, "blocked", 3},
		{Verdict(99), "blocked", 3},
	}
	for _, tc := range cases {
		if got := tc.verdict.String(); got != tc.text {
			t.Fatalf("%d -> %q, 기대값 %q", tc.verdict, got, tc.text)
		}
		if got := tc.verdict.Order(); got != tc.order {
			t.Fatalf("%s 의 정렬 순서 %d, 기대값 %d", tc.verdict, got, tc.order)
		}
	}
}
