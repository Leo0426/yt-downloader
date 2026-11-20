package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"yt-downloader/internal/assist"
)

// Membership is explicit even for routes available to every signed-in user.
var businessPermissions = map[string]string{
	"GET /api/state":            "",
	"GET /api/default-settings": "settings.manage",
	"GET /api/cache":            "storage.manage",
	"GET /api/log":              "jobs.logs",
	"GET /api/file":             "jobs.files",
	"HEAD /api/file":            "jobs.files",
	"POST /api/jobs":            "jobs.create",
	"POST /api/retry":           "jobs.control",
	"POST /api/cancel":          "jobs.control",
	"POST /api/ai-retry":        "jobs.ai",
	"POST /api/open":            "jobs.files",
	"POST /api/reveal":          "jobs.files",
	"POST /api/cache/clear":     "storage.manage",
	"POST /api/ai/models":       "settings.manage",
	"POST /api/settings":        "settings.manage",
	"POST /api/queue":           "queue.manage",
	"POST /api/reveal-data":     "storage.reveal",
	"POST /api/reveal-logs":     "storage.reveal",
	"POST /api/update":          "tools.manage",
	"POST /api/quit":            "app.quit",
}

// Both log reads and file opens share the same visibility boundary as the task list.
func (a *application) visibleJob(actor identityPrincipal, id string) (job, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	found := a.find(id)
	if found == nil || !found.visibleTo(actor) {
		return job{}, false
	}
	return *found, true
}

func routes(a *application, origin, token string, assets http.FileSystem, quit func()) http.Handler {
	address, _ := url.Parse(origin)
	return siteRoutes(a, httpSite{origin: address}, token, assets, quit)
}

func siteRoutes(a *application, site httpSite, token string, assets http.FileSystem, quit func()) http.Handler {
	cookieName := "ytd_session_" + site.origin.Port()
	fileServer := http.FileServer(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		origin, allowed := site.requestOrigin(r.Host)
		if !allowed {
			http.Error(w, "请求地址与服务配置不匹配", http.StatusForbidden)
			return
		}
		if !site.web && r.Method == http.MethodGet && r.URL.Path == "/start" && subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(token)) == 1 {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			view := "/"
			if r.URL.Query().Get("view") == "sniffer" {
				actor, signedIn := a.identity.principal(r)
				if !signedIn || !actor.Can("jobs.create") {
					http.Error(w, "请使用有权添加下载的账户打开网页嗅探", http.StatusForbidden)
					return
				}
				view = "/sniffer.html"
			}
			http.Redirect(w, r, view, http.StatusSeeOther)
			return
		}
		if !site.web {
			cookie, err := r.Cookie(cookieName)
			if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) != 1 {
				http.Error(w, "请从下载器重新打开此界面", http.StatusForbidden)
				return
			}
		}
		if path.Clean(r.URL.Path) != r.URL.Path {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method != "GET" && r.Method != "HEAD" {
				http.Error(w, "method", 405)
				return
			}
			if r.URL.Path == "/sniffer.html" {
				if site.web {
					http.NotFound(w, r)
					return
				}

				actor, signedIn := a.identity.principal(r)
				if !signedIn || !actor.Can("jobs.create") {
					http.Error(w, "请使用有权添加下载的账户打开网页嗅探", http.StatusForbidden)
					return
				}
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fail := func(code int, msg string) {
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]string{"error": msg})
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost && !(r.Method == http.MethodHead && r.URL.Path == "/api/file") {
			fail(405, "不支持的请求")
			return
		}
		if r.Method == http.MethodPost {
			if r.Header.Get("Origin") != origin || strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
				fail(403, "无效的请求来源或数据格式")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		}
		if strings.HasPrefix(r.URL.Path, "/api/auth/") || r.URL.Path == "/api/access" || strings.HasPrefix(r.URL.Path, "/api/access/") {
			a.identity.serve(w, r, site.deployment)
			return
		}
		var input struct {
			URLs        []string       `json:"urls"`
			OutDir      string         `json:"outDir"`
			ID          string         `json:"id"`
			Preferences *preferences   `json:"preferences"`
			AI          *assist.Config `json:"ai"`
			AIKey       *string        `json:"aiKey"`
			Resolution  *int           `json:"resolution"`
			Format      string         `json:"format"`
			Paused      *bool          `json:"paused"`
		}
		// Resolve the current user and permissions after receiving a mutation body.
		// A role or login session can change while a client is uploading its request.
		var err, decodeErr error
		if r.Method == http.MethodPost {
			decodeErr = json.NewDecoder(r.Body).Decode(&input)
		}
		actor, signedIn := a.identity.principal(r)
		if expected := r.Header.Get("X-YTD-User"); expected != "" && (!signedIn || actor.ID != expected) {
			fail(401, "登录账户已变更，请刷新后重试")
			return
		}
		if !signedIn {
			fail(401, "请先登录")
			return
		}
		permission, exists := businessPermissions[r.Method+" "+r.URL.Path]
		if !exists {
			fail(404, "找不到请求的功能")
			return
		}
		switch r.URL.Path {
		case "/api/file":
			if !site.web {
				fail(404, "找不到请求的功能")
				return
			}
		case "/api/open", "/api/reveal", "/api/reveal-data", "/api/reveal-logs", "/api/quit":
			if site.web {
				fail(403, "此操作仅在桌面版中可用")
				return
			}
		}
		if permission != "" && !actor.Can(permission) {
			fail(403, errAccessDenied.Error())
			return
		}
		if decodeErr != nil {
			fail(400, "无法读取请求内容")
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			switch r.URL.Path {
			case "/api/default-settings":
				dir, err := a.defaultOutputDirectory()
				if err != nil {
					fail(500, err.Error())
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"outDir": dir, "preferences": defaultPreferences()})
				return
			case "/api/cache":
				s, err := a.cacheStats()
				if err != nil {
					fail(500, err.Error())
					return
				}
				json.NewEncoder(w).Encode(s)
				return
			case "/api/state":
				state := a.snapshot(actor)
				state["deployment"] = site.name()
				json.NewEncoder(w).Encode(state)
				return
			case "/api/file":
				a.serveJobFile(w, r, actor)
				return
			case "/api/log":
				j, ok := a.visibleJob(actor, r.URL.Query().Get("id"))
				if !ok || j.Log == "" {
					fail(404, "找不到任务")
					return
				}
				f, err := os.Open(j.Log)
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				if errors.Is(err, os.ErrNotExist) {
					io.WriteString(w, "尚无日志")
					return
				}
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				defer f.Close()
				io.Copy(w, f)
				return
			}
		}
		switch r.URL.Path {
		case "/api/jobs":
			var added, skipped int
			added, skipped, err = a.add(actor, input.URLs, input.OutDir, input.Resolution, input.Format)
			if err == nil {
				json.NewEncoder(w).Encode(map[string]int{"added": added, "skipped": skipped})
				return
			}
		case "/api/retry", "/api/ai-retry", "/api/cancel":
			err = a.action(actor, input.ID, strings.TrimPrefix(r.URL.Path, "/api/"))
		case "/api/cache/clear":
			err = a.clearCache()
		case "/api/ai/models":
			if input.AI == nil {
				fail(400, "请先选择协议并填写 AI 服务地址")
				return
			}
			var models []string
			models, err = a.listModels(r.Context(), *input.AI, input.AIKey)
			if err == nil {
				json.NewEncoder(w).Encode(map[string][]string{"models": models})
				return
			}
		case "/api/settings":
			err = a.savePreferences(input.OutDir, input.Preferences, input.AIKey)
			if err == nil {
				a.mu.Lock()
				saved := map[string]any{"outDir": a.OutDir, "preferences": a.Preferences, "aiKeySet": a.aiKey != ""}
				a.mu.Unlock()
				json.NewEncoder(w).Encode(saved)
				return
			}
		case "/api/queue":
			if input.Paused == nil {
				fail(400, "缺少队列状态")
				return
			}
			err = a.pauseQueue(*input.Paused)
		case "/api/reveal-data":
			err = openSystem(a.root)
		case "/api/reveal-logs":
			err = openSystem(filepath.Join(a.root, "logs"))
		case "/api/update":
			err = a.update()
		case "/api/open", "/api/reveal":
			j, ok := a.visibleJob(actor, input.ID)
			if !ok {
				fail(404, "找不到任务")
				return
			}
			p := j.Path
			if p == "" {
				err = errors.New("没有已完成的文件")
			} else {
				if r.URL.Path == "/api/reveal" {
					p = filepath.Dir(p)
				}
				if _, err = os.Stat(p); err == nil {
					err = openSystem(p)
				} else if errors.Is(err, os.ErrNotExist) {
					err = errors.New("文件或目录已移动，可以重新提交链接以恢复文件")
				}
			}
		case "/api/quit":
			time.AfterFunc(100*time.Millisecond, quit)
		default:
			fail(404, "找不到请求的功能")
			return
		}
		if err != nil {
			switch {
			case errors.Is(err, errAccessDenied):
				fail(403, errAccessDenied.Error())
			case errors.Is(err, os.ErrNotExist):
				fail(404, "找不到任务或文件")
			default:
				fail(400, err.Error())
			}
			return
		}
		io.WriteString(w, "{}")
	})
}
