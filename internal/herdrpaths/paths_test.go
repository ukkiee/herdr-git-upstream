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
