package download

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

type Failure struct {
	Summary   string   `json:"summary"`
	Advice    string   `json:"advice"`
	YouTubeID string   `json:"youtubeID,omitempty"`
	Signals   []string `json:"signals,omitempty"`
}

var youtubeFailure = regexp.MustCompile(`(?m)^(?:ERROR|WARNING): \[youtube\] ([A-Za-z0-9_-]{11}):[ \t]`)
var failureAdvice = "查看完整日志后重试。若持续失败，可以在「设置 → 组件」检查更新。"

// Only these fixed labels may leave the downloader. Actual messages can contain
// signed URLs, video titles, account details and local file paths.
var diagnosticMarkers = []struct {
	label   string
	markers []string
}{
	{"auth_required", []string{"sign in to confirm", "sign in if", "login required", "log in to", "requires authentication", "private video", "members-only", "join this channel", "http error 401"}},
	{"drm_protected", []string{"drm protected", "drm-protected", "protected by drm"}},
	{"unavailable", []string{"video unavailable", "video has been removed", "video is unavailable", "video is no longer available", "account has been terminated"}},
	{"geo_restricted", []string{"not available in your country", "not available in your region", "geo-restricted"}},
	{"http_429", []string{"http error 429", "429 too many requests"}},
	{"http_403", []string{"http error 403", "403 forbidden"}},
	{"http_404", []string{"http error 404", "404 not found"}},
	{"disk_full", []string{"no space left on device", "not enough space on the disk"}},
	{"permission_denied", []string{"permission denied", "operation not permitted", "access is denied"}},
	{"timeout", []string{"timed out", "deadline exceeded"}},
	{"connection_reset", []string{"unexpected_eof_while_reading", "connection reset by peer", "connectionreseterror", "connection aborted"}},
	{"network_unreachable", []string{"network is unreachable", "no route to host"}},
	{"ipv6", []string{"ipv6", "dial tcp6"}},
	{"dns_error", []string{"name resolution", "name or service not known", "no such host", "getaddrinfo failed", "nodename nor servname provided"}},
	{"format_unavailable", []string{"requested format is not available", "no video formats found"}},
	{"unsupported_source", []string{"unsupported url", "no video could be found"}},
	{"missing_audio", []string{"这个来源没有可用的音轨"}},
	{"missing_video", []string{"这个来源没有视频轨道"}},
	{"incomplete_media", []string{"下载文件的时长不完整", "下载文件不完整"}},
	{"conversion_error", []string{"conversion failed", "error opening encoder", "encoder not found", "not supported in container", "could not find tag for codec", "输出文件未通过格式、轨道或时长检查"}},
	{"upcoming_or_live", []string{"视频仍在直播或等待开播"}},
}

func diagnosticSignals(text string) []string {
	var detail strings.Builder
	for line := range strings.SplitSeq(text, "\n") {
		if !strings.HasPrefix(line, "$ ") {
			detail.WriteString(strings.ToLower(line))
			detail.WriteByte('\n')
		}
	}
	log := detail.String()
	var signals []string
	for _, group := range diagnosticMarkers {
		for _, marker := range group.markers {
			if strings.Contains(log, marker) {
				signals = append(signals, group.label)
				break
			}
		}
	}
	return signals
}

func Diagnose(text string) Failure {
	signals := diagnosticSignals(text)
	f := explainFailure(text, signals)
	f.Signals = signals
	return f
}

func explainFailure(text string, signals []string) Failure {
	has := func(signal string) bool { return slices.Contains(signals, signal) }
	switch {
	case has("missing_video"):
		return Failure{Summary: "来源没有视频轨道", Advice: "把输出格式改为 M4A 或 MP3，再重新提交该地址。"}
	case has("missing_audio"):
		return Failure{Summary: "来源没有可用的音轨", Advice: "改用 MP4 或 MKV 下载画面，或检查嗅探列表是否有单独的音频地址。"}
	case has("drm_protected"):
		return Failure{Summary: "来源受到 DRM 保护", Advice: "请使用网站提供的播放或官方离线功能。"}
	case has("auth_required"):
		return Failure{Summary: "来源要求登录或验证身份", Advice: "请在网站确认账号是否能访问该视频，再使用网站提供的下载方式。"}
	case has("geo_restricted"):
		return Failure{Summary: "来源限制了当前地区的访问", Advice: "请在网站查看该视频的地区可用性。"}
	case has("unavailable"):
		return Failure{Summary: "来源视频已不可用", Advice: "在原网页确认视频是否仍可播放，并检查链接。"}
	case has("format_unavailable"):
		return Failure{Summary: "源站未提供所选格式需要的轨道", Advice: "尝试降低分辨率，或者在 MP4、MKV 和音频格式之间切换后重新提交。"}
	case has("connection_reset"):
		f := Failure{
			Summary: "下载连接被提前断开",
			Advice:  "确认当前网络能打开视频，再重试。若使用了代理，检查其 YouTube 路由。",
		}
		for _, m := range youtubeFailure.FindAllStringSubmatch(text, -1) {
			if f.YouTubeID != "" && f.YouTubeID != m[1] {
				f.YouTubeID = ""
				break
			}
			f.YouTubeID = m[1]
		}
		if f.YouTubeID != "" {
			f.Summary = "YouTube 加密连接被提前断开"
			f.Advice = "重试时会优先检查本地是否已有同一视频。需要重新下载时，请确认当前网络能打开 YouTube。"
		}
		return f
	case has("timeout"):
		return Failure{Summary: "连接源站超时", Advice: "检查网络，或降低并发、提高连接超时后重新提交。"}
	case has("http_429"):
		return Failure{Summary: "源站限制了请求频率（HTTP 429）", Advice: "稍后重试，并在设置中降低同时下载的任务数。"}
	case has("http_403"):
		return Failure{Summary: "源站拒绝了下载请求（HTTP 403）", Advice: "请检查原网页的访问权限。从网页嗅探获取的临时地址可能已过期。"}
	case has("http_404"):
		return Failure{Summary: "找不到源文件（HTTP 404）", Advice: "确认链接仍然有效，再重新提交。"}
	case has("unsupported_source"):
		return Failure{Summary: "未找到可下载的视频", Advice: "尝试粘贴视频本身的链接，或在设置中检查组件更新。"}
	case has("disk_full"):
		return Failure{Summary: "磁盘空间不足", Advice: "腾出缓存盘和保存目录所在磁盘的空间，再重试。"}
	case has("permission_denied"):
		return Failure{Summary: "没有读取或写入文件的权限", Advice: "检查系统的下载文件夹访问权限，或选择可写入的目录。"}
	}
	return Failure{Summary: "任务未完成", Advice: failureAdvice}
}

// Read the bounded tail of the most recent attempt in this job's append-only log.
func latestAttemptLog(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	data := make([]byte, min(info.Size(), 64<<10))
	n, _ := f.ReadAt(data, info.Size()-int64(len(data)))
	text := string(data[:n])
	if i := strings.LastIndex(text, "\n保存至 "); i >= 0 {
		_, text, _ = strings.Cut(text[i+1:], "\n")
	}
	return text
}

// ReadSignals supports saved diagnoses from before assisted retries.
func ReadSignals(path string) []string {
	return diagnosticSignals(latestAttemptLog(path))
}

func ReadFailure(path string, cause error) Failure {
	text := latestAttemptLog(path)
	if cause != nil {
		text += "\n" + cause.Error()
	}
	return Diagnose(text)
}

type resolutionFailure struct {
	err       error
	diagnosis Failure
}

func (e *resolutionFailure) Error() string { return fmt.Sprintf("%s: %v", e.diagnosis.Summary, e.err) }
func (e *resolutionFailure) Unwrap() error { return e.err }

func recoveryID(err error) string {
	var failure *resolutionFailure
	if errors.As(err, &failure) {
		return failure.diagnosis.YouTubeID
	}
	return ""
}
