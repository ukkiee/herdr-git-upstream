package herdrpaths

import (
	"path/filepath"
	"testing"
)

// herdr 설정 파일은 플러그인 설정 디렉터리가 아니라 설정 뿌리 바로 아래에 있다.
// HERDR_PLUGIN_CONFIG_DIR 가 있어도 그쪽을 따라가면 안 된다.
func TestConfigFileSitsAtTheConfigRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", filepath.Join(root, "elsewhere"))

	want := filepath.Join(root, appDirName, "config.toml")
	if got := ConfigFile(); got != want {
		t.Fatalf("ConfigFile() = %q, 기대값 %q", got, want)
	}
}

// herdr는 값이 설정되어 있으면 빈 값과 상대 경로도 PathBuf로 그대로 받는다.
func TestConfigFileHonorsHerdrConfigPath(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "custom.toml"), "relative.toml", ""} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("HERDR_CONFIG_PATH", path)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if got := ConfigFile(); got != path {
				t.Fatalf("ConfigFile() = %q, want override %q", got, path)
			}
		})
	}
}
