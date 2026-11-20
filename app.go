package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"yt-downloader/internal/assist"
	"yt-downloader/internal/deps"
	"yt-downloader/internal/download"
)

type job struct {
	OwnerID string            `json:"ownerId,omitempty"`
	Title   string            `json:"title,omitempty"`
	ID      string            `json:"id"`
	URL     string            `json:"url"`
	OutDir  string            `json:"outDir"`
	Status  string            `json:"status"`
	Message string            `json:"message"`
	Path    string            `json:"path"`
	Log     string            `json:"log"`
	Created string            `json:"created"`
	Options download.Options  `json:"options"`
	Failure *download.Failure `json:"failure,omitempty"`
	AIRetry *assistedRetry    `json:"aiRetry,omitempty"`
	AINote  string            `json:"aiNote,omitempty"`
}
type state struct {
	OutDir      string      `json:"outDir"`
	Jobs        []job       `json:"jobs"`
	Preferences preferences `json:"preferences"`
	Paused      bool        `json:"paused"`
}
type application struct {
	identity     *identityStore
	downloadRoot string
	mediaRoot    *os.Root
	mu           sync.Mutex
	state
	ctx                                      context.Context
	cancel                                   context.CancelFunc
	root, cache, maintenance, maintenanceLog string
	updating                                 bool
	maintenanceQueued                        bool
	active                                   map[string]context.CancelFunc
	wake                                     chan struct{}
	wg                                       sync.WaitGroup
	logger                                   *log.Logger
	manager                                  *deps.Manager
	tools                                    []deps.Tool
	engine                                   *download.Engine
	aiKey                                    string // Kept in memory for the currently configured service.
}

func newApplication(parent context.Context, root string, logger *log.Logger) (*application, error) {
	return newApplicationWithDownloadRoot(parent, root, logger, "")
}

func newApplicationWithDownloadRoot(parent context.Context, root string, logger *log.Logger, downloadRoot string) (*application, error) {
	var mediaRoot *os.Root
	var err error
	if downloadRoot != "" {
		mediaRoot, err = openMediaRoot(root, downloadRoot)
		if err != nil {
			return nil, err
		}
		downloadRoot = mediaRoot.Name()
	}
	ready := false
	defer func() {
		if !ready && mediaRoot != nil {
			mediaRoot.Close()
		}
	}()
	cache := filepath.Join(root, "cache")
	for _, p := range []string{cache, filepath.Join(root, "logs")} {
		if err = os.MkdirAll(p, 0700); err != nil {
			return nil, err
		}
	}
	identity, err := newIdentityStore(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	a := &application{downloadRoot: downloadRoot, mediaRoot: mediaRoot, identity: identity, ctx: ctx, cancel: cancel, root: root, cache: cache, active: make(map[string]context.CancelFunc), logger: logger, manager: deps.New(filepath.Join(root, "tools")), engine: download.NewWithOutputRoot(cache, mediaRoot), wake: make(chan struct{}, 1), state: state{Jobs: []job{}, Preferences: defaultPreferences()}, tools: []deps.Tool{}}
	a.OutDir, err = a.defaultOutputDirectory()
	if err != nil {
		cancel()
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, "queue.json"))
	if err == nil {
		if err = json.Unmarshal(data, &a.state); err != nil {
			cancel()
			return nil, fmt.Errorf("读取任务记录失败 %w", err)
		}
		if err := a.Preferences.validate(); err != nil {
			logger.Printf("读取设置时恢复默认值: %v", err)
			a.Preferences = defaultPreferences()
		}
		if mediaRoot != nil {
			if err := a.reconcileMediaPaths(); err != nil {
				cancel()
				return nil, err
			}
		}
		for i := range a.Jobs {
			if a.Jobs[i].Options.TimeoutSeconds == 0 {
				a.Jobs[i].Options = download.DefaultOptions()
			}
			if a.Jobs[i].Status == "failed" && a.Jobs[i].Failure == nil {
				failure := download.ReadFailure(a.Jobs[i].Log, errors.New(a.Jobs[i].Message))
				a.Jobs[i].Failure = &failure
			}
			if a.Jobs[i].Status == "running" {
				a.Jobs[i].Status = "queued"
				a.Jobs[i].Message = "等待恢复下载"
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		cancel()
		return nil, err
	}
	a.wg.Add(1)
	go a.loop()
	a.notify()
	ready = true
	return a, nil
}
func (a *application) close() {
	a.cancel()
	a.wg.Wait()
	if a.mediaRoot != nil {
		a.mediaRoot.Close()
	}
}
func (a *application) notify() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}
func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}
func (a *application) save() error {
	b, err := json.MarshalIndent(a.state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(a.root, "queue.json"), b)
}
func (a *application) checkpoint() {
	if err := a.save(); err != nil {
		a.logger.Printf("保存队列失败: %v", err)
	}
}

// outputDirectory is called under the application lock because it uses the current default and cache.
func (a *application) outputDirectory(out string) (string, error) {
	if out == "" {
		out = a.OutDir
	}
	out = strings.TrimSpace(out)
	if a.mediaRoot != nil {
		rel, err := a.mediaRelative(out)
		if err != nil {
			return "", err
		}
		if err = a.mediaRoot.MkdirAll(rel, 0755); err != nil {
			return "", fmt.Errorf("无法在下载根目录内创建保存位置: %w", err)
		}
		return filepath.Join(a.downloadRoot, rel), nil
	}
	if strings.HasPrefix(out, "~/") || strings.HasPrefix(out, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		out = filepath.Join(home, out[2:])
	}
	if !filepath.IsAbs(out) {
		return "", errors.New("保存目录需要使用绝对路径")
	}
	out = filepath.Clean(out)
	if err := os.MkdirAll(out, 0755); err != nil {
		return "", err
	}
	// Prevent exporting into the application cache even through a directory symlink.
	resolvedOut, err := filepath.EvalSymlinks(out)
	if err != nil {
		return "", err
	}
	if a.cache != "" {
		resolvedCache, err := filepath.EvalSymlinks(a.cache)
		if err != nil {
			return "", err
		}
		if pathInside(resolvedCache, resolvedOut) {
			return "", errors.New("请选择缓存目录之外的保存位置")
		}
	}
	return out, nil
}

func (a *application) add(actor identityPrincipal, raw []string, out string, resolution *int, format string) (int, int, error) {
	if actor.ID == "" || !actor.Can("jobs.create") {
		return 0, 0, errAccessDenied
	}
	if len(raw) == 0 || len(raw) > 500 {
		return 0, 0, errors.New("每批需要 1 至 500 个链接")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !actor.Can("settings.manage") {
		if out != "" && out != a.OutDir {
			return 0, 0, errAccessDenied
		}
		out = a.OutDir
	}
	out, err := a.outputDirectory(out)
	if err != nil {
		return 0, 0, err
	}
	options := a.Preferences.Download
	if options.TimeoutSeconds == 0 {
		options = download.DefaultOptions()
	}
	if resolution != nil {
		options.MaxResolution = *resolution
	}
	if format != "" {
		options.OutputFormat = format
	}
	if err := options.Validate(); err != nil {
		return 0, 0, err
	}
	// Only active entries suppress submissions. Finished entries must be revalidated by the engine.
	seen := map[string]bool{}
	for _, j := range a.Jobs {
		if j.OwnerID == actor.ID && j.OutDir == out && j.Options.Profile() == options.Profile() && (j.Status == "queued" || j.Status == "running") {
			seen[j.URL] = true
		}
	}
	var pending []job
	skipped := 0
	for _, s := range raw {
		u, err := download.NormalizeURL(s)
		if err != nil {
			return 0, 0, err
		}
		key := u
		if seen[key] {
			skipped++
			continue
		}
		seen[key] = true
		var idBytes [12]byte
		if _, err = rand.Read(idBytes[:]); err != nil {
			return 0, 0, err
		}
		id := hex.EncodeToString(idBytes[:])
		pending = append(pending, job{OwnerID: actor.ID, ID: id, URL: u, OutDir: out, Status: "queued", Options: options, Created: time.Now().Format(time.RFC3339), Log: filepath.Join(a.root, "logs", id+".log")})
	}
	previous := a.state
	a.Jobs = append(a.Jobs, pending...)
	a.OutDir = out
	if err := a.save(); err != nil {
		a.state = previous
		return 0, 0, err
	}
	a.notify()
	return len(pending), skipped, nil
}
func (a *application) find(id string) *job {
	for i := range a.Jobs {
		if a.Jobs[i].ID == id {
			return &a.Jobs[i]
		}
	}
	return nil
}

var errAccessDenied = errors.New("当前账户没有此操作权限")

func (j *job) visibleTo(actor identityPrincipal) bool {
	return actor.ID != "" && actor.Can("jobs.view") && (j.OwnerID == actor.ID || actor.Can("jobs.viewAll"))
}

func (a *application) action(actor identityPrincipal, id, action string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	j := a.find(id)
	if j == nil || !j.visibleTo(actor) {
		return os.ErrNotExist
	}
	if !actor.Can("jobs.control") || (j.OwnerID != actor.ID && !actor.Can("jobs.controlAll")) {
		return errAccessDenied
	}
	if action == "ai-retry" && !actor.Can("jobs.ai") {
		return errAccessDenied
	}
	if a.mediaRoot != nil && (action == "retry" || action == "ai-retry") {
		if _, err := a.outputDirectory(j.OutDir); err != nil {
			return fmt.Errorf("原保存目录不可用，请使用当前保存目录重新提交链接: %w", err)
		}
	}
	previous := *j
	switch action {
	case "retry":
		if j.Status != "failed" && j.Status != "canceled" {
			return errors.New("仅失败或取消的任务可重试")
		}
		j.Status = "queued"
		j.Message = "等待重试"
		j.AIRetry, j.AINote = nil, ""
	case "ai-retry":
		if j.Status != "failed" {
			return errors.New("仅失败的任务可启动 AI 辅助下载")
		}
		if a.Preferences.AI.BaseURL == "" || a.Preferences.AI.Model == "" {
			return errors.New("请先在「设置 → AI 辅助」中填写服务地址和模型名称")
		}
		// Older queues may have a diagnosis that predates the diagnostic signals.
		if j.Failure == nil {
			failure := download.ReadFailure(j.Log, errors.New(j.Message))
			j.Failure = &failure
		} else if j.AINote == "" && len(j.Failure.Signals) == 0 {
			failure := *j.Failure
			failure.Signals = download.ReadSignals(j.Log)
			j.Failure = &failure
		}
		j.AIRetry = &assistedRetry{Config: a.Preferences.AI}
		j.Status, j.Message, j.AINote = "queued", "等待 AI 辅助下载", ""
	case "cancel":
		if cancel := a.active[id]; cancel != nil {
			cancel()
			return nil
		}
		if j.Status != "queued" {
			return errors.New("该任务已经结束")
		}
		j.Status = "canceled"
		j.Message = "已取消"
		j.AIRetry = nil
	default:
		return errors.New("未知的任务操作")
	}
	if err := a.save(); err != nil {
		*j = previous
		return err
	}
	a.notify()
	return nil
}

// Build the response from copies so filtering the API can never alter queued work.
func (a *application) snapshot(actor identityPrincipal) map[string]any {
	a.mu.Lock()
	jobs := []job{}
	running := 0
	settings := actor.Can("settings.manage")
	for _, entry := range a.Jobs {
		if !entry.visibleTo(actor) {
			continue
		}
		if entry.Status == "running" {
			running++
		}
		fileVisible := actor.Can("jobs.files")
		if a.mediaRoot != nil && entry.Path != "" {
			_, available := a.jobFileRelative(entry)
			fileVisible = fileVisible && available
		}
		if !fileVisible {
			if entry.Title == "" && entry.Path != "" {
				entry.Title = filepath.Base(entry.Path)
			}
			entry.OutDir, entry.Path = "", ""
		}
		if !actor.Can("storage.reveal") || !actor.Can("jobs.logs") {
			entry.Log = ""
		}
		if !settings && entry.AIRetry != nil {
			entry.AIRetry = &assistedRetry{Strategy: entry.AIRetry.Strategy}
		}
		jobs = append(jobs, entry)
	}
	prefs := a.Preferences
	if !settings {
		prefs.AI = assist.Config{}
	}
	result := map[string]any{
		"session": map[string]any{"user": actor, "setupRequired": false},
		"jobs":    jobs, "preferences": prefs, "paused": a.Paused, "running": running, "busy": running > 0,
	}
	if actor.Can("jobs.create") || settings {
		result["outDir"] = a.OutDir
	}
	if actor.Can("jobs.ai") || settings {
		result["aiConfigured"] = a.Preferences.AI.BaseURL != "" && a.Preferences.AI.Model != ""
	}
	if settings {
		result["aiKeySet"] = a.aiKey != ""
		if a.mediaRoot != nil {
			result["downloadRoot"] = a.downloadRoot
		}
	}
	if actor.Can("storage.manage") {
		result["cacheDir"] = a.cache
	}
	if actor.Can("tools.manage") {
		result["tools"] = append([]deps.Tool{}, a.tools...)
		result["maintenance"], result["maintenanceLog"] = a.maintenance, a.maintenanceLog
	}
	if actor.Can("tools.manage") || actor.Can("storage.manage") || actor.Can("queue.manage") {
		result["busy"] = a.updating || len(a.active) > 0 || a.maintenanceQueued
	}
	a.mu.Unlock()
	if actor.Can("jobs.view") && actor.Can("jobs.viewAll") {
		ids := make([]string, 0, len(jobs))
		for _, entry := range jobs {
			if entry.OwnerID != "" {
				ids = append(ids, entry.OwnerID)
			}
		}
		result["ownerNames"] = a.identity.ownerNames(ids)
	}
	return result
}

func (a *application) update() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.updating || a.maintenanceQueued {
		return errors.New("正在检查组件")
	}
	a.maintenanceQueued = true
	a.maintenance = "等待检查更新"
	a.notify()
	return nil
}
func (a *application) refreshTools() {
	list := a.manager.Status(a.ctx)
	a.mu.Lock()
	a.tools = list
	a.mu.Unlock()
}
func (a *application) loop() {
	defer a.wg.Done()
	var workers sync.WaitGroup
	defer workers.Wait()
	a.refreshTools()
	for a.ctx.Err() == nil {
		a.mu.Lock()
		if a.maintenanceQueued && len(a.active) == 0 {
			a.updating = true
			a.maintenanceQueued = false
			a.maintenance = "正在检查并更新组件"
			a.maintenanceLog = ""
			a.mu.Unlock()
			a.runUpdate()
			continue
		}
		var next job
		if !a.Paused && !a.maintenanceQueued && len(a.active) < a.Preferences.Concurrency {
			for i := range a.Jobs {
				if a.Jobs[i].Status == "queued" {
					a.Jobs[i].Status = "running"
					a.Jobs[i].Message = "检查工具和缓存"
					next = a.Jobs[i]
					break
				}
			}
		}
		if next.ID == "" {
			a.mu.Unlock()
			select {
			case <-a.wake:
			case <-a.ctx.Done():
			}
			continue
		}
		ctx, cancel := context.WithCancel(a.ctx)
		a.active[next.ID] = cancel
		a.checkpoint()
		a.mu.Unlock()
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer cancel()
			a.runJob(ctx, next)
		}()
	}
}
func (a *application) runUpdate() {
	f, err := os.OpenFile(filepath.Join(a.root, "logs", "tools.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	var display tailWriter
	if err == nil {
		fmt.Fprintf(f, "\n[%s] 检查更新\n", time.Now().Format(time.RFC3339))
		_, err = a.manager.Update(a.ctx, io.MultiWriter(f, &display))
		if err != nil {
			fmt.Fprintln(f, err)
		}
		f.Close()
	}
	a.refreshTools()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updating = false
	a.maintenanceLog = string(display)
	if err != nil {
		a.maintenance = "更新失败 · " + err.Error()
	} else {
		a.maintenance = "组件已是最新版本"
	}
}

type tailWriter []byte

func (t *tailWriter) Write(b []byte) (int, error) {
	*t = append(*t, b...)
	if len(*t) > 16384 {
		*t = append([]byte(nil), (*t)[len(*t)-16384:]...)
	}
	return len(b), nil
}

func (a *application) runJob(ctx context.Context, j job) {
	f, err := os.OpenFile(j.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	var result download.Result
	var planErr error
	if err == nil {
		fmt.Fprintf(f, "\n[%s] %s\n保存至 %s\n", time.Now().Format(time.RFC3339), j.URL, j.OutDir)
		progress := func(msg string) { a.mu.Lock(); a.find(j.ID).Message = msg; a.mu.Unlock() }
		var hit bool
		result, hit, err = a.engine.TryCached(ctx, j.URL, j.OutDir, f, progress, j.Options)
		if !hit && err == nil && j.AIRetry != nil {
			if j.AIRetry.Strategy == "" {
				progress("AI 正在分析上次下载失败的原因")
				fmt.Fprintln(f, "AI 辅助下载 · 分析上次下载失败的原因")
			}
			j.Options.RecoveryStrategy, planErr = a.planRetry(ctx, j)
			err = planErr
			if err == nil {
				msg := "AI 辅助下载 · " + strategyLabel(j.Options.RecoveryStrategy)
				progress(msg)
				fmt.Fprintln(f, msg)
			}
		}
		if !hit && err == nil {
			var paths deps.Paths
			paths, err = a.manager.Ensure(ctx, f)
			if err == nil && j.Failure != nil && j.Failure.YouTubeID != "" {
				result, hit, err = a.engine.TryIdentified(ctx, j.URL, j.OutDir, j.Failure.YouTubeID, paths, f, progress, j.Options)
			}
			if !hit && err == nil {
				result, err = a.engine.RunWithTitle(ctx, j.URL, j.OutDir, paths, f, progress, func(title string) {
					a.mu.Lock()
					defer a.mu.Unlock()
					a.find(j.ID).Title = title
				}, j.Options)
			}
		}
		if err != nil {
			fmt.Fprintf(f, "[%s] %v\n", time.Now().Format(time.RFC3339), err)
		} else {
			fmt.Fprintln(f, "完成", result.Path)
		}
		f.Close()
	}
	var failure *download.Failure
	if planErr != nil && j.Failure != nil {
		failure = j.Failure
	} else if err != nil {
		diagnosed := download.ReadFailure(j.Log, err)
		failure = &diagnosed
	}
	if a.ctx.Err() == nil && planErr == nil {
		a.refreshTools()
	}
	a.mu.Lock()
	current := a.find(j.ID)
	switch {
	case err == nil:
		current.Failure = nil
		current.Status = "done"
		current.Message = "下载完成"
		current.Path = result.Path
		if result.Cached {
			current.Status = "cached"
			current.Message = "已复用本地视频"
		}
	case a.ctx.Err() != nil:
		current.Status = "queued"
		current.Message = "下次启动时继续"
	case ctx.Err() != nil:
		current.Status = "canceled"
		current.Message = "已取消，可重试"
	default:
		current.Status = "failed"
		current.Failure = failure
		current.Message = failure.Summary
		if planErr != nil {
			current.AINote = planErr.Error()
		}
	}
	if current.Status != "queued" {
		current.AIRetry = nil
	}
	delete(a.active, j.ID)
	a.checkpoint()
	a.mu.Unlock()
	a.notify()
}
