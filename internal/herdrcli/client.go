// Package herdrcli는 herdr 명령줄 도구를 통해 herdr 서버와 대화한다.
//
// 소켓 프로토콜을 직접 구현하지 않고 CLI를 쓰는 이유는 플랫폼 때문이다. herdr는 유닉스에서는
// 유닉스 도메인 소켓을, 윈도우에서는 named pipe를 쓴다. CLI는 그 차이를 이미 흡수하고 있으므로,
// CLI를 거치면 같은 코드가 세 플랫폼에서 그대로 돈다.
package herdrcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Client는 herdr 실행 파일 하나를 감싼다.
type Client struct {
	// Binary는 herdr 실행 파일의 경로다. 비어 있으면 New가 채운다.
	Binary string
	// Timeout은 herdr 명령 하나당 제한 시간이다.
	Timeout time.Duration
}

const defaultTimeout = 10 * time.Second

// New는 환경에서 herdr 실행 파일을 찾아 클라이언트를 만든다.
// herdr가 플러그인에게 HERDR_BIN_PATH로 정확한 경로를 알려 주므로 그것을 우선한다.
// 플러그인 밖에서 직접 실행하는 경우를 위해 PATH 탐색을 대안으로 둔다.
func New() *Client {
	binary := os.Getenv("HERDR_BIN_PATH")
	if binary == "" {
		binary = "herdr"
	}
	return &Client{Binary: binary, Timeout: defaultTimeout}
}

// Pane은 herdr가 알려 주는 페인 정보 중 이 플러그인이 쓰는 부분이다.
type Pane struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	// CWD는 페인 셸의 작업 디렉터리, ForegroundCWD는 그 안에서 도는 프로그램의 작업 디렉터리다.
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
}

type paneListResponse struct {
	Result struct {
		Panes []Pane `json:"panes"`
	} `json:"result"`
}

// Dir는 이 페인이 가리키는 디렉터리를 돌려준다.
//
// 셸의 작업 디렉터리를 쓴다. herdr가 워크스페이스의 정체를 정할 때 보는 값이 바로 이것이기 때문이다
// (src/workspace/tab.rs cwd_for_pane). 전경 프로그램의 위치는 herdr가 정체에도 라벨에도 git 상태에도
// 쓰지 않으므로, 그쪽을 우선하면 사이드바가 그리는 브랜치와 이 플러그인이 세는 숫자가 서로 다른
// 저장소를 가리킬 수 있다. 그것은 이 플러그인이 막으려던 바로 그 어긋남이다.
func (p Pane) Dir() string {
	if p.CWD != "" {
		return p.CWD
	}
	return p.ForegroundCWD
}

// PaneList는 열려 있는 모든 페인을 돌려준다.
//
// 워크스페이스 목록이 아니라 페인 목록을 쓰는 이유가 있다. `herdr workspace list`의 worktree 필드는
// 저장소에 따라 비어 있어서 저장소 경로를 알아내는 근거로 삼을 수 없다. 반면 페인은 언제나 작업
// 디렉터리를 들고 있고, workspace_id도 함께 주므로 한 번의 호출로 워크스페이스와 경로를 모두 얻는다.
func (c *Client) PaneList(ctx context.Context) ([]Pane, error) {
	out, err := c.run(ctx, "pane", "list")
	if err != nil {
		return nil, err
	}
	var parsed paneListResponse
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("pane list 응답을 해석하지 못했다: %w", err)
	}
	return parsed.Result.Panes, nil
}

// Workspace는 herdr가 알려 주는 워크스페이스 정보 중 이 플러그인이 쓰는 부분이다.
type Workspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	// AgentStatus는 herdr가 그 워크스페이스의 에이전트를 어떻게 보고 있는지다. 실측한 값은
	// "working", "idle", "unknown"이다. worktree 화면은 "working"일 때만 그 worktree를 blocked로 본다.
	// 모르는 값은 일하지 않는 것으로 읽는다. 그래야 herdr가 새 상태 이름을 더해도 화면이 멈추지 않는다.
	AgentStatus string `json:"agent_status"`
	// Worktree는 이 워크스페이스가 어느 체크아웃에 속하는지 알려 준다. worktree 흐름으로 만든
	// 워크스페이스에만 들어 있고, 그렇지 않은 저장소에서는 비어 있다.
	Worktree *Worktree `json:"worktree"`
}

// AgentWorking은 herdr가 이 워크스페이스에서 에이전트가 일하는 중이라고 보는지 답한다.
func (w Workspace) AgentWorking() bool {
	return w.AgentStatus == "working"
}

// Worktree는 워크스페이스가 매인 git 체크아웃이다.
type Worktree struct {
	CheckoutPath string `json:"checkout_path"`
	RepoRoot     string `json:"repo_root"`
	RepoKey      string `json:"repo_key"`
	IsLinked     bool   `json:"is_linked_worktree"`
}

type workspaceListResponse struct {
	Result struct {
		Workspaces []Workspace `json:"workspaces"`
	} `json:"result"`
}

// WorkspaceList는 열려 있는 워크스페이스를 돌려준다.
//
// 페인 목록만으로도 대부분은 알 수 있지만, worktree로 만든 워크스페이스는 herdr가 체크아웃 경로를
// 따로 기억해 둔다. 그 값은 페인이 어떻게 바뀌든 흔들리지 않으므로, 있을 때는 그것을 먼저 믿는다.
func (c *Client) WorkspaceList(ctx context.Context) ([]Workspace, error) {
	out, err := c.run(ctx, "workspace", "list")
	if err != nil {
		return nil, err
	}
	return parseWorkspaceList(out)
}

// parseWorkspaceList는 `herdr workspace list`의 응답을 해석한다. 실행과 떼어 두어 고정된 JSON으로 시험한다.
func parseWorkspaceList(out []byte) ([]Workspace, error) {
	var parsed workspaceListResponse
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("workspace list 응답을 해석하지 못했다: %w", err)
	}
	return parsed.Result.Workspaces, nil
}

// WorktreeSource는 `herdr worktree list`가 말하는 "이 목록이 어느 저장소의 것인가"다.
type WorktreeSource struct {
	// RepoName은 herdr가 부르는 저장소 이름이다. 화면의 제목 줄에 쓴다.
	RepoName string `json:"repo_name"`
	RepoRoot string `json:"repo_root"`
	// SourceCheckoutPath는 본 체크아웃의 경로, SourceWorkspaceID는 그것이 열려 있는 워크스페이스다.
	SourceCheckoutPath string `json:"source_checkout_path"`
	SourceWorkspaceID  string `json:"source_workspace_id"`
}

// WorktreeItem은 `herdr worktree list`의 한 줄이다. git이 아는 worktree에 herdr가 아는 것
// (어느 워크스페이스에 열려 있는가)을 덧붙인 것이다.
type WorktreeItem struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	IsBare bool   `json:"is_bare"`
	// IsDetached는 HEAD가 브랜치를 가리키지 않는다는 뜻이다. 그때 Branch는 비어 있다.
	IsDetached bool `json:"is_detached"`
	// IsLinkedWorktree가 거짓이면 본 체크아웃이다. 화면은 그것을 blocked로 본다.
	IsLinkedWorktree bool `json:"is_linked_worktree"`
	// IsPrunable은 디렉터리가 사라져 `git worktree prune`이 치울 수 있다는 뜻이다.
	IsPrunable bool   `json:"is_prunable"`
	Label      string `json:"label"`
	// OpenWorkspaceID는 이 worktree가 열려 있는 워크스페이스다. 열려 있지 않으면 비어 있다.
	// 이 값이 있으면 삭제와 이동을 herdr에게 맡기고, 없으면 git을 직접 부른다.
	OpenWorkspaceID string `json:"open_workspace_id"`
}

// WorktreeListResult는 `herdr worktree list`의 응답이다.
type WorktreeListResult struct {
	Source    WorktreeSource `json:"source"`
	Worktrees []WorktreeItem `json:"worktrees"`
}

type worktreeListResponse struct {
	Result WorktreeListResult `json:"result"`
}

// WorktreeList는 한 저장소의 worktree 전부를 herdr에게 묻는다.
//
// workspaceID가 있으면 그 워크스페이스가 속한 저장소, 없으면 cwd가 속한 저장소다. 액션으로 열리면
// HERDR_WORKSPACE_ID가 있고, 터미널에서 직접 부르면 현재 디렉터리뿐이라 두 길을 다 둔다. 둘 다 비어
// 있으면 옵션 없이 부르는데, 그때는 herdr가 자기 문맥으로 저장소를 고른다.
func (c *Client) WorktreeList(ctx context.Context, workspaceID, cwd string) (WorktreeListResult, error) {
	out, err := c.run(ctx, worktreeListArgs(workspaceID, cwd)...)
	if err != nil {
		return WorktreeListResult{}, err
	}
	return parseWorktreeList(out)
}

func worktreeListArgs(workspaceID, cwd string) []string {
	args := []string{"worktree", "list"}
	switch {
	case workspaceID != "":
		args = append(args, "--workspace", workspaceID)
	case cwd != "":
		args = append(args, "--cwd", cwd)
	}
	return args
}

// parseWorktreeList는 `herdr worktree list`의 응답을 해석한다. 실행과 떼어 두어 고정된 JSON으로 시험한다.
func parseWorktreeList(out []byte) (WorktreeListResult, error) {
	var parsed worktreeListResponse
	if err := json.Unmarshal(out, &parsed); err != nil {
		return WorktreeListResult{}, fmt.Errorf("worktree list 응답을 해석하지 못했다: %w", err)
	}
	return parsed.Result, nil
}

// removeTimeout은 WorktreeRemove 의 제한 시간이다. gitrepo 의 RemoveWorktree 와 같은 값이다.
//
// 목록 조회와 같은 Timeout 을 쓰지 않는다. herdr 가 지워도 결국 `git worktree remove` 가 돌고, 그 시간은
// 파일 수에 비례한다(근거는 internal/gitrepo/worktree.go 의 removeTimeout 주석에 실측과 함께 있다). 여기만
// 짧으면 캐시가 식었거나 대량 삭제가 느린 파일 시스템(NTFS)에서 CLI 가 먼저 죽어 화면에는 실패로 보이지만
// 서버 쪽 삭제는 계속 진행되어 다음 갱신에서야 사라진다. 두 삭제 길은 같은 약속(ADR 0002)을 지켜야 하므로
// 제한 시간도 같아야 한다. gitrepo 의 상수를 가져오지 않는 이유는 이 패키지가 gitrepo 에 기대지 않기 때문이다.
const removeTimeout = 60 * time.Second

// WorktreeRemove는 herdr에 열려 있는 worktree를 herdr에게 지우게 한다.
//
// --force는 붙이지 않는다. herdr는 손댄 것이 있으면 거절하는데, 그 거절이 곧 이 플러그인의 약속이다
// (ADR 0002). 화면이 safe로 판정한 것만 여기 오지만, 판정과 삭제 사이에 사용자가 파일을 만들었을 수
// 있으므로 마지막 문턱은 herdr에게 맡긴다.
func (c *Client) WorktreeRemove(ctx context.Context, workspaceID string) error {
	if workspaceID == "" {
		return fmt.Errorf("workspace_id가 비어 있다")
	}
	_, err := c.runWithTimeout(ctx, removeTimeout, "worktree", "remove", "--workspace", workspaceID)
	return err
}

// WorktreeOpen은 디스크에만 있는 worktree를 herdr 워크스페이스로 열고 그리로 옮겨 간다.
func (c *Client) WorktreeOpen(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("경로가 비어 있다")
	}
	_, err := c.run(ctx, "worktree", "open", "--path", path, "--focus")
	return err
}

// WorkspaceFocus는 이미 열려 있는 워크스페이스로 옮겨 간다.
func (c *Client) WorkspaceFocus(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("workspace_id가 비어 있다")
	}
	_, err := c.run(ctx, "workspace", "focus", id)
	return err
}

// PluginPaneOpen은 플러그인 매니페스트에 선언된 pane을 연다. 액션이 화면을 띄우는 통로다.
//
// placement가 비어 있으면 옵션을 붙이지 않아 매니페스트의 값을 따르게 한다. CLI의 --placement에는
// popup이 없어서, 매니페스트의 popup을 쓰려면 비워서 부르는 수밖에 없다. 비워도 매니페스트를 따르지
// 않는 것으로 실측되면 호출자가 overlay로 다시 부른다.
func (c *Client) PluginPaneOpen(ctx context.Context, plugin, entrypoint, placement string) error {
	if plugin == "" || entrypoint == "" {
		return fmt.Errorf("플러그인 id와 pane id가 모두 있어야 한다")
	}
	_, err := c.run(ctx, pluginPaneOpenArgs(plugin, entrypoint, placement)...)
	return err
}

func pluginPaneOpenArgs(plugin, entrypoint, placement string) []string {
	args := []string{"plugin", "pane", "open", "--plugin", plugin, "--entrypoint", entrypoint}
	if placement != "" {
		args = append(args, "--placement", placement)
	}
	return append(args, "--focus")
}

// ReportMetadata는 워크스페이스에 사이드바 토큰을 보고한다.
//
// 값이 빈 문자열인 토큰은 herdr가 해당 키를 지우는 것으로 해석하므로, 표시할 것이 없을 때는
// 빈 값을 보내면 된다. ttl이 0보다 크면 그 시간이 지난 뒤 herdr가 값을 스스로 지운다. 이 플러그인이
// 죽었을 때 낡은 숫자가 사이드바에 영원히 남지 않도록 하는 안전장치다.
func (c *Client) ReportMetadata(ctx context.Context, workspaceID, source string, tokens map[string]string, ttl time.Duration) error {
	if workspaceID == "" {
		return fmt.Errorf("workspace_id가 비어 있다")
	}
	if len(tokens) == 0 {
		return nil
	}
	args := []string{"workspace", "report-metadata", workspaceID, "--source", source}
	for name, value := range tokens {
		if name == "" {
			continue
		}
		args = append(args, "--token", name+"="+value)
	}
	if ttl > 0 {
		args = append(args, "--ttl-ms", strconv.FormatInt(ttl.Milliseconds(), 10))
	}
	_, err := c.run(ctx, args...)
	return err
}

// run은 herdr 명령 하나를 Timeout 안에 돌린다. 조회처럼 금방 끝나는 명령의 길이다.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return c.runWithTimeout(ctx, timeout, args...)
}

// runWithTimeout은 run 의 본체다. 디스크 I/O 에 매여 오래 걸릴 수 있는 명령(WorktreeRemove)이 자기 제한 시간을
// 들고 온다.
func (c *Client) runWithTimeout(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// herdr 명령은 입력을 읽지 않는다. 그래도 상속된 표준 입력을 끊어 두어야,
	// 예기치 못한 상황에서 프롬프트를 기다리며 멈추는 일이 없다.
	cmd.Stdin = nil

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return nil, fmt.Errorf("herdr %s 실패: %w: %s", strings.Join(args, " "), err, truncate(detail, 200))
		}
		return nil, fmt.Errorf("herdr %s 실패: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	// 바이트가 아니라 룬 경계에서 자른다. 잘린 UTF-8 조각이 로그에 남지 않게 하기 위함이다.
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}
