// Package download resolves one video per URL and publishes verified MP4 files.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"yt-downloader/internal/deps"
)

type Engine struct {
	outputRoot *os.Root
	root       string
	mu         sync.Mutex
	pending    map[string]chan struct{}
}
type Result struct {
	Path   string
	Cached bool
}

func New(cacheDir string) *Engine {
	return NewWithOutputRoot(cacheDir, nil)
}

// NewWithOutputRoot confines published media to output when supplied.
// The application keeps the handle alive until all jobs have stopped.
func NewWithOutputRoot(cacheDir string, output *os.Root) *Engine {
	return &Engine{root: cacheDir, outputRoot: output, pending: make(map[string]chan struct{})}
}

type media struct {
	ID        string  `json:"id"`
	Extractor string  `json:"extractor_key"`
	Title     string  `json:"title"`
	Duration  float64 `json:"duration"`
	// Generic IDs can be identical on unrelated sites (e.g. video.mp4).
	Scope string `json:"scope,omitempty"`
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
func (m media) key() string {
	return digest(strings.ToLower(m.Extractor) + "\x00" + m.ID + "\x00" + m.Scope)
}

var youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func videoID(u *url.URL) string {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	var id string
	switch strings.ToLower(u.Hostname()) {
	case "youtu.be":
		if len(parts) == 1 {
			id = parts[0]
		}
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com", "www.youtube-nocookie.com", "youtube-nocookie.com":
		if u.Path == "/watch" {
			id = u.Query().Get("v")
		}
		if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "live" || parts[0] == "v") {
			id = parts[1]
		}
	}
	if !youtubeID.MatchString(id) {
		return ""
	}
	return id
}

// NormalizeURL removes Markdown escapes and playback offsets on YouTube videos.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.NewReplacer(`\:`, ":", `\.`, ".", `\&`, "&", `\?`, "?").Replace(strings.TrimRight(raw, `\`))
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", errors.New("请输入有效的 HTTP 或 HTTPS 视频链接")
	}
	if id := videoID(u); id != "" {
		return "https://www.youtube.com/watch?v=" + id, nil
	}
	return u.String(), nil
}

type job struct {
	*Engine
	ctx      context.Context
	tools    deps.Paths
	log      io.Writer
	progress func(string)
	out, url string
	outRel   string
	options  Options
}

func (j *job) stage(msg string) { fmt.Fprintln(j.log, msg); j.progress(msg) }

func (e *Engine) newJob(ctx context.Context, rawURL, outDir string, log io.Writer, progress func(string), configured []Options) (*job, error) {
	if log == nil {
		log = io.Discard
	}
	if progress == nil {
		progress = func(string) {}
	}
	u, err := NormalizeURL(rawURL)
	if err != nil {
		return nil, err
	}
	out, err := filepath.Abs(outDir)
	if err != nil {
		return nil, err
	}
	var rel string
	if e.outputRoot != nil {
		rel, err = filepath.Rel(e.outputRoot.Name(), out)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, errors.New("任务保存位置超出下载根目录")
		}
		err = e.outputRoot.MkdirAll(rel, 0755)
	} else {
		err = os.MkdirAll(out, 0700)
	}
	if err != nil {
		return nil, err
	}
	options := DefaultOptions()
	if len(configured) > 0 && configured[0].TimeoutSeconds != 0 {
		options = configured[0]
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &job{Engine: e, ctx: ctx, log: log, progress: progress, out: out, outRel: rel, url: u, options: options}, nil
}

func (j *job) aliasPath() string {
	return filepath.Join(j.root, "aliases", digest(j.url)+".json")
}
func (j *job) known() (Result, bool, error) {
	if err := j.ctx.Err(); err != nil {
		return Result{}, false, err
	}
	var m media
	if b, err := os.ReadFile(j.aliasPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if m.ID == "" || m.Extractor == "" {
		parsed, _ := url.Parse(j.url)
		id := videoID(parsed)
		if id == "" {
			return Result{}, false, nil
		}
		m = media{ID: id, Extractor: "Youtube", Title: id}
	}
	unlock, err := j.lockMedia(m)
	if err != nil {
		return Result{}, false, err
	}
	defer unlock()
	return j.reuse(m)
}

// TryCached verifies and publishes an existing object without invoking any component.
// A miss can proceed to component installation and Run to import or download a video.
func (e *Engine) TryCached(ctx context.Context, rawURL, outDir string, log io.Writer, progress func(string), configured ...Options) (Result, bool, error) {
	j, err := e.newJob(ctx, rawURL, outDir, log, progress, configured)
	if err != nil {
		return Result{}, false, err
	}
	return j.known()
}

// TryIdentified checks a YouTube ID previously emitted by the extractor for this URL.
func (e *Engine) TryIdentified(ctx context.Context, rawURL, outDir, id string, tools deps.Paths, log io.Writer, progress func(string), configured ...Options) (Result, bool, error) {
	j, err := e.newJob(ctx, rawURL, outDir, log, progress, configured)
	if err != nil {
		return Result{}, false, err
	}
	j.tools = tools
	return j.recoverYouTube(id)
}

func (j *job) recoverYouTube(id string) (Result, bool, error) {
	if !youtubeID.MatchString(id) {
		return Result{}, false, nil
	}
	m := media{ID: id, Extractor: "Youtube", Title: id}
	unlock, err := j.lockMedia(m)
	if err != nil {
		return Result{}, false, err
	}
	defer unlock()
	j.stage("检查已识别的 YouTube 视频 · " + id)
	result, hit, err := j.reuse(m)
	if hit && err == nil {
		if e := os.MkdirAll(filepath.Dir(j.aliasPath()), 0700); e == nil {
			if e = writeJSON(j.aliasPath(), m); e != nil {
				fmt.Fprintf(j.log, "保存网址映射失败: %v\n", e)
			}
		}
	}
	return result, hit, err
}

func (e *Engine) Run(ctx context.Context, rawURL, outDir string, tools deps.Paths, log io.Writer, progress func(string), configured ...Options) (Result, error) {
	return e.RunWithTitle(ctx, rawURL, outDir, tools, log, progress, nil, configured...)
}

func (e *Engine) RunWithTitle(ctx context.Context, rawURL, outDir string, tools deps.Paths, log io.Writer, progress, onTitle func(string), configured ...Options) (r Result, err error) {
	j, err := e.newJob(ctx, rawURL, outDir, log, progress, configured)
	if err != nil {
		return r, err
	}
	j.tools = tools
	defer func() {
		if err != nil {
			fmt.Fprintf(j.log, "失败: %v\n", err)
		}
	}()
	for _, d := range []string{"objects", "aliases", "staging"} {
		if err = os.MkdirAll(filepath.Join(e.root, d), 0700); err != nil {
			return r, err
		}
	}
	j.stage("检查本地缓存")
	if result, hit, err := j.known(); hit || err != nil {
		return result, err
	}
	j.stage("解析视频")
	m, info, err := j.resolve()
	if err != nil {
		if id := recoveryID(err); id != "" {
			if result, hit, recoverErr := j.recoverYouTube(id); hit || recoverErr != nil {
				return result, recoverErr
			}
		}
		return r, err
	}
	if onTitle != nil && m.Title != "" {
		onTitle(m.Title)
	}
	unlock, err := j.lockMedia(m)
	if err != nil {
		return r, err
	}
	defer unlock()
	if err = writeJSON(j.aliasPath(), m); err != nil {
		return r, err
	}
	if result, hit, err := j.reuse(m); hit || err != nil {
		return result, err
	}
	stage := filepath.Join(e.root, "staging", j.key(m))
	if err = os.MkdirAll(stage, 0700); err != nil {
		return r, err
	}
	built, err := j.fetch(m, info, stage)
	if err != nil {
		return r, err
	}
	record, err := j.store(m, built, j.filename(m))
	if err != nil {
		return r, err
	}
	if err = os.RemoveAll(stage); err != nil {
		fmt.Fprintf(j.log, "清理临时下载文件: %v\n", err)
	}
	path, err := j.publish(m, record)
	return Result{Path: path}, err
}

func sanitize(s string, maxBytes int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, s)
	for len(s) > maxBytes {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	s = strings.Trim(s, " .")
	if s == "" {
		return "video"
	}
	return s
}
func filename(m media) string {
	title := sanitize(m.Title, 140)
	// Windows reserves device names even before a filename extension.
	base := strings.ToUpper(strings.SplitN(title, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || regexp.MustCompile(`^(COM|LPT)[1-9]$`).MatchString(base) {
		title = "_" + title
	}
	return title + " [" + sanitize(m.ID, 64) + "].mp4"
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".new-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
