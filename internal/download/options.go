package download

import (
	"errors"
	"strconv"
	"strings"
)

// Options are captured when a task is queued so retries retain its settings.
type Options struct {
	OutputFormat   string `json:"format,omitempty"`
	MaxResolution  int    `json:"maxResolution"`
	CompatibleMP4  bool   `json:"compatibleMP4"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	Retries        int    `json:"retries"`
	RateLimitKiB   int    `json:"rateLimitKiB"`
	// RecoveryStrategy applies only to the current assisted download attempt.
	RecoveryStrategy string `json:"-"`
}

func DefaultOptions() Options { return Options{TimeoutSeconds: 25, Retries: 3} }

// Format treats jobs and settings saved before format selection as MP4.
func (o Options) Format() string {
	if o.OutputFormat == "" {
		return "mp4"
	}
	return o.OutputFormat
}

func (o Options) AudioOnly() bool { return o.Format() == "m4a" || o.Format() == "mp3" }

func (o Options) Validate() error {
	switch o.RecoveryStrategy {
	case "", "network", "combined", "ipv4":
	default:
		return errors.New("未知的辅助下载策略")
	}
	switch o.Format() {
	case "mp4", "mkv", "m4a", "mp3":
	default:
		return errors.New("请选择有效的输出格式")
	}
	if !ValidResolution(o.MaxResolution) {
		return errors.New("请选择有效的视频分辨率")
	}
	if o.TimeoutSeconds < 5 || o.TimeoutSeconds > 180 {
		return errors.New("连接超时需在 5 到 180 秒之间")
	}
	if o.Retries < 0 || o.Retries > 10 {
		return errors.New("自动重试次数需在 0 到 10 之间")
	}
	if o.RateLimitKiB < 0 || o.RateLimitKiB > 1048576 {
		return errors.New("限速需在 0 到 1048576 KiB/s 之间")
	}
	return nil
}

func ValidResolution(size int) bool {
	switch size {
	case 0, 480, 720, 1080, 1440, 2160:
		return true
	}
	return false
}

func (o Options) rendition() string {
	if o.AudioOnly() {
		return ""
	}
	var parts []string
	if o.MaxResolution != 0 {
		parts = append(parts, strconv.Itoa(o.MaxResolution)+"p")
	}
	if o.Format() == "mp4" && o.CompatibleMP4 {
		parts = append(parts, "H264")
	}
	return strings.Join(parts, " ")
}

// Profile only describes output identity. The existing MP4 cache keys are stable.
func (o Options) Profile() string {
	if o.Format() == "mp4" {
		return o.rendition()
	}
	return strings.TrimSpace(o.Format() + " " + o.rendition())
}

func (j *job) key(m media) string {
	if profile := j.options.Profile(); profile != "" {
		return digest(m.key() + "\x00" + profile)
	}
	return m.key()
}

func (j *job) extension() string      { return "." + j.options.Format() }
func (j *job) objectFilename() string { return "video" + j.extension() }

func (j *job) filename(m media) string {
	stem := strings.TrimSuffix(filename(m), ".mp4")
	if label := j.options.rendition(); label != "" {
		stem += " (" + label + ")"
	}
	return stem + j.extension()
}
