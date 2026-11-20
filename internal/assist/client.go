// Package assist asks a configured language model to select a download recovery action.
package assist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Config struct {
	BaseURL  string `json:"baseURL"`
	Model    string `json:"model"`
	Protocol string `json:"protocol,omitempty"`
}

func (c Config) protocol() string {
	if c.Protocol == "" {
		return "openai"
	}
	return c.Protocol
}

// SameService compares the endpoint and authentication protocol, ignoring the selected model.
func (c Config) SameService(other Config) bool {
	return c.protocol() == other.protocol() && strings.TrimRight(c.BaseURL, "/") == strings.TrimRight(other.BaseURL, "/")
}

func loopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func (c Config) Validate() error {
	if err := c.validateService(); err != nil {
		return err
	}
	if c.BaseURL == "" && c.Model == "" {
		return nil
	}
	if c.BaseURL == "" || c.Model == "" {
		return errors.New("请同时填写 AI 服务地址和模型名称")
	}
	if !validModelID(c.Model) {
		return errors.New("AI 模型名称无效")
	}
	return nil
}

func validModelID(id string) bool {
	return id != "" && len(id) <= 256 && strings.TrimSpace(id) == id && strings.IndexFunc(id, unicode.IsControl) < 0
}

func (c Config) validateService() error {
	switch c.protocol() {
	case "openai", "openai-responses", "anthropic":
	default:
		return errors.New("请选择支持的 AI 接口协议")
	}
	if c.BaseURL == "" {
		return nil
	}
	badURL := errors.New("AI 服务地址需为 HTTPS 或本机 HTTP 基础地址，且不能包含凭据、查询参数或片段")
	if len(c.BaseURL) > 2048 || strings.IndexFunc(c.BaseURL, unicode.IsSpace) >= 0 || strings.ContainsAny(c.BaseURL, "?#\\") {
		return badURL
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
		return badURL
	}
	if strings.HasSuffix(u.Host, ":") || (strings.Contains(u.Hostname(), ":") && !strings.HasPrefix(u.Host, "[")) {
		return badURL
	}
	if u.Port() != "" {
		port, err := strconv.ParseUint(u.Port(), 10, 16)
		if err != nil || port == 0 {
			return badURL
		}
	}
	return nil
}

// Input contains diagnostic categories and job settings prepared by the application.
type Input struct {
	Summary        string   `json:"summary"`
	Format         string   `json:"format"`
	YouTube        bool     `json:"youtube"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
	Retries        int      `json:"retries"`
	Signals        []string `json:"signals"`
}

type Decision struct {
	Action string `json:"action"`
	Advice string `json:"advice"`
}

const instructions = `根据下载器提供的错误类别及任务设置，选择一次恢复操作。只返回一个 JSON 对象，包含 action 和 advice 两个字符串。advice 用简短中文说明原因和下一步。action 只能是以下之一：
network：网络超时或临时断连，延长超时并退避重试。
combined：适合视频输出的分轨格式错误，优先使用同时含画面和声音的单文件。
ipv4：有地址族或 IPv6 连通性问题时，使用 IPv4。
stop：需要用户处理，或者没有合适的恢复操作。对于登录、访问权限、DRM、限流、失效链接、磁盘和文件权限问题，选择 stop 并给出对应建议。
不要提供命令、脚本、网址或要求用户分享密钥。`

var client = &http.Client{
	Timeout: 60 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func Plan(ctx context.Context, config Config, key string, input Input) (Decision, error) {
	if err := config.Validate(); err != nil {
		return Decision{}, err
	}
	if config.BaseURL == "" {
		return Decision{}, errors.New("请先在设置中配置 AI 服务地址和模型")
	}
	if len(input.Summary) > 1024 || len(input.Format) > 32 || len(input.Signals) > 32 {
		return Decision{}, errors.New("AI 诊断信息超出长度限制")
	}
	for _, signal := range input.Signals {
		if len(signal) > 128 {
			return Decision{}, errors.New("AI 诊断信息超出长度限制")
		}
	}
	diagnostics, _ := json.Marshal(input)
	path, payload := planRequest(config, string(diagnostics))
	data, err := requestAPI(ctx, config, key, http.MethodPost, path, bytes.NewReader(payload), 128<<10)
	if err != nil {
		return Decision{}, err
	}
	content := planReply(config.protocol(), data)
	if content == "" {
		return Decision{}, errors.New("AI 服务未返回有效的对话响应")
	}
	decision, err := parseDecision(content)
	if err == nil && key != "" && strings.Contains(decision.Advice, key) {
		return Decision{}, errors.New("AI 服务在建议中返回了请求密钥，请检查服务配置")
	}
	return decision, err
}

func ValidateKey(key string) error {
	if len(key) > 4096 || strings.IndexFunc(key, unicode.IsControl) >= 0 || strings.IndexFunc(key, unicode.IsSpace) >= 0 {
		return errors.New("AI 服务密钥格式无效")
	}
	return nil
}

func requestAPI(ctx context.Context, config Config, key, method, path string, body io.Reader, limit int64) ([]byte, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(config.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, errors.New("无法创建 AI 服务请求，请检查服务地址")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	authHeader, prefix := "Authorization", "Bearer "
	if config.protocol() == "anthropic" {
		authHeader, prefix = "x-api-key", ""
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if key != "" {
		req.Header.Set(authHeader, prefix+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("AI 服务响应超时，请稍后重试")
		}
		return nil, errors.New("连接 AI 服务失败，请检查服务地址和网络")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, errors.New("AI 服务拒绝访问，请检查 API 密钥及模型权限")
		case http.StatusNotFound:
			return nil, errors.New("AI 服务未找到接口或模型，请检查基础地址和接口协议")
		case http.StatusTooManyRequests:
			return nil, errors.New("AI 服务已限流或额度不足，请检查额度或稍后重试")
		default:
			return nil, fmt.Errorf("AI 服务请求失败（HTTP %d）", resp.StatusCode)
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("无法读取 AI 服务响应")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("AI 服务响应超出长度限制")
	}
	return data, nil
}

func parseDecision(content string) (Decision, error) {
	content = strings.TrimSpace(content)
	// Some compatible services wrap JSON in a Markdown code block.
	if fenced, ok := strings.CutPrefix(content, "```"); ok && strings.HasSuffix(fenced, "```") {
		lang, body, ok := strings.Cut(fenced, "\n")
		if ok && (strings.TrimSpace(lang) == "" || strings.EqualFold(strings.TrimSpace(lang), "json")) {
			content = strings.TrimSuffix(body, "```")
		}
	}
	var result Decision
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return Decision{}, errors.New("AI 未返回有效的下载策略")
	}
	switch result.Action {
	case "network", "combined", "ipv4", "stop":
	default:
		return Decision{}, errors.New("AI 返回了不支持的下载策略")
	}
	result.Advice = strings.Join(strings.Fields(result.Advice), " ")
	if result.Advice == "" || utf8.RuneCountInString(result.Advice) > 600 || strings.IndexFunc(result.Advice, unicode.IsControl) >= 0 {
		return Decision{}, errors.New("AI 未返回有效的处理建议")
	}
	return result, nil
}
