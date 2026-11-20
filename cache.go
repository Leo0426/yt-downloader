package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

type cacheStats struct {
	Bytes        int64 `json:"bytes"`
	Objects      int   `json:"objects"`
	PartialBytes int64 `json:"partialBytes"`
	Stages       int   `json:"stages"`
}

func (a *application) cacheStats() (cacheStats, error) {
	var s cacheStats
	stage := filepath.Join(a.cache, "staging")
	objects := filepath.Join(a.cache, "objects")
	err := filepath.WalkDir(a.cache, func(p string, e fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if e.IsDir() {
			if filepath.Dir(p) == stage {
				s.Stages++
			}
			return nil
		}
		st, err := e.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if st.Mode().IsRegular() {
			s.Bytes += st.Size()
			if pathInside(stage, p) {
				s.PartialBytes += st.Size()
			}
			if pathInside(objects, p) {
				switch e.Name() {
				case "video.mp4", "video.mkv", "video.m4a", "video.mp3":
					s.Objects++
				}
			}
		}
		return nil
	})
	return s, err
}
func pathInside(base, child string) bool {
	rel, err := filepath.Rel(base, child)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}
func (a *application) clearCache() error {
	a.mu.Lock()
	if len(a.active) != 0 || a.updating || a.maintenanceQueued {
		a.mu.Unlock()
		return errors.New("请等待当前任务结束后再清理缓存")
	}
	for _, j := range a.Jobs {
		if j.Status == "queued" {
			a.mu.Unlock()
			return errors.New("队列中还有未处理的任务")
		}
	}
	// Atomically detach the old cache before allowing the worker to begin a new job.
	trash, err := os.MkdirTemp(a.root, ".clear-cache-")
	if err != nil {
		a.mu.Unlock()
		return err
	}
	if err = os.Rename(a.cache, filepath.Join(trash, "cache")); err != nil {
		os.Remove(trash)
		a.mu.Unlock()
		return err
	}
	err = os.Mkdir(a.cache, 0700)
	if err != nil {
		os.Rename(filepath.Join(trash, "cache"), a.cache)
		a.mu.Unlock()
		os.Remove(trash)
		return err
	}
	a.mu.Unlock()
	return os.RemoveAll(trash)
}
