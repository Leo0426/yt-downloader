package deps

import (
	"archive/tar"
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

const maxAsset = 768 << 20

var client = &http.Client{Timeout: 15 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) > 8 || req.URL.Scheme != "https" {
		return errors.New("组件下载返回了不安全的重定向")
	}
	switch req.URL.Hostname() {
	case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "github-releases.githubusercontent.com":
		return nil
	}
	return fmt.Errorf("未知的下载域名: %s", req.URL.Hostname())
}}

type releaseAsset struct {
	name, url, sum string
	size           int64
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func request(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "YT-Downloader/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("GET %s: %s: %s", u, resp.Status, strings.TrimSpace(string(body)))
	}
	return resp, nil
}
func latest(ctx context.Context, pkg bundle) (releaseAsset, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	resp, err := request(ctx, "https://api.github.com/repos/"+pkg.repo+"/releases/latest")
	if err != nil {
		return releaseAsset{}, "", err
	}
	defer resp.Body.Close()
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string
			URL    string `json:"browser_download_url"`
			Digest string
			Size   int64
		}
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return releaseAsset{}, "", err
	}
	for _, a := range rel.Assets {
		match, _ := path.Match(pkg.asset, a.Name)
		if !match {
			continue
		}
		u, err := url.Parse(a.URL)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || !strings.HasPrefix(u.Path, "/"+pkg.repo+"/releases/download/") || u.User != nil {
			return releaseAsset{}, "", fmt.Errorf("组件 URL 无效: %s", a.Name)
		}
		sum, ok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok || !validDigest(sum) || a.Size < 1 || a.Size > maxAsset {
			return releaseAsset{}, "", fmt.Errorf("发布包缺少有效的 SHA-256 或大小: %s", a.Name)
		}
		return releaseAsset{a.Name, a.URL, strings.ToLower(sum), a.Size}, rel.Tag, nil
	}
	return releaseAsset{}, "", fmt.Errorf("在 %s 的最新版本 %s 中找不到 %s", pkg.repo, rel.Tag, pkg.asset)
}
func (m *Manager) install(ctx context.Context, log io.Writer, pkg bundle, a releaseAsset) error {
	parent := filepath.Join(m.root, pkg.key)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	dest := filepath.Join(parent, a.sum)
	check := func(dir string) error {
		for _, name := range pkg.tools {
			if _, err := m.probe(ctx, filepath.Join(dir, executable(name)), name); err != nil {
				return err
			}
		}
		return nil
	}
	if err = check(dest); err != nil {
		fmt.Fprintf(log, "下载 %s (%.1f MiB)\n", a.name, float64(a.size)/(1<<20))
		archive := filepath.Join(stage, "asset")
		if err = download(ctx, a, archive); err != nil {
			return err
		}
		out := filepath.Join(stage, "bin")
		if err = os.Mkdir(out, 0700); err != nil {
			return err
		}
		if err = unpack(archive, out, a.name, pkg.tools); err != nil {
			return err
		}
		if err = check(out); err != nil {
			return fmt.Errorf("验证新组件失败: %w", err)
		}
		if _, err = os.Stat(dest); err == nil {
			if err = os.Rename(dest, filepath.Join(stage, "broken")); err != nil {
				return err
			}
			if err = os.Rename(out, dest); err != nil {
				rollback := os.Rename(filepath.Join(stage, "broken"), dest)
				return errors.Join(err, rollback)
			}
		} else if err = os.Rename(out, dest); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(log, "复用已验证的 %s 发布包\n", pkg.key)
	}
	pointer := filepath.Join(stage, "active")
	if err = os.WriteFile(pointer, []byte(a.sum+"\n"), 0600); err != nil {
		return err
	}
	return os.Rename(pointer, filepath.Join(parent, "active"))
}
func download(ctx context.Context, a releaseAsset, out string) error {
	resp, err := request(ctx, a.url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, a.size+1))
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if n != a.size || hex.EncodeToString(h.Sum(nil)) != a.sum {
		return fmt.Errorf("%s 的下载大小或 SHA-256 不匹配", a.name)
	}
	return nil
}
func unpack(src, out, assetName string, tools []string) error {
	missing := map[string]bool{}
	for _, name := range tools {
		missing[executable(name)] = true
	}
	write := func(entry string, r io.Reader) error {
		name := path.Base(entry)
		if !missing[name] {
			return nil
		}
		f, err := os.OpenFile(filepath.Join(out, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(r, maxAsset+1))
		if err = errors.Join(err, f.Close()); err != nil {
			return err
		}
		if n > maxAsset || n == 0 {
			return fmt.Errorf("无效的可执行文件大小: %s", name)
		}
		delete(missing, name)
		return nil
	}
	switch {
	case strings.HasSuffix(assetName, ".zip"):
		z, err := zip.OpenReader(src)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, e := range z.File {
			if !e.Mode().IsRegular() || !missing[path.Base(e.Name)] {
				continue
			}
			r, err := e.Open()
			if err != nil {
				return err
			}
			if err = errors.Join(write(e.Name, r), r.Close()); err != nil {
				return err
			}
		}
	case strings.HasSuffix(assetName, ".tar.xz"):
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		stream, err := xz.NewReader(f)
		if err != nil {
			return err
		}
		t := tar.NewReader(io.LimitReader(stream, 2<<30))
		for len(missing) > 0 {
			e, err := t.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if e.Typeflag == tar.TypeReg {
				if err = write(e.Name, t); err != nil {
					return err
				}
			}
		}
	default:
		if len(tools) != 1 {
			return errors.New("无效的独立可执行文件包")
		}
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		if err = write(executable(tools[0]), f); err != nil {
			return err
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s 缺少所需的可执行文件: %v", assetName, missing)
	}
	return nil
}
