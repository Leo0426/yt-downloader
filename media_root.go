package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"yt-downloader/internal/download"
)

func openMediaRoot(dataDir, mediaDir string) (*os.Root, error) {
	if !filepath.IsAbs(mediaDir) {
		return nil, errors.New("下载根目录需要使用绝对路径")
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	dataDir, err = filepath.EvalSymlinks(dataDir)
	if err != nil {
		return nil, err
	}
	mediaDir = filepath.Clean(mediaDir)
	// Resolve the existing prefix before creating a new mount directory. An
	// alias to the private data directory must not become an export root.
	parent := mediaDir
	for {
		_, err := os.Lstat(parent)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(parent) == parent {
			return nil, err
		}
		parent = filepath.Dir(parent)
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, err
	}
	// Compare directory identities to cover aliases, bind mounts of the same
	// directory, and case-insensitive filesystems.
	dataInfo, err := os.Stat(dataDir)
	if err != nil {
		return nil, err
	}
	overlap := includesDirectory(resolved, dataInfo)
	if parent == mediaDir && !overlap {
		mediaInfo, err := os.Stat(resolved)
		if err != nil {
			return nil, err
		}
		overlap = includesDirectory(dataDir, mediaInfo)
	}
	if overlap {
		return nil, errors.New("下载根目录和应用数据目录必须互不包含")
	}
	if err = os.MkdirAll(mediaDir, 0755); err != nil {
		return nil, err
	}
	return os.OpenRoot(mediaDir)
}

func includesDirectory(path string, target os.FileInfo) bool {
	for {
		info, err := os.Stat(path)
		if err == nil && os.SameFile(info, target) {
			return true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		path = parent
	}
}

func (a *application) mediaRelative(name string) (string, error) {
	if filepath.IsAbs(name) {
		var err error
		name, err = filepath.Rel(a.downloadRoot, name)
		if err != nil {
			return "", errors.New("保存位置需要在下载根目录内")
		}
	}
	if name == "" || !filepath.IsLocal(name) {
		return "", errors.New("保存位置需要在下载根目录内")
	}
	return filepath.Clean(name), nil
}

// Queue records written on a different host can contain absolute paths that
// are invalid in the new container. Keep the history and never dispatch work
// to the old location; the user can submit its URL to the current destination.
func (a *application) reconcileMediaPaths() error {
	changed := false
	previous := a.OutDir
	if out, err := a.outputDirectory(previous); err == nil {
		a.OutDir = out
	} else {
		a.OutDir = a.downloadRoot
	}
	changed = previous != a.OutDir
	for i := range a.Jobs {
		j := &a.Jobs[i]
		if !filepath.IsLocal(j.ID) || filepath.Base(j.ID) != j.ID || j.ID == "." {
			return errors.New("任务记录中含有无效的任务 ID")
		}
		// Logs belong to the data volume, even when it has a new mount point.
		logPath := filepath.Join(a.root, "logs", j.ID+".log")
		if j.Log != logPath {
			j.Log = logPath
			changed = true
		}
		if j.Status == "queued" || j.Status == "running" {
			dir, err := a.outputDirectory(j.OutDir)
			if err != nil {
				j.Status, j.AIRetry = "failed", nil
				j.Failure = &download.Failure{Summary: "原保存目录不可用", Advice: "请使用当前保存目录重新提交该链接。"}
				j.Message = j.Failure.Summary
				changed = true
			} else if j.OutDir != dir {
				j.OutDir = dir
				changed = true
			}
		}
	}
	if changed {
		return a.save()
	}
	return nil
}

// The final file must be recorded as an immediate child of this job's output
// directory and have one of the formats produced by the engine.
func (a *application) jobFileRelative(j job) (string, bool) {
	if (j.Status != "done" && j.Status != "cached") || !filepath.IsAbs(j.OutDir) || !filepath.IsAbs(j.Path) ||
		filepath.Clean(j.OutDir) != filepath.Dir(j.Path) {
		return "", false
	}
	switch strings.ToLower(filepath.Ext(j.Path)) {
	case ".mp4", ".mkv", ".mp3", ".m4a":
	default:
		return "", false
	}
	rel, err := a.mediaRelative(j.Path)
	return rel, err == nil
}

// The caller has established visibility and jobs.files permission. Stay rooted
// at the mount itself for every lookup, and reject symlinks as deliverables.
func (a *application) openJobFile(j job) (*os.File, error) {
	name, ok := a.jobFileRelative(j)
	if !ok || a.mediaRoot == nil {
		return nil, os.ErrNotExist
	}
	entry, err := a.mediaRoot.Lstat(name)
	if err != nil || !entry.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	f, err := a.mediaRoot.Open(name)
	if err != nil {
		return nil, os.ErrNotExist
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, entry) {
		f.Close()
		return nil, fmt.Errorf("成品已移动或替换: %w", os.ErrNotExist)
	}
	return f, nil
}
