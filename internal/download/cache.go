package download

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type record struct {
	Name   string
	SHA256 string
	Size   int64
}

type cancelReader struct {
	ctx context.Context
	r   io.Reader
}

func (r cancelReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func checksum(ctx context.Context, path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, cancelReader{ctx, f})
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func verified(ctx context.Context, path string, r record) bool {
	s, err := os.Lstat(path)
	if err != nil || !s.Mode().IsRegular() || s.Size() != r.Size || r.Size <= 0 {
		return false
	}
	h, _, err := checksum(ctx, path)
	return err == nil && h == r.SHA256
}

func (j *job) reuse(m media) (Result, bool, error) {
	if err := j.ctx.Err(); err != nil {
		return Result{}, false, err
	}
	dir := filepath.Join(j.root, "objects", j.key(m))
	var rec record
	b, _ := os.ReadFile(filepath.Join(dir, "record.json"))
	_ = json.Unmarshal(b, &rec)
	hasRecord := len(rec.SHA256) == 64 && rec.Name == filepath.Base(rec.Name) && strings.ToLower(filepath.Ext(rec.Name)) == j.extension()
	if hasRecord && verified(j.ctx, filepath.Join(dir, j.objectFilename()), rec) {
		j.stage("复用已校验的 " + strings.ToUpper(j.options.Format()) + " 缓存")
		p, err := j.publish(m, rec)
		return Result{p, true}, true, err
	}
	if err := j.ctx.Err(); err != nil {
		return Result{}, false, err
	}
	// Only YouTube has stable IDs across all sources in legacy filenames.
	if j.outputRoot != nil || j.options.Profile() != "" || j.tools.FFprobe == "" || !strings.EqualFold(m.Extractor, "youtube") {
		return Result{}, false, nil
	}
	files, err := os.ReadDir(j.out)
	if err != nil {
		return Result{}, false, err
	}
	for _, ext := range []string{".mp4", ".webm", ".mkv", ".mov"} {
		for _, f := range files {
			if f.IsDir() || !f.Type().IsRegular() || !strings.HasSuffix(strings.TrimSuffix(f.Name(), filepath.Ext(f.Name())), "["+m.ID+"]") || !strings.EqualFold(filepath.Ext(f.Name()), ext) {
				continue
			}
			path := filepath.Join(j.out, f.Name())
			if hasRecord && ext == ".mp4" && !verified(j.ctx, path, rec) {
				continue
			}
			info, err := j.inspect(path, true)
			if err != nil || !info.complete(m.Duration) || (ext == ".mp4" && !info.mp4()) {
				if j.ctx.Err() != nil {
					return Result{}, false, j.ctx.Err()
				}
				fmt.Fprintf(j.log, "忽略未通过完整性检查的本地文件 %q: %v\n", path, err)
				continue
			}
			name := strings.TrimSuffix(f.Name(), filepath.Ext(f.Name())) + ".mp4"
			source := path
			if ext != ".mp4" {
				stage := filepath.Join(j.root, "staging", j.key(m))
				if err = os.MkdirAll(stage, 0700); err != nil {
					return Result{}, false, err
				}
				source, err = j.convert(path, stage, info)
				if err != nil {
					return Result{}, false, err
				}
			}
			j.stage("复用本地视频并建立校验缓存")
			rec, err = j.store(m, source, name)
			if err != nil {
				return Result{}, false, err
			}
			if ext == ".mp4" {
				return Result{path, true}, true, nil
			}
			if err = os.RemoveAll(filepath.Dir(source)); err != nil {
				fmt.Fprintf(j.log, "清理转换文件: %v\n", err)
			}
			p, err := j.publish(m, rec)
			return Result{p, true}, true, err
		}
	}
	return Result{}, false, nil
}

// copyFile takes ownership of dst even if opening the source fails.
func copyFile(ctx context.Context, source string, dst *os.File) (string, int64, error) {
	h := sha256.New()
	var n int64
	in, err := os.Open(source)
	if err == nil {
		n, err = io.Copy(io.MultiWriter(dst, h), cancelReader{ctx, in})
		in.Close()
	}
	if err == nil {
		err = dst.Sync()
	}
	if closeErr := dst.Close(); err == nil {
		err = closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func copyTemp(ctx context.Context, source, dir string) (string, string, int64, error) {
	f, err := os.CreateTemp(dir, ".yt-copy-*")
	if err != nil {
		return "", "", 0, err
	}
	hash, n, err := copyFile(ctx, source, f)
	if err != nil {
		os.Remove(f.Name())
		return "", "", 0, err
	}
	return f.Name(), hash, n, nil
}

func (j *job) store(m media, path, name string) (record, error) {
	dir := filepath.Join(j.root, "objects", j.key(m))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return record{}, err
	}
	tmp, hash, n, err := copyTemp(j.ctx, path, dir)
	if err != nil {
		return record{}, err
	}
	defer os.Remove(tmp)
	rec := record{name, hash, n}
	if err = os.Rename(tmp, filepath.Join(dir, j.objectFilename())); err != nil {
		return record{}, err
	}
	return rec, writeJSON(filepath.Join(dir, "record.json"), rec)
}

// Directory and filename operations use one root handle through publication.
// In server mode all names are relative to the mount itself, even when a
// destination subdirectory is replaced while a download is in progress.
func (j *job) publish(m media, rec record) (string, error) {
	source := filepath.Join(j.root, "objects", j.key(m), j.objectFilename())
	dir := j.outputRoot
	if dir == nil {
		var err error
		dir, err = os.OpenRoot(j.out)
		if err != nil {
			return "", err
		}
		defer dir.Close()
	}
	var tmp string
	defer func() {
		if tmp != "" {
			dir.Remove(tmp)
		}
	}()
	for i := 1; ; i++ {
		if err := j.ctx.Err(); err != nil {
			return "", err
		}
		name := rec.Name
		if i > 1 {
			name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, filepath.Ext(name)), i, j.extension())
		}
		destination := filepath.Join(j.outRel, name)
		path := filepath.Join(j.out, name)
		info, err := dir.Lstat(destination)
		if err == nil {
			if info.Mode().IsRegular() && info.Size() == rec.Size && rec.Size > 0 {
				f, err := dir.Open(destination)
				if err == nil {
					h := sha256.New()
					_, readErr := io.Copy(h, cancelReader{j.ctx, f})
					f.Close()
					if readErr == nil && hex.EncodeToString(h.Sum(nil)) == rec.SHA256 {
						return path, nil
					}
				}
			}
			continue
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if tmp == "" {
			tempName := filepath.Join(j.outRel, ".yt-copy-"+rand.Text())
			f, err := dir.OpenFile(tempName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return "", err
			}
			tmp = tempName
			hash, _, err := copyFile(j.ctx, source, f)
			if err != nil {
				return "", err
			}
			if hash != rec.SHA256 {
				return "", errors.New("复制时缓存校验失败")
			}
		}
		// Link a private copy instead of the cache object; user edits remain independent.
		if err = dir.Link(tmp, destination); err == nil {
			return path, nil
		} else if os.IsExist(err) {
			continue
		}
		// exFAT and some network shares cannot hard-link. Reserve the name with O_EXCL.
		dst, err := dir.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		hash, _, err := copyFile(j.ctx, source, dst)
		if err == nil && hash != rec.SHA256 {
			err = errors.New("复制时缓存校验失败")
		}
		if err != nil {
			dir.Remove(destination)
			return "", err
		}
		return path, nil
	}
}
