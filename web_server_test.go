package main

import (
	"encoding/json"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestWebOriginConfiguration(t *testing.T) {
	actual := &net.TCPAddr{IP: net.IPv4zero, Port: 32000}
	site, err := webSite("0.0.0.0:0", actual, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"localhost:32000", "127.0.0.1:32000", "[::1]:32000"} {
		if origin, ok := site.requestOrigin(host); !ok || origin != "http://"+host {
			t.Fatalf("loopback origin %q", origin)
		}
	}
	for _, host := range []string{"external.example:32000", "localhost:8080"} {
		if _, ok := site.requestOrigin(host); ok {
			t.Fatalf("unconfigured host %q", host)
		}
	}
	site, err = webSite("0.0.0.0:0", actual, "https://Media.Example:443/")
	if err != nil || site.origin.String() != "https://media.example" || !site.secure {
		t.Fatal(site, err)
	}
	for _, host := range []string{"media.example", "media.example:443"} {
		if origin, ok := site.requestOrigin(host); !ok || origin != "https://media.example" {
			t.Fatal(host, origin)
		}
	}
	for _, raw := range []string{"javascript:example", "http://user@media.test", "https://media.test/site", "https://media.test?key=private", "https://media.test/#abc", "https://media.test:99999"} {
		if _, err := webSite("0.0.0.0:0", actual, raw); err == nil {
			t.Errorf("accepted invalid origin %s", raw)
		}
	}
}

func TestWebBrowserDeployment(t *testing.T) {
	a := bareApp(t)
	downloads := t.TempDir()
	root, err := openMediaRoot(a.root, downloads)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	a.mediaRoot, a.downloadRoot, a.OutDir = root, downloads, downloads
	const origin = "https://downloads.example"
	public, _ := url.Parse(origin)
	site := httpSite{origin: public, deployment: deployment{web: true, secure: true, setupToken: "setup-test-code"}}
	h := siteRoutes(a, site, "", http.FS(fstest.MapFS{
		"index.html": {Data: []byte("workspace")}, "sniffer.html": {Data: []byte("sniffer")},
	}), func() { t.Error("unexpected web service exit") })
	do := func(method, route, body string, c *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, origin+route, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			if k == "Host" {
				r.Host = v
			} else {
				r.Header.Set(k, v)
			}
		}
		if c != nil {
			r.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	demand := func(w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code {
			t.Fatalf("expected %d, received %d: %s", code, w.Code, w.Body)
		}
	}
	const auth = `{"username":"admin","password":"web-admin-passphrase"}`
	demand(do("GET", "/", "", nil, nil), 200)
	demand(do("GET", "/api/state", "", nil, nil), 401)
	demand(do("GET", "/sniffer.html", "", nil, nil), 404)
	w := do("GET", "/api/auth/session", "", nil, nil)
	demand(w, 200)
	if !strings.Contains(w.Body.String(), `"setupTokenRequired":true`) || !strings.Contains(w.Body.String(), `"deployment":"web"`) || strings.Contains(w.Body.String(), "setup-test-code") {
		t.Fatal("unexpected public setup metadata", w.Body.String())
	}
	demand(do("POST", "/api/auth/setup", auth, nil, nil), 403)
	demand(do("POST", "/api/auth/setup", auth, nil, map[string]string{"Origin": "https://other.example", "X-YTD-Setup-Token": "setup-test-code"}), 403)
	demand(do("GET", "/api/auth/session", "", nil, map[string]string{"Host": "other.example", "X-Forwarded-Host": "downloads.example"}), 403)
	w = do("POST", "/api/auth/setup", auth, nil, map[string]string{"X-YTD-Setup-Token": "setup-test-code"})
	demand(w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("invalid HTTPS account cookie")
	}
	admin := cookies[0]
	demand(do("POST", "/api/auth/setup", auth, nil, map[string]string{"X-YTD-Setup-Token": "setup-test-code"}), 409)
	for _, route := range []string{"quit", "reveal", "open", "reveal-data", "reveal-logs"} {
		demand(do("POST", "/api/"+route, "{}", admin, nil), 403)
	}
	w = do("GET", "/api/state", "", admin, nil)
	demand(w, 200)
	if !strings.Contains(w.Body.String(), `"deployment":"web"`) {
		t.Fatal(w.Body.String())
	}
	demand(do("POST", "/api/access/users/save", `{"username":"operator","roles":["operator"],"password":"web-operator-passphrase"}`, admin, nil), 200)
	w = do("POST", "/api/auth/login", `{"username":"operator","password":"web-operator-passphrase"}`, nil, nil)
	demand(w, 200)
	operator := w.Result().Cookies()[0]
	var logged struct{ User identityPrincipal }
	if err := json.Unmarshal(w.Body.Bytes(), &logged); err != nil {
		t.Fatal(err)
	}
	name := "下载示例.mp4"
	local := filepath.Join(downloads, name)
	if err := os.WriteFile(local, []byte("abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	a.Jobs = []job{{ID: "own", OwnerID: logged.User.ID, Status: "done", OutDir: downloads, Path: local}, {ID: "other", OwnerID: "another", Status: "done", OutDir: downloads, Path: local}}
	demand(do("POST", "/api/access/users/save", `{"username":"reader","roles":["viewer"],"password":"web-reader-passphrase"}`, admin, nil), 200)
	w = do("POST", "/api/auth/login", `{"username":"reader","password":"web-reader-passphrase"}`, nil, nil)
	demand(w, 200)
	reader := w.Result().Cookies()[0]

	// The same existing artifact is downloadable in Web mode only. Even an
	// authenticated desktop administrator cannot turn the core into a file server.
	desktop := routes(a, origin, "desktop-test", http.FS(fstest.MapFS{}), nil)
	launch := routeCall(desktop, "GET", origin, "/start?token=desktop-test", "").Result().Cookies()[0]
	for _, method := range []string{"GET", "HEAD"} {
		demand(do(method, "/api/file?id=own", "", nil, nil), 401)
		demand(do(method, "/api/file?id=own", "", reader, nil), 403)
		demand(do(method, "/api/file?id=other", "", operator, nil), 404)
		demand(routeCall(desktop, method, origin, "/api/file?id=own", "", launch, admin), 404)
	}
	w = do("GET", "/api/file?id=own", "", operator, map[string]string{"Range": "bytes=1-3"})
	demand(w, 206)
	mt, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || mt != "attachment" || params["filename"] != name || w.Body.String() != "bcd" || w.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatal("invalid attachment range response", w.Body.String(), w.Header())
	}
	w = do("HEAD", "/api/file?id=own", "", operator, nil)
	demand(w, 200)
	if w.Body.Len() != 0 || w.Header().Get("Content-Length") != "6" {
		t.Fatal("invalid HEAD")
	}
}
