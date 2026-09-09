// Package config는 플러그인 설정을 읽는다.
//
// 설정 파일은 herdr가 관리하는 디렉터리에 둔다. 경로는 `herdr plugin config-dir git-upstream`로
// 확인할 수 있고, 파일이 없으면 모든 값이 기본값으로 동작한다. 설정 없이도 바로 쓸 수 있어야 하므로
// 파싱에 실패하더라도 기본값으로 진행하고 오류는 로그로만 남긴다.
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"herdr-git-upstream/internal/herdrpaths"
)

// FileName은 설정 디렉터리 안에서 찾는 파일 이름이다.
const FileName = "config.json"

// Config는 플러그인의 동작을 조절하는 값들이다. 시간 단위 값은 모두 초로 받는다.
type Config struct {
	// Enabled가 false면 데몬도 이벤트 훅도 아무 일을 하지 않고, 이미 올라간 토큰은 지운다.
	Enabled *bool `json:"enabled,omitempty"`

	// IntervalSeconds는 데몬이 전체 워크스페이스를 한 바퀴 도는 간격이다.
	IntervalSeconds int `json:"interval_seconds,omitempty"`

	// ThrottleSeconds는 같은 저장소를 다시 fetch하기까지의 최소 간격이다.
	// 데몬과 포커스 이벤트가 이 값을 공유하므로, 포커스를 자주 옮겨도 중복 fetch가 생기지 않는다.
	ThrottleSeconds int `json:"throttle_seconds,omitempty"`

	// FetchTimeoutSeconds는 저장소 하나당 fetch 제한 시간이다.
	FetchTimeoutSeconds int `json:"fetch_timeout_seconds,omitempty"`

	// StaleAfterSeconds는 fetch 실패가 이 시간 넘게 이어질 때 stale 토큰을 띄우는 기준이다.
	StaleAfterSeconds int `json:"stale_after_seconds,omitempty"`

	// BehindToken과 AheadToken은 사이드바에 보고할 토큰 이름이다. 빈 문자열이면 그 토큰을 보고하지 않는다.
	BehindToken *string `json:"behind_token,omitempty"`
	AheadToken  *string `json:"ahead_token,omitempty"`
	StaleToken  *string `json:"stale_token,omitempty"`

	// BehindPrefix와 AheadPrefix는 숫자 앞에 붙이는 기호다.
	BehindPrefix *string `json:"behind_prefix,omitempty"`
	AheadPrefix  *string `json:"ahead_prefix,omitempty"`

	// StaleLabel은 stale 토큰의 값이다.
	StaleLabel *string `json:"stale_label,omitempty"`

	// GoneToken과 MergedToken은 브랜치가 원격에서 끝난 작업인지 알리는 토큰 이름이고,
	// GoneLabel과 MergedLabel은 해당할 때 채우는 값이다.
	GoneToken   *string `json:"gone_token,omitempty"`
	GoneLabel   *string `json:"gone_label,omitempty"`
	MergedToken *string `json:"merged_token,omitempty"`
	MergedLabel *string `json:"merged_label,omitempty"`

	// CatchupToken은 따라잡을 때 충돌하는지 미리 알리는 토큰 이름이고,
	// CatchupConflictLabel은 충돌할 때만 채우는 값이다. 깨끗하면 빈 값이다.
	CatchupToken         *string `json:"catchup_token,omitempty"`
	CatchupConflictLabel *string `json:"catchup_conflict_label,omitempty"`

	// FreshWorktrees가 false면 새로 만든 worktree를 최신 상태로 맞추지 않는다.
	FreshWorktrees *bool `json:"fresh_worktrees,omitempty"`
}

// Resolved는 기본값이 모두 채워진, 바로 쓸 수 있는 설정이다.
type Resolved struct {
	Enabled      bool
	Interval     time.Duration
	Throttle     time.Duration
	FetchTimeout time.Duration
	StaleAfter   time.Duration
	BehindToken  string
	AheadToken   string
	StaleToken   string
	BehindPrefix string
	AheadPrefix  string
	StaleLabel   string
	// GoneToken, MergedToken, CatchupToken은 아직 보고하지 않는 토큰이다. setup이 사이드바 행을
	// 만들 때 이름을 알아야 하므로 먼저 설정에 자리를 둔다. 보고는 판정 기능과 함께 시작된다.
	GoneToken            string
	GoneLabel            string
	MergedToken          string
	MergedLabel          string
	CatchupToken         string
	CatchupConflictLabel string
	// FreshWorktrees는 갓 만든 worktree를 원격의 최신 상태로 앞당길지 정한다.
	FreshWorktrees bool

	// invalidTokens는 설정에 적혔지만 herdr가 받아들이지 않아 버린 이름들이다.
	invalidTokens []string
}

// 기본값. 60초 주기와 120초 스로틀은 사람이 pull을 잊고 지나칠 만한 시간보다 짧으면서,
// 원격에 부담을 주지 않는 선에서 고른 값이다.
const (
	defaultInterval     = 60 * time.Second
	defaultThrottle     = 120 * time.Second
	defaultFetchTimeout = 20 * time.Second
	defaultStaleAfter   = 15 * time.Minute

	defaultBehindToken  = "behind"
	defaultAheadToken   = "ahead"
	defaultStaleToken   = "sync_stale"
	defaultBehindPrefix = "↓"
	defaultAheadPrefix  = "↑"
	defaultStaleLabel   = "stale"

	defaultGoneToken            = "gone"
	defaultGoneLabel            = "gone"
	defaultMergedToken          = "merged"
	defaultMergedLabel          = "merged"
	defaultCatchupToken         = "catchup"
	defaultCatchupConflictLabel = "conflict"
)

// 사람이 실수로 극단적인 값을 넣었을 때를 위한 하한과 상한이다.
// herdr 자신이 사이드바를 1.5초마다 다시 그리므로 그보다 촘촘한 fetch는 의미가 없고,
// 하루를 넘기는 간격은 사실상 꺼 둔 것과 같아 설정 실수일 가능성이 높다.
const (
	minInterval     = 5 * time.Second
	maxInterval     = 24 * time.Hour
	minThrottle     = 0
	maxThrottle     = 24 * time.Hour
	minFetchTimeout = 1 * time.Second
	maxFetchTimeout = 10 * time.Minute
	minStaleAfter   = 1 * time.Minute
	maxStaleAfter   = 24 * time.Hour
)

// Dir는 설정 디렉터리를 돌려준다.
//
// herdr가 띄운 명령에는 HERDR_PLUGIN_CONFIG_DIR가 들어 있다. 사람이 셸에서 직접 부를 때는 그것이
// 없는데, 그때 herdr와 다른 자리를 보면 사용자가 적어 둔 설정이 통째로 무시된다. 그래서 herdr와
// 같은 규칙으로 계산한다.
func Dir() string {
	return herdrpaths.ConfigDir()
}

// validTokenName은 herdr가 받아들이는 토큰 이름의 규칙이다(^[A-Za-z0-9_-]{1,32}$).
//
// herdr는 한 요청에 담긴 토큰 이름을 통째로 검사해서 하나라도 어긋나면 요청 전체를 거절한다.
// 이름 하나를 잘못 적으면 나머지 토큰까지 함께 사라지므로, 여기서 걸러 내고 그 토큰만 쉬게 한다.
func validTokenName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// InvalidTokenNames는 설정에 적힌 토큰 이름 가운데 herdr가 받아들이지 않는 것들을 돌려준다.
// status 출력과 로그로 드러내, 아무것도 보이지 않는 이유를 사용자가 알 수 있게 한다.
func (r Resolved) InvalidTokenNames() []string {
	return r.invalidTokens
}

// TokenNames는 보고할 수 있는 토큰 이름의 집합이다. 빈 이름(보고하지 않음)과 규칙에 어긋나 버린
// 이름은 이미 빠져 있다. status와 setup이 "우리 토큰이 하나라도 설정에 있는가"를 물을 때 쓴다.
// 사이드바 행의 순서와 모양은 여기가 아니라 setup이 한 곳에서 정한다.
func (r Resolved) TokenNames() []string {
	var names []string
	for _, name := range []string{
		r.BehindToken, r.AheadToken, r.GoneToken, r.MergedToken, r.CatchupToken, r.StaleToken,
	} {
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// Load는 설정을 읽어 기본값을 채운 결과를 돌려준다.
// 파일이 없으면 오류가 아니며, 그 외의 오류는 기본값과 함께 돌려주어 호출자가 로그로 남기게 한다.
func Load() (Resolved, error) {
	dir := Dir()
	if dir == "" {
		return resolve(Config{}), nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return resolve(Config{}), nil
		}
		return resolve(Config{}), err
	}
	var parsed Config
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return resolve(Config{}), err
	}
	return resolve(parsed), nil
}

func resolve(c Config) Resolved {
	var invalid []string
	// 이름이 규칙에 어긋나면 그 토큰만 쉬게 한다(빈 이름 = 보고하지 않음).
	// 기본값으로 되돌리지 않는 이유는, 사용자가 의도한 이름과 다른 이름이 사이드바에 나타나면
	// 설정이 먹은 것처럼 보여 문제를 더 감추기 때문이다.
	check := func(name string) string {
		if name == "" || validTokenName(name) {
			return name
		}
		invalid = append(invalid, name)
		return ""
	}
	resolved := Resolved{
		Enabled:      boolOr(c.Enabled, true),
		Interval:     clampSeconds(c.IntervalSeconds, defaultInterval, minInterval, maxInterval),
		Throttle:     clampSeconds(c.ThrottleSeconds, defaultThrottle, minThrottle, maxThrottle),
		FetchTimeout: clampSeconds(c.FetchTimeoutSeconds, defaultFetchTimeout, minFetchTimeout, maxFetchTimeout),
		StaleAfter:   clampSeconds(c.StaleAfterSeconds, defaultStaleAfter, minStaleAfter, maxStaleAfter),
		BehindToken:  check(stringOr(c.BehindToken, defaultBehindToken)),
		AheadToken:   check(stringOr(c.AheadToken, defaultAheadToken)),
		StaleToken:   check(stringOr(c.StaleToken, defaultStaleToken)),
		BehindPrefix: stringOr(c.BehindPrefix, defaultBehindPrefix),
		AheadPrefix:  stringOr(c.AheadPrefix, defaultAheadPrefix),
		StaleLabel:   stringOr(c.StaleLabel, defaultStaleLabel),

		GoneToken:            check(stringOr(c.GoneToken, defaultGoneToken)),
		GoneLabel:            stringOr(c.GoneLabel, defaultGoneLabel),
		MergedToken:          check(stringOr(c.MergedToken, defaultMergedToken)),
		MergedLabel:          stringOr(c.MergedLabel, defaultMergedLabel),
		CatchupToken:         check(stringOr(c.CatchupToken, defaultCatchupToken)),
		CatchupConflictLabel: stringOr(c.CatchupConflictLabel, defaultCatchupConflictLabel),
		// 기본으로 켜 둔다. 하는 일이 빨리 감기뿐이라 사용자가 만든 것을 잃을 수 없고,
		// 낡은 바닥 위에서 새 작업을 시작하는 것이 이 플러그인이 막으려는 바로 그 상황이다.
		FreshWorktrees: boolOr(c.FreshWorktrees, true),
	}
	resolved.invalidTokens = invalid
	return resolved
}

// clampSeconds는 0(미지정)이면 기본값을, 그 외에는 상한과 하한 사이로 자른 값을 돌려준다.
// 음수는 미지정과 같게 다룬다.
func clampSeconds(seconds int, fallback, low, high time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	d := time.Duration(seconds) * time.Second
	if d < low {
		return low
	}
	if d > high {
		return high
	}
	return d
}

// stringOr는 포인터가 nil이면 기본값을 쓴다.
// 빈 문자열은 "이 토큰을 보고하지 마라"는 뜻이므로 기본값으로 되돌리지 않는다.
func stringOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func boolOr(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}
