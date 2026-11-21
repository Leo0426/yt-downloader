package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"yt-downloader/internal/assist"
)

func TestReuseRequiresAccountAwareCore(t *testing.T) {
	for _, tt := range []struct {
		name   string
		code   int
		body   string
		wantOK bool
	}{
		{"existing sign-in", 200, `{"setupRequired":false,"user":null}`, true},
		{"setup", 200, `{"setupRequired":true,"user":null}`, true},
		{"old core", 405, "", false},
		{"missing session", 200, `{}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/auth/session" || len(r.Cookies()) != 1 || r.Cookies()[0].Value != "launch-test" {
					t.Error("expected an anonymous account probe with only the private launch cookie")
				}
				w.WriteHeader(tt.code)
				w.Write([]byte(tt.body))
			}))
			defer server.Close()
			launch, err := url.Parse(server.URL + "/start?token=launch-test")
			if err != nil {
				t.Fatal(err)
			}
			if err := checkAccountService(launch); (err == nil) != tt.wantOK {
				t.Fatalf("unexpected reuse result: %v", err)
			}
		})
	}
}

func routeCall(h http.Handler, method, origin, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func setupTestAccount(t *testing.T, h http.Handler, origin string, launch *http.Cookie) *http.Cookie {
	t.Helper()
	w := routeCall(h, "POST", origin, "/api/auth/setup", `{"username":"admin","password":"test-admin-password"}`, launch)
	if w.Code/100 != 2 || len(w.Result().Cookies()) == 0 {
		t.Fatalf("bootstrap: %d %s", w.Code, w.Body)
	}
	return w.Result().Cookies()[0]
}

func TestAccountsUseSeparateQueuesAndProtectBusinessRoutes(t *testing.T) {
	a := bareApp(t)
	a.cache = filepath.Join(a.root, "private-cache")
	if err := os.MkdirAll(a.cache, 0700); err != nil {
		t.Fatal(err)
	}
	a.Preferences.AI = assist.Config{BaseURL: "https://private-provider.test/v1", Model: "private-model"}
	a.maintenanceLog = "private maintenance log"
	a.Jobs = append(a.Jobs, job{ID: "legacy", URL: "https://user-legacy.test/mp4", Status: "failed"})
	const origin = "http://127.0.0.1:32043"
	h := routes(a, origin, "launch", http.FS(fstest.MapFS{"sniffer.html": {Data: []byte("sniffer shell")}}), func() { t.Error("unexpected application quit") })
	launch := routeCall(h, "GET", origin, "/start?token=launch", "").Result().Cookies()[0]
	admin := setupTestAccount(t, h, origin, launch)
	check := func(method, path, body string, c *http.Cookie, code int) *httptest.ResponseRecorder {
		t.Helper()
		cookies := []*http.Cookie{launch}
		if c != nil {
			cookies = append(cookies, c)
		}
		w := routeCall(h, method, origin, path, body, cookies...)
		if w.Code != code {
			t.Fatalf("%s %s: want %d, got %d %s", method, path, code, w.Code, w.Body)
		}
		return w
	}
	addUser := func(name, role string) *http.Cookie {
		t.Helper()
		creds := `{"username":"` + name + `","password":"test-member-password"`
		w := routeCall(h, "POST", origin, "/api/access/users/save", creds+`,"roles":["`+role+`"]}`, launch, admin)
		if w.Code/100 != 2 {
			t.Fatalf("create %s: %d %s", name, w.Code, w.Body)
		}
		return check("POST", "/api/auth/login", creds+"}", nil, 200).Result().Cookies()[0]
	}
	operator := addUser("operator-one", "operator")
	viewer := addUser("read-only", "viewer")

	check("GET", "/sniffer.html", "", viewer, 403)
	check("GET", "/start?token=launch&view=sniffer", "", viewer, 403)
	check("GET", "/sniffer.html", "", operator, 200)
	check("GET", "/start?token=launch&view=sniffer", "", operator, 303)
	check("GET", "/api/state", "", nil, 401)
	check("POST", "/api/unknown", "{}", admin, 404)
	for key, permission := range businessPermissions {
		if permission == "" {
			continue
		}
		method, path, _ := strings.Cut(key, " ")
		if path == "/api/file" {
			check(method, path, "", viewer, 404)
			continue
		}
		check(method, path, "{}", viewer, 403)
	}
	// The caller cannot grant a task to another account or detect their duplicate.
	const submit = `{"urls":["https://example.com/shared-video"],"ownerId":"claimed-other-user"}`
	check("POST", "/api/jobs", submit, admin, 200)
	check("POST", "/api/jobs", submit, operator, 200)
	check("POST", "/api/jobs", submit, operator, 200)
	if len(a.Jobs) != 3 || a.Jobs[1].OwnerID == "" || a.Jobs[2].OwnerID == "" || a.Jobs[1].OwnerID == a.Jobs[2].OwnerID || a.Jobs[2].OwnerID == "claimed-other-user" {
		t.Fatalf("incorrect job ownership or duplicate handling: %#v", a.Jobs)
	}
	unapproved := filepath.Join(a.root, "unapproved-path")
	check("POST", "/api/jobs", `{"urls":["https://example.com/other"],"outDir":"`+filepath.ToSlash(unapproved)+`"}`, operator, 403)
	if _, err := os.Stat(unapproved); !os.IsNotExist(err) {
		t.Fatal("unapproved directory created")
	}
	for _, j := range []job{a.Jobs[0], a.Jobs[1]} {
		check("GET", "/api/log?id="+j.ID, "", operator, 404)
		for _, action := range []string{"retry", "ai-retry", "cancel", "delete", "reveal", "open"} {
			check("POST", "/api/"+action, `{"id":"`+j.ID+`"}`, operator, 404)
		}
	}

	// Confirm actual state responses, including indirect disclosures in assisted attempts.
	a.Jobs[2].AIRetry = &assistedRetry{Config: a.Preferences.AI}
	w := check("GET", "/api/state", "", operator, 200)
	var result struct {
		Jobs         []job
		Session      struct{ User identityPrincipal }
		AIConfigured bool
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Jobs) != 1 || result.Jobs[0].ID != a.Jobs[2].ID || result.Session.User.ID != a.Jobs[2].OwnerID || !result.AIConfigured {
		t.Fatalf("incorrect operator state: %s", w.Body)
	}
	for _, secret := range []string{a.maintenanceLog, a.Preferences.AI.BaseURL, a.Preferences.AI.Model, a.cache, a.Jobs[1].ID, a.Jobs[2].Log, "user-legacy.test", "ownerNames"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("unexpected private field %q in state", secret)
		}
	}
	adminState := check("GET", "/api/state", "", admin, 200)
	if !strings.Contains(adminState.Body.String(), `"ownerNames":`) || !strings.Contains(adminState.Body.String(), `"operator-one"`) {
		t.Fatal("global view lacks visible owner names")
	}
	check("POST", "/api/cancel", `{"id":"`+a.Jobs[2].ID+`"}`, operator, 200)
	check("POST", "/api/retry", `{"id":"legacy"}`, admin, 200)
	req := httptest.NewRequest("POST", origin+"/api/jobs", strings.NewReader(submit))
	req.AddCookie(launch)
	req.AddCookie(admin)
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-YTD-User", a.Jobs[2].OwnerID)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != 401 || len(a.Jobs) != 3 {
		t.Fatal("stale tab submitted as another principal")
	}
	owned := a.Jobs[2].ID
	check("POST", "/api/delete", `{"id":"`+owned+`"}`, operator, 200)
	check("GET", "/api/log?id="+owned, "", operator, 404)
	check("POST", "/api/delete", `{"id":"legacy"}`, admin, 200)
	if len(a.Jobs) != 1 || a.Jobs[0].OwnerID == result.Session.User.ID {
		t.Fatalf("unexpected queue after deletion: %#v", a.Jobs)
	}
}

func TestTaskScopesRequireTheirBaseCapabilities(t *testing.T) {
	a := bareApp(t)
	a.Jobs = []job{{ID: "other", OwnerID: "bob", Status: "queued", Path: "/private/other.mp4", Log: "/private/data/other.log"}}
	readAll := identityPrincipal{ID: "alice", Permissions: []string{"jobs.view", "jobs.viewAll", "jobs.control"}}
	if err := a.action(readAll, "other", "cancel"); err != errAccessDenied {
		t.Fatalf("view all allowed controlling other account: %v", err)
	}
	if err := a.action(readAll, "other", "delete"); err != errAccessDenied {
		t.Fatalf("view all allowed deleting another account's job: %v", err)
	}
	j := a.snapshot(readAll)["jobs"].([]job)[0]
	if j.Path != "" || j.Log != "" || j.OutDir != "" {
		t.Fatalf("read-only job exposed paths: %#v", j)
	}
	scopeOnly := identityPrincipal{ID: "alice", Permissions: []string{"jobs.viewAll", "jobs.controlAll"}}
	if len(a.snapshot(scopeOnly)["jobs"].([]job)) != 0 || a.action(scopeOnly, "other", "cancel") == nil {
		t.Fatal("scope implicitly granted a base permission")
	}
}
