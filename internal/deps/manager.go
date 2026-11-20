// Package deps discovers working tools or installs verified release binaries in
// a private directory. Publishing one pointer after verifying the entire bundle
// keeps running downloads and prior tool versions usable during updates.
package deps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Paths struct{ YTDLP, FFmpeg, FFprobe, Deno string }
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
}
type Manager struct {
	root   string
	mu     sync.Mutex
	probes sync.Map
}

func New(root string) *Manager { return &Manager{root: filepath.Join(root, "components")} }

type bundle struct {
	key, repo, asset string
	tools            []string
}

func bundles(goos, arch string) ([]bundle, error) {
	if arch != "amd64" && arch != "arm64" {
		return nil, fmt.Errorf("暂无 %s/%s 组件，请安装系统工具", goos, arch)
	}
	triple := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]
	yt := "yt-dlp"
	ffrepo := "yt-dlp/FFmpeg-Builds"
	var ff string
	bits := map[string]string{"amd64": "64", "arm64": "arm64"}[arch]
	switch goos {
	case "darwin":
		yt += "_macos"
		triple += "-apple-darwin"
		ffrepo = "jellyfin/jellyfin-ffmpeg"
		ff = "*_portable_mac" + bits + "-gpl.tar.xz"
	case "linux":
		yt += "_linux"
		if arch == "arm64" {
			yt += "_aarch64"
		}
		triple += "-unknown-linux-gnu"
		ff = "ffmpeg-master-latest-linux" + bits + "-gpl.tar.xz"
	case "windows":
		if arch == "arm64" {
			yt += "_arm64"
		}
		yt += ".exe"
		triple += "-pc-windows-msvc"
		ff = "ffmpeg-master-latest-win" + bits + "-gpl.zip"
	default:
		return nil, fmt.Errorf("暂无 %s 的自动安装包，请安装系统工具", goos)
	}
	return []bundle{
		{"yt-dlp", "yt-dlp/yt-dlp", yt, []string{"yt-dlp"}},
		{"ffmpeg", ffrepo, ff, []string{"ffmpeg", "ffprobe"}},
		{"deno", "denoland/deno", "deno-" + triple + ".zip", []string{"deno"}},
	}, nil
}
func executable(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
func (m *Manager) active(key string) string {
	raw, err := os.ReadFile(filepath.Join(m.root, key, "active"))
	if err != nil {
		return ""
	}
	digest := strings.TrimSpace(string(raw))
	if !validDigest(digest) {
		return ""
	}
	return filepath.Join(m.root, key, digest)
}

type cachedProbe struct {
	modified time.Time
	size     int64
	version  string
}

func (m *Manager) probe(ctx context.Context, path, name string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s 不是文件", path)
	}
	if old, ok := m.probes.Load(path); ok {
		p := old.(cachedProbe)
		if p.modified.Equal(st.ModTime()) && p.size == st.Size() {
			return p.version, nil
		}
	}
	arg := "--version"
	if name == "ffmpeg" || name == "ffprobe" {
		arg = "-version"
	}
	child, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, path, arg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", path, arg, err, strings.TrimSpace(string(out)))
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(line)
	idx := 0
	switch name {
	case "ffmpeg", "ffprobe":
		idx = 2
	case "deno":
		idx = 1
	}
	if len(fields) <= idx || (idx > 0 && fields[0] != name) {
		return "", fmt.Errorf("无法读取 %s 版本: %s", name, line)
	}
	v := fields[idx]
	m.probes.Store(path, cachedProbe{st.ModTime(), st.Size(), v})
	return v, nil
}
func (m *Manager) lookup(ctx context.Context, name string) Tool {
	key := name
	if key == "ffprobe" {
		key = "ffmpeg"
	}
	var paths []string
	if active := m.active(key); active != "" {
		paths = append(paths, filepath.Join(active, executable(name)))
	}
	if p, err := exec.LookPath(name); err == nil && filepath.IsAbs(p) {
		paths = append(paths, p)
	}
	// Finder launches applications with a restricted PATH.
	if runtime.GOOS == "darwin" {
		for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	for _, p := range paths {
		v, err := m.probe(ctx, p, name)
		if err == nil {
			return Tool{name, v, p}
		}
	}
	return Tool{Name: name}
}
func (m *Manager) Status(ctx context.Context) []Tool {
	var tools []Tool
	for _, name := range []string{"yt-dlp", "ffmpeg", "ffprobe", "deno"} {
		tools = append(tools, m.lookup(ctx, name))
	}
	return tools
}
func (m *Manager) Ensure(ctx context.Context, log io.Writer) (Paths, error) {
	return m.prepare(ctx, log, false)
}
func (m *Manager) Update(ctx context.Context, log io.Writer) (Paths, error) {
	return m.prepare(ctx, log, true)
}
func (m *Manager) prepare(ctx context.Context, log io.Writer, update bool) (Paths, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if log == nil {
		log = io.Discard
	}
	pkgs, err := bundles(runtime.GOOS, runtime.GOARCH)
	all := make(map[string]Tool)
	for _, t := range m.Status(ctx) {
		all[t.Name] = t
	}
	final := func() Paths {
		return Paths{all["yt-dlp"].Path, all["ffmpeg"].Path, all["ffprobe"].Path, all["deno"].Path}
	}
	if err != nil {
		p := final()
		if !update && p.YTDLP != "" && p.FFmpeg != "" && p.FFprobe != "" && p.Deno != "" {
			return p, nil
		}
		return Paths{}, err
	}
	var failures []error
	for _, pkg := range pkgs {
		if err := ctx.Err(); err != nil {
			return Paths{}, err
		}
		ok := true
		for _, name := range pkg.tools {
			if all[name].Path == "" {
				ok = false
			}
		}
		if pkg.key == "deno" && compare(all["deno"].Version, "2.3.0") < 0 {
			ok = false
		}
		if !update && ok {
			continue
		}
		fmt.Fprintf(log, "检查 %s (%s)…\n", pkg.key, pkg.repo)
		asset, tag, err := latest(ctx, pkg)
		if err == nil {
			active := m.active(pkg.key)
			already := ok && active != "" && filepath.Base(active) == asset.sum
			// A system package can be newer than the portable distributor's release.
			systemCurrent := ok && tag != "latest"
			for _, name := range pkg.tools {
				fromActive := filepath.Dir(all[name].Path) == active
				already = already && fromActive
				systemCurrent = systemCurrent && !fromActive && compare(all[name].Version, tag) >= 0
			}
			if already || systemCurrent {
				fmt.Fprintf(log, "%s %s 已是当前可用版本\n", pkg.key, all[pkg.key].Version)
				continue
			}
			err = m.install(ctx, log, pkg, asset)
		}
		if err != nil {
			fmt.Fprintf(log, "%s 更新失败: %v\n", pkg.key, err)
			failures = append(failures, fmt.Errorf("%s: %w", pkg.key, err))
			continue
		}
		for _, name := range pkg.tools {
			all[name] = m.lookup(ctx, name)
			fmt.Fprintf(log, "%s %s (%s)\n", name, all[name].Version, all[name].Path)
		}
	}
	return final(), errors.Join(failures...)
}

// Compare numeric release components; distributor/build suffixes do not make
// an otherwise newer system release eligible for a downgrade.
func compare(a, b string) int {
	parts := func(s string) []string {
		s = strings.TrimLeft(s, "nv")
		end := strings.IndexFunc(s, func(r rune) bool { return r != '.' && (r < '0' || r > '9') })
		if end >= 0 {
			s = s[:end]
		}
		return strings.Split(s, ".")
	}
	aa, bb := parts(a), parts(b)
	for i := 0; i < len(aa) || i < len(bb); i++ {
		var x, y int
		if i < len(aa) {
			x, _ = strconv.Atoi(aa[i])
		}
		if i < len(bb) {
			y, _ = strconv.Atoi(bb[i])
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}
