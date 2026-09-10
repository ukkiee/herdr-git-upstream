package freshen

import (
	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/state"
)

// SuppressCreation은 팝업에서 선택한 원격 기준과 기존 브랜치를 자동 최신화 추측에서 보호한다.
// 생성 CLI 전에 호출한다. herdr는 생성 응답과 훅 완료를 동기화하지 않으므로 응답 뒤에도 지우지 않는다.
// CLI 시간 제한 뒤에도 herdr 서버의 생성은 계속될 수 있어 시간만으로 보호를 해제하지 않는다.
// 생성 완료 뒤에도 중복 이벤트를 보호한다. 이름을 새 Current 생성에 재사용할 때만 완료 기록을 해제한다.
func SuppressCreation(store state.Store, repo gitrepo.Repo, branch string) error {
	return store.SuppressFreshening(repo.CommonDir, branch)
}

func CompleteCreation(store state.Store, repo gitrepo.Repo, branch string) error {
	return store.CompleteFresheningSuppression(repo.CommonDir, branch)
}

func AllowCreation(store state.Store, repo gitrepo.Repo, branch string) error {
	return store.AllowFreshening(repo.CommonDir, branch)
}
