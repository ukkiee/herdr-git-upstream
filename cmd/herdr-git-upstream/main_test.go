package main

import (
	"path/filepath"
	"testing"
)

// 홈 디렉터리 값이 구분자로 끝나도 ~ 로 줄여야 한다. os.UserHomeDir 는 HOME 을 정리하지 않고
// 돌려주지만, 견줄 경로는 herdrpaths 가 filepath.Join 으로 정리해 둔 것이다.
func TestAbbreviateHome(t *testing.T) {
	home := t.TempDir()
	sep := string(filepath.Separator)
	inside := filepath.Join(home, ".config", "herdr", "config.toml")
	outside := filepath.Join(t.TempDir(), "config.toml")
	abbreviated := "~" + sep + filepath.Join(".config", "herdr", "config.toml")

	cases := []struct {
		name string
		home string
		path string
		want string
	}{
		{"홈 아래", home, inside, abbreviated},
		{"구분자로 끝나는 홈", home + sep, inside, abbreviated},
		{"홈 바깥", home, outside, outside},
		{"홈 자체는 줄이지 않는다", home, home, home},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// os.UserHomeDir 는 유닉스에서 HOME 을, 윈도우에서 USERPROFILE 을 본다.
			t.Setenv("HOME", tc.home)
			t.Setenv("USERPROFILE", tc.home)
			if got := abbreviateHome(tc.path); got != tc.want {
				t.Fatalf("abbreviateHome(%q) = %q, 기대값 %q", tc.path, got, tc.want)
			}
		})
	}
}
