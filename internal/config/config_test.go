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
