package download

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yt-downloader/internal/deps"
)

func TestNormalizeYouTube(t *testing.T) {
	want := "https://www.youtube.com/watch?v=5OiyIV85E2w"
	for _, u := range []string{`https\://www\.youtube.com/watch?t=15s&v=5OiyIV85E2w\`, "https://youtu.be/5OiyIV85E2w?t=15", "https://www.youtube.com/embed/5OiyIV85E2w", "https://youtube.com/shorts/5OiyIV85E2w"} {
		got, err := NormalizeURL(u)
		if err != nil || got != want {
			t.Fatalf("normalize %s: %s %v", u, got, err)
		}
	}
}

func binaries(t *testing.T) deps.Paths {
	t.Helper()
	locate := func(name string) string {
		b, err := exec.LookPath(name)
		if err != nil {
			t.Skip("integration fixture requires " + name)
		}
		return b
	}
	return deps.Paths{FFmpeg: locate("ffmpeg"), FFprobe: locate("ffprobe"), YTDLP: locate("yt-dlp")}
}
func fixture(t *testing.T, tools deps.Paths) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "sample.mp4")
	b, err := exec.Command(tools.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "color=gray:s=32x32:r=5", "-f", "lavfi", "-i", "anullsrc", "-t", "1", "-c:v", "mpeg4", "-c:a", "aac", file).CombinedOutput()
	if err != nil {
		t.Fatalf("generate sample: %s %v", b, err)
	}
	return file
}
func TestDownloadOfflineRestoreAndCorruption(t *testing.T) {
	tools := binaries(t)
	src := fixture(t, tools)
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.ServeFile(w, r, src) }))
	defer s.Close()
	raw := s.URL + "/clip.mp4"
	root := t.TempDir()
	out := t.TempDir()
	ctx := context.Background()
	var log bytes.Buffer
	run := func(raw, out string) Result {
		t.Helper()
		r, err := New(root).Run(ctx, raw, out, tools, &log, nil)
		if err != nil {
			t.Fatalf("run failed: %v\n%s", err, &log)
		}
		return r
	}
	first := run(raw, out)
	if first.Cached || requests.Load() == 0 {
		t.Fatal("expected real download")
	}
	n := requests.Load()
	restored := run(raw, t.TempDir())
	if !restored.Cached || requests.Load() != n {
		t.Fatal("cache restore contacted source")
	}
	// Output copies cannot corrupt the cache. Preserve the changed file even after a restart.
	if err := os.WriteFile(first.Path, []byte("user-edited"), 0600); err != nil {
		t.Fatal(err)
	}
	other := run(raw, out)
	if !other.Cached || other.Path == first.Path || requests.Load() != n {
		t.Fatal("expected non-overwriting restore")
	}
	again := run(raw, out)
	if again.Path != other.Path || requests.Load() != n {
		t.Fatal("expected reuse of collision-safe filename")
	}
	// A corrupted cached object must never be served, even if the file size is unchanged.
	objects, err := filepath.Glob(filepath.Join(root, "objects", "*", "video.mp4"))
	if err != nil || len(objects) != 1 {
		t.Fatal(objects, err)
	}
	b, err := os.ReadFile(objects[0])
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 1
	if err = os.WriteFile(objects[0], b, 0600); err != nil {
		t.Fatal(err)
	}
	fresh := run(raw, t.TempDir())
	if fresh.Cached || requests.Load() == n {
		t.Fatal("corrupt cache was trusted")
	}
}
func TestLegacyImportWithoutNetwork(t *testing.T) {
	tools := binaries(t)
	src := fixture(t, tools)
	out := t.TempDir()
	root := t.TempDir()
	dst := filepath.Join(out, "旧视频 [5OiyIV85E2w].mp4")
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	tools.YTDLP = "must-not-run"
	var log bytes.Buffer
	first, err := New(root).Run(context.Background(), "https://youtu.be/5OiyIV85E2w", out, tools, &log, nil)
	if err != nil || !first.Cached || first.Path != dst {
		t.Fatal(err, first, log.String())
	}
	if err = os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	second, hit, err := New(root).TryCached(context.Background(), "https://youtube.com/watch?t=15s&v=5OiyIV85E2w", out, &log, nil)
	if err != nil || !hit || !second.Cached || second.Path != dst {
		t.Fatal(err, second)
	}
}
func TestFailureKeepsDiagnostics(t *testing.T) {
	tools := binaries(t)
	x := httptest.NewServer(http.NotFoundHandler())
	defer x.Close()
	var log bytes.Buffer
	_, err := New(t.TempDir()).Run(context.Background(), x.URL+"/gone", t.TempDir(), tools, &log, nil)
	if err == nil || !strings.Contains(log.String(), "404") || !strings.Contains(log.String(), "exit status") {
		t.Fatalf("missing diagnostics: %v %s", err, &log)
	}
}

type throttled struct{ http.ResponseWriter }

func (w throttled) Write(b []byte) (int, error) {
	n := 0
	for len(b) > 0 {
		part := len(b)
		if part > 32<<10 {
			part = 32 << 10
		}
		written, err := w.ResponseWriter.Write(b[:part])
		n += written
		if err != nil {
			return n, err
		}
		b = b[written:]
		time.Sleep(20 * time.Millisecond)
	}
	return n, nil
}

func TestInterruptedDownloadResumesWithRange(t *testing.T) {
	tools := binaries(t)
	b, err := os.ReadFile(fixture(t, tools))
	if err != nil {
		t.Fatal(err)
	}
	free := make([]byte, 4<<20)
	binary.BigEndian.PutUint32(free[:4], uint32(len(free)))
	copy(free[4:8], "free")
	b = append(b, free...)
	var resume atomic.Bool
	var slow atomic.Bool
	slow.Store(true)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Range"); strings.HasPrefix(h, "bytes=") && !strings.HasPrefix(h, "bytes=0-") {
			resume.Store(true)
		}
		if slow.Load() {
			w = throttled{w}
		}
		http.ServeContent(w, r, "clip.mp4", time.Unix(0, 0), bytes.NewReader(b))
	}))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	out := t.TempDir()
	u := s.URL + "/clip.mp4"
	var log bytes.Buffer
	_, err = New(root).Run(ctx, u, out, tools, &log, func(msg string) {
		p, _, ok := strings.Cut(msg, "%")
		if ok {
			f, _ := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if f > 5 && f < 100 {
				cancel()
			}
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected interrupted transfer: %v\n%s", err, &log)
	}
	parts, _ := filepath.Glob(filepath.Join(root, "staging", "*", "*.part"))
	if len(parts) == 0 {
		t.Fatal("missing resume file")
	}
	slow.Store(false)
	if _, err = New(root).Run(context.Background(), u, out, tools, &log, nil); err != nil {
		t.Fatalf("resume failed: %v\n%s", err, &log)
	}
	if !resume.Load() {
		t.Fatalf("expected nonzero range request\n%s", &log)
	}
}

func TestQualityProfileKeepsCacheVariants(t *testing.T) {
	tools := binaries(t)
	src := filepath.Join(t.TempDir(), "portrait.mp4")
	cmd := exec.Command(tools.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "color=gray:s=720x1280:r=8", "-t", "1", "-c:v", "mpeg4", src)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create portrait fixture: %v\n%s", err, output)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.ServeFile(w, r, src)
	}))
	defer server.Close()
	ctx := context.Background()
	e := New(t.TempDir())
	raw := server.URL + "/clip.mp4"
	out := t.TempDir()
	original, err := e.Run(ctx, raw, out, tools, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	opt := DefaultOptions()
	opt.MaxResolution, opt.CompatibleMP4 = 480, true
	smaller, err := e.Run(ctx, raw, out, tools, nil, nil, opt)
	if err != nil || smaller.Path == original.Path || smaller.Cached {
		t.Fatalf("expected newly encoded variant: %#v %v", smaller, err)
	}
	probe, err := (&job{ctx: ctx, tools: tools, log: io.Discard}).inspect(smaller.Path, false)
	if video := probe.track("video"); err != nil || video.Width != 480 || video.Height <= video.Width || video.Codec != "h264" {
		t.Fatalf("invalid portrait variant: %#v %v", video, err)
	}
	before := requests.Load()
	opt.Retries, opt.TimeoutSeconds = 0, 60
	cached, hit, err := e.TryCached(ctx, raw, t.TempDir(), nil, nil, opt)
	if err != nil || !hit || !cached.Cached || filepath.Base(cached.Path) != filepath.Base(smaller.Path) || requests.Load() != before {
		t.Fatalf("expected cached rendition after changing network settings: %#v %t %v", cached, hit, err)
	}
}

func TestRecoverIdentifiedEmbedAcrossDestinations(t *testing.T) {
	const id = "M7AFeRw44zQ"
	const article = "https://example.invalid/story"
	tools := binaries(t)
	original := fixture(t, tools)
	firstDir, secondDir := t.TempDir(), t.TempDir()
	local := filepath.Join(firstDir, "Embedded ["+id+"].mp4")
	data, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}
	engine := New(t.TempDir())
	ctx := context.Background()
	res, hit, err := engine.TryIdentified(ctx, article, firstDir, id, tools, nil, nil)
	if err != nil || !hit || !res.Cached || res.Path != local {
		t.Fatalf("failed to recover already downloaded embed: %#v %v %v", res, hit, err)
	}
	// The alias needs no extractor the next time, including when exporting elsewhere.
	res, hit, err = engine.TryCached(ctx, article, secondDir, nil, nil)
	if err != nil || !hit || !res.Cached || filepath.Dir(res.Path) != secondDir {
		t.Fatalf("failed to reuse article alias: %#v %v %v", res, hit, err)
	}
}

func TestDiagnoseDetectedEmbed(t *testing.T) {
	result := Diagnose("WARNING: [generic] Falling back on generic information extractor\nERROR: [youtube] M7AFeRw44zQ: Unable to download API page: [SSL: UNEXPECTED_EOF_WHILE_READING]")
	if result.YouTubeID != "M7AFeRw44zQ" || result.Summary != "YouTube 加密连接被提前断开" {
		t.Fatalf("unexpected diagnosis %#v", result)
	}
}

func TestCrossFormatDownloadAndCache(t *testing.T) {
	tools := binaries(t)
	dir, out := t.TempDir(), t.TempDir()
	create := func(args ...string) {
		t.Helper()
		full := append([]string{"-nostdin", "-v", "error"}, args...)
		if b, err := exec.Command(tools.FFmpeg, full...).CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, b)
		}
	}
	create("-f", "lavfi", "-i", "color=blue:s=320x180:r=10", "-f", "lavfi", "-i", "sine=frequency=880", "-t", "1", "-c:v", "mpeg4", "-c:a", "pcm_s16le", filepath.Join(dir, "clip.avi"))
	create("-f", "lavfi", "-i", "sine=frequency=440", "-t", "1", filepath.Join(dir, "clip.wav"))
	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer server.Close()
	engine := New(t.TempDir())
	ctx := context.Background()
	check := func(source, format, videoCodec, audioCodec string) Result {
		t.Helper()
		options := DefaultOptions()
		options.OutputFormat = format
		var log bytes.Buffer
		result, err := engine.Run(ctx, server.URL+source, out, tools, &log, nil, options)
		if err != nil {
			t.Fatalf("%s → %s: %v\n%s", source, format, err, log.String())
		}
		if result.Cached || filepath.Ext(result.Path) != "."+format {
			t.Fatalf("unexpected output: %#v", result)
		}
		probe := &job{ctx: ctx, tools: tools, log: io.Discard}
		info, err := probe.inspect(result.Path, true)
		if err != nil || info.codec("video") != videoCodec || info.codec("audio") != audioCodec {
			t.Fatalf("%s output: %#v %v", format, info, err)
		}
		return result
	}
	mkv := check("/clip.avi", "mkv", "mpeg4", "pcm_s16le")
	m4a := check("/clip.avi", "m4a", "", "aac")
	check("/clip.avi", "mp4", "mpeg4", "aac")
	check("/clip.wav", "mp3", "", "mp3")
	server.Close()
	for _, item := range []struct {
		format string
		first  Result
	}{{"mkv", mkv}, {"m4a", m4a}} {
		options := DefaultOptions()
		options.OutputFormat = item.format
		res, hit, err := engine.TryCached(ctx, server.URL+"/clip.avi", t.TempDir(), nil, nil, options)
		if err != nil || !hit || !res.Cached || filepath.Base(res.Path) != filepath.Base(item.first.Path) {
			t.Fatalf("format-aware cache miss: %v %v %v", res, hit, err)
		}
	}
}
