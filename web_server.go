package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type deployment struct {
	web        bool
	secure     bool
	setupToken string
}

func (d deployment) name() string {
	if d.web {
		return "web"
	}
	return "desktop"
}

type httpSite struct {
	deployment
	origin       *url.URL
	localAliases bool
}

func webSite(listen string, actual net.Addr, publicURL string) (httpSite, error) {
	raw := strings.TrimSpace(publicURL)
	local := false
	if raw == "" {
		host, _, err := net.SplitHostPort(listen)
		if err != nil {
			return httpSite{}, err
		}
		_, port, err := net.SplitHostPort(actual.String())
		if err != nil {
			return httpSite{}, err
		}
		ip := net.ParseIP(host)
		if host == "" || ip.IsUnspecified() {
			host = "localhost"
		}
		local = strings.EqualFold(host, "localhost") || ip.IsLoopback()
		raw = "http://" + net.JoinHostPort(host, port)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || strings.ContainsAny(raw, "?#\\") || (u.Path != "/" && u.Path != "") || u.RawPath != "" ||
		strings.HasSuffix(u.Host, ":") {
		return httpSite{}, errors.New("公开地址需要是 HTTP 或 HTTPS 的站点根地址，例如 https://download.example.com")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return httpSite{}, errors.New("公开地址中的端口无效")
		}
	}
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = strings.TrimSuffix(u.Host, ":"+u.Port())
	}
	u.Path = ""
	return httpSite{deployment: deployment{web: true, secure: u.Scheme == "https"}, origin: u, localAliases: local}, nil
}

// Host is checked before using it to compare Origin. Reverse proxies must
// preserve the public Host; client-supplied forwarding headers are ignored.
func (site httpSite) requestOrigin(host string) (string, bool) {
	host = strings.ToLower(host)
	if site.origin.Port() == "" {
		if site.origin.Scheme == "https" {
			host = strings.TrimSuffix(host, ":443")
		} else {
			host = strings.TrimSuffix(host, ":80")
		}
	}
	if host != site.origin.Host {
		if !site.localAliases {
			return "", false
		}
		allowed := false
		for _, loopback := range []string{"localhost", "127.0.0.1", "[::1]"} {
			if port := site.origin.Port(); port != "" {
				loopback += ":" + port
			}
			if host == loopback {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", false
		}
	}
	return site.origin.Scheme + "://" + host, true
}

func (site *httpSite) prepareSetup(store *identityStore) {
	store.mu.Lock()
	empty := len(store.data.Users) == 0
	store.mu.Unlock()
	if !empty {
		return
	}
	site.setupToken = strings.TrimSpace(os.Getenv("YTDL_SETUP_TOKEN"))
	if site.setupToken != "" {
		fmt.Println("首次创建管理员时请输入 YTDL_SETUP_TOKEN 中配置的初始化码。")
		return
	}
	site.setupToken = rand.Text()
	fmt.Printf("首次管理员初始化码（仅在创建账户前有效）: %s\n", site.setupToken)
}

func (a *application) serveJobFile(w http.ResponseWriter, r *http.Request, actor identityPrincipal) {
	j, ok := a.visibleJob(actor, r.URL.Query().Get("id"))
	if !ok {
		identityError(w, http.StatusNotFound, "找不到可下载的文件")
		return
	}
	f, err := a.openJobFile(j)
	if err != nil {
		identityError(w, http.StatusNotFound, "找不到可下载的文件")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		identityError(w, http.StatusNotFound, "找不到可下载的文件")
		return
	}
	name := filepath.Base(j.Path)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, info.ModTime(), f)
}
