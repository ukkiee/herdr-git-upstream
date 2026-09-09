// Package herdrpaths는 herdr가 플러그인에게 내주는 디렉터리를 herdr와 같은 방식으로 계산한다.
//
// herdr가 띄운 명령에는 HERDR_PLUGIN_CONFIG_DIR와 HERDR_PLUGIN_STATE_DIR가 들어 있어 그대로 쓰면
// 된다. 문제는 사람이 셸에서 이 실행 파일을 직접 부를 때다. 그때는 그 환경변수가 없는데, 여기서
// 엉뚱한 자리를 대안으로 고르면 herdr가 띄운 데몬과 다른 디렉터리를 보게 된다. 그러면 잠금 파일이
// 따로 놀아 데몬이 둘 뜨고, 사용자가 적어 둔 설정도 읽히지 않는다.
//
// 그래서 herdr의 계산(src/config/io.rs config_dir/state_dir, src/plugin_paths.rs)을 그대로 옮겨 둔다.
package herdrpaths

import (
	"os"
	"path/filepath"
	"runtime"
)

// PluginID는 herdr 매니페스트에 적은 id다. 디렉터리 이름이 여기서 나온다.
const PluginID = "git-upstream"

const appDirName = "herdr"

// ConfigDir는 이 플러그인의 설정 디렉터리다. herdr는 <설정 뿌리>/plugins/config/<id>를 쓴다.
func ConfigDir() string {
	if dir := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(configRoot(), "plugins", "config", PluginID)
}

// ConfigFile은 herdr 자신의 설정 파일이다. herdr는 <설정 뿌리>/config.toml을 읽는다.
//
// 플러그인 설정(ConfigDir)과 달리 환경변수로 덮어쓰는 길이 없다. herdr가 플러그인에게 자기 설정
// 파일의 자리를 알려 주지 않기 때문에, 언제나 herdr와 같은 계산으로 찾는다. setup 명령이 붙여 넣을
// 자리를 알려 줄 때와, status가 사이드바 설정이 들어 있는지 볼 때 쓴다.
func ConfigFile() string {
	return filepath.Join(configRoot(), "config.toml")
}

// StateDir는 이 플러그인의 상태 디렉터리다. herdr는 <상태 뿌리>/plugins/<id>를 쓴다.
func StateDir() string {
	if dir := os.Getenv("HERDR_PLUGIN_STATE_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(stateRoot(), "plugins", PluginID)
}

// configRoot는 herdr의 config_dir()과 같은 자리를 가리킨다.
func configRoot() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, appDirName)
	}
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("APPDATA"); dir != "" {
			return filepath.Join(dir, appDirName)
		}
		if profile := os.Getenv("USERPROFILE"); profile != "" {
			return filepath.Join(profile, "AppData", "Roaming", appDirName)
		}
		if home := os.Getenv("HOME"); home != "" {
			return filepath.Join(home, ".config", appDirName)
		}
		return filepath.Join(os.TempDir(), appDirName)
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", appDirName)
	}
	return filepath.Join(os.TempDir(), appDirName)
}

// stateRoot는 herdr의 state_dir()과 같은 자리를 가리킨다.
func stateRoot() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, appDirName)
	}
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, appDirName)
		}
		if profile := os.Getenv("USERPROFILE"); profile != "" {
			return filepath.Join(profile, "AppData", "Local", appDirName)
		}
		if home := os.Getenv("HOME"); home != "" {
			return filepath.Join(home, ".local", "state", appDirName)
		}
		return filepath.Join(os.TempDir(), appDirName+"-state")
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", appDirName)
	}
	return filepath.Join(os.TempDir(), appDirName+"-state")
}
