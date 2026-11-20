package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var adminActor = identityPrincipal{ID: "test-admin", Permissions: builtinRoles()["admin"].Permissions}

func bareApp(t *testing.T) *application {
	t.Helper()
	d := t.TempDir()
	identity, err := newIdentityStore(d)
	if err != nil {
		t.Fatal(err)
	}
	return &application{identity: identity, root: d, state: state{OutDir: d, Preferences: defaultPreferences()}, wake: make(chan struct{}, 1), logger: log.New(io.Discard, "", 0)}
}
func TestBatchSubmission(t *testing.T) {
	a := bareApp(t)
	urls := []string{`https\://www\.youtube.com/watch?t=15s&v=5OiyIV85E2w\`, "https://youtu.be/5OiyIV85E2w"}
	added, dups, err := a.add(adminActor, urls, "", nil, "")
	if err != nil || added != 1 || dups != 1 {
		t.Fatalf("%d %d %v", added, dups, err)
	}
	_, _, err = a.add(adminActor, []string{"https://example.com/video", "invalid"}, "", nil, "")
	if err == nil || len(a.Jobs) != 1 {
		t.Fatal("invalid submissions must not partially enqueue")
	}
	if err = a.action(adminActor, a.Jobs[0].ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if err = a.action(adminActor, a.Jobs[0].ID, "retry"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(a.root, "queue.json"))
	if err != nil || !strings.Contains(string(raw), `"status": "queued"`) {
		t.Fatal("retry not persisted", err)
	}
}
func TestLocalSession(t *testing.T) {
	a := bareApp(t)
	const origin = "http://127.0.0.1:54321"
	h := routes(a, origin, "secret", http.Dir(a.root), func() {})
	request := func(method, path, body, from string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		r.Header.Set("Origin", from)
		r.Header.Set("Content-Type", "application/json")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/state", "", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	launch := request("GET", "/start?token=secret", "", "")
	c := launch.Result().Cookies()[0]
	if !c.HttpOnly || launch.Code != 303 {
		t.Fatal("invalid launch session")
	}
	if w := request("POST", "/api/jobs", `{"urls":["http://example.com"]}`, "https://example.com", c); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/api/jobs", `{"urls":["http://example.com"]}`, origin, c); w.Code != 401 {
		t.Fatalf("launch cookie alone started a download: %d", w.Code)
	}
	user := setupTestAccount(t, h, origin, c)
	if w := request("POST", "/api/jobs", `{"urls":["http://example.com"]}`, origin, c, user); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestCacheClearKeepsOutputsAndHonorsQueue(t *testing.T) {
	a := bareApp(t)
	a.cache = filepath.Join(a.root, "cache")
	object := filepath.Join(a.cache, "objects", "test")
	if err := os.MkdirAll(object, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(object, "video.mp4"), []byte("cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.add(adminActor, []string{"https://example.com/v"}, object, nil, ""); err == nil {
		t.Fatal("outputs inside cache should be rejected")
	}
	dst := filepath.Join(a.root, "exports")
	if _, _, err := a.add(adminActor, []string{"https://example.com/v"}, dst, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.clearCache(); err == nil {
		t.Fatal("queued job must prevent cleanup")
	}
	if err := a.action(adminActor, a.Jobs[0].ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dst, "export.mp4")
	if err := os.WriteFile(file, []byte("export"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.clearCache(); err != nil {
		t.Fatal(err)
	}
	stats, err := a.cacheStats()
	if err != nil || stats.Bytes != 0 {
		t.Fatalf("stats=%#v err=%v", stats, err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyQueueKeepsDefaultsAndFreezesJobSettings(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "queue.json"), []byte(`{"paused":true,"jobs":[{"id":"old","status":"queued"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newApplication(context.Background(), root, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	config := a.snapshot(adminActor)["preferences"].(preferences)
	if config != defaultPreferences() {
		t.Fatalf("legacy settings were not migrated: %#v", config)
	}
	config.Download.MaxResolution = 720
	config.Download.Retries = 0
	if err := a.savePreferences(root, &config, nil); err != nil {
		t.Fatal(err)
	}
	urls := []string{"https://example.com/clip.mp4"}
	if added, _, err := a.add(adminActor, urls, "", nil, ""); err != nil || added != 1 {
		t.Fatalf("could not queue configured task: %d %v", added, err)
	}
	limit := 480
	if added, _, err := a.add(adminActor, urls, "", &limit, ""); err != nil || added != 1 {
		t.Fatalf("could not queue second rendition: %d %v", added, err)
	}
	if err := a.savePreferences(root, &preferences{}, nil); err == nil {
		t.Fatal("accepted malformed settings")
	}
	snapshot := a.snapshot(adminActor)
	jobs := snapshot["jobs"].([]job)
	if snapshot["preferences"] != config || jobs[0].Options != defaultPreferences().Download ||
		jobs[1].Options.MaxResolution != 720 || jobs[2].Options.MaxResolution != 480 ||
		jobs[2].Options.Retries != 0 || jobs[2].Status != "queued" {
		t.Fatalf("incorrect snapshot after import and queueing: %#v", snapshot)
	}
}
