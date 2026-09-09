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
	// 에이전트가 하위 디렉터리로 들어가 있는 경우가 있어 둘 다 본다.
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
}

type paneListResponse struct {
	Result struct {
		Panes []Pane `json:"panes"`
	} `json:"result"`
}

// Dir는 이 페인이 가리키는 디렉터리를 돌려준다. 전경 프로그램의 위치를 우선한다.
func (p Pane) Dir() string {
	if p.ForegroundCWD != "" {
		return p.ForegroundCWD
	}
	return p.CWD
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

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
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
