package download

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

func (j *job) convert(path, dir string, input mediaInfo) (string, error) {
	format := j.options.Format()
	dst := filepath.Join(dir, "prepared"+j.extension())
	base := []string{"-nostdin", "-hide_banner", "-y", "-v", "warning", "-i", path}
	outputArgs := func(transcode bool) []string {
		// Always make a new slice so a retry cannot overwrite the first command's options.
		args := append([]string(nil), base...)
		if j.options.AudioOnly() {
			args = append(args, "-map", "0:a:0", "-vn")
		} else {
			args = append(args, "-map", "0:V:0", "-map", "0:a:0?")
			args = append(args, j.videoEncoding(input.track("video"), transcode)...)
		}
		args = append(args, j.audioEncoding(input.codec("audio"), transcode)...)
		if format == "mp4" || format == "m4a" {
			args = append(args, "-movflags", "+faststart")
		}
		return append(args, dst)
	}
	j.stage("封装并校验 " + strings.ToUpper(format))
	var log stderrTail
	diagnostics := io.MultiWriter(j.log, &log)
	err := j.command(j.tools.FFmpeg, outputArgs(false), nil, diagnostics, diagnostics)
	if err != nil && j.ctx.Err() == nil && !j.options.AudioOnly() && incompatibleMux(string(log)) {
		j.stage("容器无法直接封装源编码，改用 H.264 与 AAC")
		err = j.command(j.tools.FFmpeg, outputArgs(true), nil, j.log, j.log)
	}
	if err != nil {
		return "", err
	}
	actual, err := j.inspect(dst, true)
	if err != nil {
		return "", err
	}
	duration, _ := strconv.ParseFloat(input.Format.Duration, 64)
	if !j.validOutput(input, actual, duration) {
		return "", errors.New("输出文件未通过格式、轨道或时长检查")
	}
	return dst, nil
}

func incompatibleMux(log string) bool {
	log = strings.ToLower(log)
	return strings.Contains(log, "could not find tag for codec") ||
		strings.Contains(log, "not supported in container") ||
		strings.Contains(log, "not supported in matroska") ||
		strings.Contains(log, "raw rgb is not supported") ||
		strings.Contains(log, "no wav codec tag found")
}

func (j *job) videoEncoding(video stream, transcode bool) []string {
	cap := j.options.MaxResolution
	scale := cap > 0 && min(video.Width, video.Height) > cap
	copyCodec := j.options.Format() == "mkv"
	if !copyCodec {
		switch video.Codec {
		case "h264", "hevc", "av1", "vp9", "mpeg4":
			copyCodec = true
		}
		if j.options.CompatibleMP4 && (video.Codec != "h264" || video.PixelFormat != "yuv420p") {
			copyCodec = false
		}
	}
	if copyCodec && !scale && !transcode {
		return []string{"-c:v", "copy"}
	}

	// yuv420p requires even dimensions. Only shrink when a cap is exceeded.
	filter := "scale=trunc(iw/2)*2:trunc(ih/2)*2"
	if scale && video.Width <= video.Height {
		filter = fmt.Sprintf("scale=%d:-2", cap)
	} else if scale {
		filter = fmt.Sprintf("scale=-2:%d", cap)
	}
	return []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-pix_fmt", "yuv420p", "-vf", filter}
}

func (j *job) audioEncoding(codec string, transcode bool) []string {
	switch j.options.Format() {
	case "mp3":
		if codec == "mp3" {
			return []string{"-c:a", "copy"}
		}
		return []string{"-c:a", "libmp3lame", "-q:a", "2"}
	case "mkv":
		if !transcode {
			return []string{"-c:a", "copy"}
		}
	}
	if codec == "aac" {
		return []string{"-c:a", "copy"}
	}
	return []string{"-c:a", "aac", "-b:a", "192k"}
}

func (j *job) validOutput(input, output mediaInfo, duration float64) bool {
	switch j.options.Format() {
	case "m4a":
		return output.mp4() && output.hasCompleteTrack("audio", duration) &&
			output.codec("video") == "" && output.codec("audio") == "aac"
	case "mp3":
		return output.isFormat("mp3") && output.hasCompleteTrack("audio", duration) &&
			output.codec("video") == "" && output.codec("audio") == "mp3"
	case "mkv":
		if !output.isFormat("matroska") {
			return false
		}
	default:
		if !output.mp4() || (j.options.CompatibleMP4 && output.codec("video") != "h264") {
			return false
		}
	}
	video := output.track("video")
	cap := j.options.MaxResolution
	return output.complete(duration) && (input.codec("audio") == "" || output.codec("audio") != "") &&
		(cap <= 0 || min(video.Width, video.Height) <= cap)
}
