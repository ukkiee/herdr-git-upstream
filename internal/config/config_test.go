package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultsApplyWithoutAConfigFile(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())

	got, err := Load()
	if err != nil {
		t.Fatalf("설정 파일이 없는 것은 오류가 아니어야 한다: %v", err)
	}
	if !got.Enabled {
		t.Fatal("기본은 켜진 상태여야 한다")
	}
	if got.Interval != defaultInterval || got.Throttle != defaultThrottle {
		t.Fatalf("기본 주기가 적용되지 않았다: %+v", got)
	}
	if got.BehindToken != defaultBehindToken || got.BehindPrefix != defaultBehindPrefix {
		t.Fatalf("기본 토큰 설정이 적용되지 않았다: %+v", got)
	}
}

func TestValuesAreClamped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	write(t, dir, `{"interval_seconds":1,"throttle_seconds":999999999,"fetch_timeout_seconds":100000}`)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Interval != minInterval {
		t.Fatalf("너무 짧은 주기는 하한으로 잘려야 한다: %v", got.Interval)
	}
	if got.Throttle != maxThrottle {
		t.Fatalf("너무 긴 스로틀은 상한으로 잘려야 한다: %v", got.Throttle)
	}
	if got.FetchTimeout != maxFetchTimeout {
		t.Fatalf("너무 긴 제한 시간은 상한으로 잘려야 한다: %v", got.FetchTimeout)
	}
}

// 토큰 이름을 빈 문자열로 두는 것은 "그 토큰을 보고하지 마라"는 뜻이므로 기본값으로 되돌리면 안 된다.
func TestEmptyTokenNameDisablesThatToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	write(t, dir, `{"ahead_token":"","stale_token":""}`)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.AheadToken != "" || got.StaleToken != "" {
		t.Fatalf("빈 토큰 이름이 기본값으로 되돌아갔다: %+v", got)
	}
	if got.BehindToken != defaultBehindToken {
		t.Fatalf("지정하지 않은 토큰은 기본값이어야 한다: %q", got.BehindToken)
	}
}

// 설정이 깨져 있어도 플러그인은 기본값으로 계속 돌아야 한다.
func TestBrokenConfigFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	write(t, dir, `{이건 JSON이 아니다`)

	got, err := Load()
	if err == nil {
		t.Fatal("깨진 설정은 오류로 알려야 한다")
	}
	if got.Interval != defaultInterval {
		t.Fatalf("깨진 설정에서도 기본값을 돌려주어야 한다: %v", got.Interval)
	}
}

func TestEnabledFalseIsRespected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	write(t, dir, `{"enabled":false}`)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("enabled=false 가 무시되었다")
	}
}

func TestNegativeSecondsFallBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	write(t, dir, `{"interval_seconds":-5,"stale_after_seconds":-1}`)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Interval != defaultInterval {
		t.Fatalf("음수는 미지정과 같게 다뤄야 한다: %v", got.Interval)
	}
	if got.StaleAfter != defaultStaleAfter {
		t.Fatalf("음수는 미지정과 같게 다뤄야 한다: %v", got.StaleAfter)
	}
}

func TestClampSecondsBoundaries(t *testing.T) {
	if got := clampSeconds(0, time.Minute, time.Second, time.Hour); got != time.Minute {
		t.Fatalf("0은 기본값이어야 한다: %v", got)
	}
	if got := clampSeconds(30, time.Minute, time.Second, time.Hour); got != 30*time.Second {
		t.Fatalf("범위 안의 값은 그대로여야 한다: %v", got)
	}
}

func write(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// herdr 는 한 요청의 토큰 이름을 통째로 검사해서 하나라도 어긋나면 요청 전체를 거절한다.
// 그래서 이름 하나를 잘못 적으면 나머지 토큰까지 함께 사라진다. 여기서 걸러 내야 한다.
func TestInvalidTokenNamesAreDroppedNotSubstituted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	write(t, dir, `{"behind_token":"뒤처짐","ahead_token":"my.ahead"}`)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.BehindToken != "" || got.AheadToken != "" {
		t.Fatalf("규칙에 어긋난 이름은 보고하지 않아야 한다: %+v", got)
	}
	if got.StaleToken != defaultStaleToken {
		t.Fatalf("멀쩡한 토큰까지 함께 버리면 안 된다: %q", got.StaleToken)
	}
	names := got.InvalidTokenNames()
	if len(names) != 2 {
		t.Fatalf("버린 이름을 알려 주어야 한다: %v", names)
	}
}

func TestValidTokenName(t *testing.T) {
	valid := []string{"behind", "ahead", "sync_stale", "a", "A-9_z", "0123456789012345678901234567890a"}
	for _, name := range valid {
		if !validTokenName(name) {
			t.Fatalf("받아들여야 한다: %q", name)
		}
	}
	invalid := []string{"", "뒤처짐", "my.token", "with space", "a/b", "0123456789012345678901234567890ab"}
	for _, name := range invalid {
		if validTokenName(name) {
			t.Fatalf("거절해야 한다: %q", name)
		}
	}
}

// gone/merged/catchup 토큰은 아직 보고하지 않지만, setup 이 사이드바 행을 만들려면 이름을 알아야 한다.
// 기존 토큰과 같은 규칙을 따라야 사용자가 한 가지 방식만 익히면 된다.
func TestJudgementTokenDefaults(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"gone_token", got.GoneToken, "gone"},
		{"gone_label", got.GoneLabel, "gone"},
		{"merged_token", got.MergedToken, "merged"},
		{"merged_label", got.MergedLabel, "merged"},
		{"catchup_token", got.CatchupToken, "catchup"},
		{"catchup_conflict_label", got.CatchupConflictLabel, "conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("%s = %q, 기대값 %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestJudgementTokensFollowTheSameRules(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	write(t, dir, `{"gone_token":"","merged_token":"병합됨","catchup_token":"cu","catchup_conflict_label":"!"}`)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.GoneToken != "" {
		t.Fatalf("빈 이름은 보고하지 않음이어야 한다: %q", got.GoneToken)
	}
	if got.MergedToken != "" {
		t.Fatalf("규칙에 어긋난 이름은 버려야 한다: %q", got.MergedToken)
	}
	if got.CatchupToken != "cu" || got.CatchupConflictLabel != "!" {
		t.Fatalf("바꾼 이름과 라벨이 반영되지 않았다: %+v", got)
	}
	names := got.InvalidTokenNames()
	if len(names) != 1 || names[0] != "병합됨" {
		t.Fatalf("버린 이름을 알려 주어야 한다: %v", names)
	}
}

// TokenNames 는 setup 과 status 가 "우리 토큰"을 한 목록으로 다루는 근거다. 빈 이름과 버린 이름은 빠져야 한다.
func TestTokenNamesListsOnlyReportableNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	write(t, dir, `{"ahead_token":"","gone_token":"my.gone","catchup_token":"cu"}`)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"behind", "merged", "cu", "sync_stale"}
	names := got.TokenNames()
	if len(names) != len(want) {
		t.Fatalf("TokenNames = %v, 기대값 %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("TokenNames = %v, 기대값 %v", names, want)
		}
	}
}
