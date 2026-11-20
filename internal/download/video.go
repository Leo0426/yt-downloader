package download

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const videoSelector = "bv[protocol=https]+ba[protocol=https]/b[protocol=https]/bv+ba/b/bv"

func (j *job) args() []string {
	format := videoSelector
	if j.options.AudioOnly() {
		format = "ba/b/ba*"
	}
	if j.options.RecoveryStrategy == "combined" {
		// In yt-dlp, b selects a single format containing both video and audio.
		format = "b[protocol=https]/b/" + format
	}
	timeout, retries := j.options.TimeoutSeconds, j.options.Retries
	if j.options.RecoveryStrategy == "network" {
		timeout = min(180, max(60, timeout*2))
		retries = min(10, max(3, retries+2))
	}
	args := []string{"--ignore-config", "--no-playlist", "--playlist-end", "1", "--socket-timeout", strconv.Itoa(timeout), "--retries", strconv.Itoa(retries), "--fragment-retries", strconv.Itoa(retries), "--extractor-retries", strconv.Itoa(retries), "--abort-on-unavailable-fragments", "--cache-dir", filepath.Join(j.root, "extractor"), "--ffmpeg-location", j.tools.FFmpeg, "-f", format}
	switch j.options.RecoveryStrategy {
	case "network":
		args = append(args, "--retry-sleep", "http:exp=1:8", "--retry-sleep", "fragment:exp=1:8", "--retry-sleep", "extractor:exp=1:8")
	case "ipv4":
		args = append(args, "--force-ipv4")
	}
	if !j.options.AudioOnly() && j.options.MaxResolution > 0 {
		args = append(args, "-S", "res:"+strconv.Itoa(j.options.MaxResolution))
	}
	if j.options.RateLimitKiB > 0 {
		args = append(args, "--limit-rate", strconv.Itoa(j.options.RateLimitKiB)+"K")
	}
	if j.tools.Deno != "" {
		args = append(args, "--js-runtimes", "deno:"+j.tools.Deno)
	}
	return args
}
func (j *job) resolve() (media, json.RawMessage, error) {
	b, err := j.output(j.tools.YTDLP, append(j.args(), "--dump-single-json", "--", j.url))
	if err != nil {
		return media{}, nil, err
	}
	b, err = firstEntry(b)
	if err != nil {
		return media{}, nil, err
	}
	var m media
	if err = json.Unmarshal(b, &m); err != nil {
		return media{}, nil, err
	}
	if m.ID == "" || m.Extractor == "" {
		return media{}, nil, errors.New("没有找到可下载的视频")
	}
	if strings.EqualFold(m.Extractor, "generic") {
		m.Scope = digest(j.url)
	}
	var live struct {
		IsLive bool   `json:"is_live"`
		Status string `json:"live_status"`
	}
	if err = json.Unmarshal(b, &live); err != nil {
		return media{}, nil, err
	}
	if live.IsLive || live.Status == "is_upcoming" {
		return media{}, nil, errors.New("视频仍在直播或等待开播，请在直播结束后重试")
	}
	return m, b, nil
}

// A video can be wrapped in several transparent collections by site extractors.
func firstEntry(metadata json.RawMessage) (json.RawMessage, error) {
	for depth := 0; depth < 12; depth++ {
		var wrapper struct {
			Entries []json.RawMessage `json:"entries"`
		}
		if err := json.Unmarshal(metadata, &wrapper); err != nil {
			return nil, fmt.Errorf("解析 yt-dlp 元数据: %w", err)
		}
		if wrapper.Entries == nil {
			return metadata, nil
		}
		metadata = nil
		for _, entry := range wrapper.Entries {
			if len(entry) != 0 && string(entry) != "null" {
				metadata = entry
				break
			}
		}
		if metadata == nil {
			break
		}
	}
	return nil, errors.New("没有找到可下载的媒体")
}

func (j *job) fetch(m media, info json.RawMessage, dir string) (string, error) {
	var selected struct {
		Format string `json:"format_id"`
	}
	if err := json.Unmarshal(info, &selected); err != nil {
		return "", err
	}
	prefix := "source-" + digest(selected.Format)[:16]
	// Pipe freshly resolved metadata directly to yt-dlp; signed URLs need not live on disk.
	pathfile := filepath.Join(dir, "downloaded-path")
	if err := os.Remove(pathfile); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	args := append(j.args(), "--continue", "--part", "--no-quiet", "--progress", "--newline", "--progress-delta", "1", "--no-simulate", "--merge-output-format", "mkv", "-o", filepath.Join(strings.ReplaceAll(dir, "%", "%%"), prefix+".%(ext)s"), "--print-to-file", "after_move:filepath", strings.ReplaceAll(pathfile, "%", "%%"), "--load-info-json", "-")
	j.stage("下载媒体（自动续传）")
	w := &progressLog{out: j.log, notify: j.progress}
	if err := j.command(j.tools.YTDLP, args, bytes.NewReader(info), w, w); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(pathfile)
	if err != nil {
		return "", err
	}
	path := filepath.Clean(strings.TrimSpace(string(raw)))
	if filepath.Dir(path) != filepath.Clean(dir) || !strings.HasPrefix(filepath.Base(path), prefix+".") {
		return "", errors.New("下载工具返回了意外的文件路径")
	}
	probe, err := j.inspect(path, false)
	requiredTrack := "video"
	if j.options.AudioOnly() {
		requiredTrack = "audio"
	}
	if err == nil && probe.codec(requiredTrack) == "" {
		if requiredTrack == "audio" {
			return "", errors.New("这个来源没有可用的音轨")
		}
		return "", errors.New("这个来源没有视频轨道，可以尝试 M4A 或 MP3 输出")
	}
	if err != nil || !probe.hasCompleteTrack(requiredTrack, m.Duration) {
		if j.ctx.Err() != nil {
			return "", j.ctx.Err()
		}
		os.Remove(path) // A completed but corrupt intermediate cannot be resumed.
		if err != nil {
			return "", fmt.Errorf("下载文件不完整: %w", err)
		}
		return "", errors.New("下载文件的时长不完整，重试将重新获取媒体")
	}
	return j.convert(path, dir, probe)
}

type stream struct {
	Codec       string `json:"codec_name"`
	Type        string `json:"codec_type"`
	Packets     string `json:"nb_read_packets"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	PixelFormat string `json:"pix_fmt"`
	Disposition struct {
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
}
type mediaInfo struct {
	Format struct {
		Name     string `json:"format_name"`
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []stream `json:"streams"`
}

func (p mediaInfo) track(kind string) stream {
	for _, s := range p.Streams {
		if s.Type == kind && s.Disposition.AttachedPic == 0 {
			return s
		}
	}
	return stream{}
}
func (p mediaInfo) codec(kind string) string { return p.track(kind).Codec }
func (p mediaInfo) isFormat(format string) bool {
	for _, name := range strings.Split(p.Format.Name, ",") {
		if name == format {
			return true
		}
	}
	return false
}
func (p mediaInfo) mp4() bool                  { return p.isFormat("mp4") }
func (p mediaInfo) complete(want float64) bool { return p.hasCompleteTrack("video", want) }
func (p mediaInfo) hasCompleteTrack(kind string, want float64) bool {
	d, err := strconv.ParseFloat(p.Format.Duration, 64)
	return err == nil && !math.IsInf(d, 0) && d > 0 && p.codec(kind) != "" && (want <= 0 || math.Abs(d-want) <= math.Max(2, want*0.015))
}
func (j *job) inspect(path string, deep bool) (mediaInfo, error) {
	args := []string{"-v", "error"}
	if deep {
		args = append(args, "-count_packets")
	}
	args = append(args, "-show_entries", "format=format_name,duration:stream=codec_type,codec_name,nb_read_packets,width,height,pix_fmt:stream_disposition=attached_pic", "-of", "json", "-i", path)
	var out, stderr bytes.Buffer
	var p mediaInfo
	if err := j.command(j.tools.FFprobe, args, nil, &out, io.MultiWriter(j.log, &stderr)); err != nil {
		return p, err
	}
	if stderr.Len() > 0 {
		return p, fmt.Errorf("ffprobe: %s", strings.TrimSpace(stderr.String()))
	}
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		return p, err
	}
	if deep {
		for _, s := range p.Streams {
			if s.Disposition.AttachedPic == 0 && (s.Type == "video" || s.Type == "audio") {
				n, _ := strconv.Atoi(s.Packets)
				if n <= 0 {
					return p, errors.New("媒体轨道为空")
				}
			}
		}
	}
	return p, nil
}
