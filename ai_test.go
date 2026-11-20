package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yt-downloader/internal/assist"
	"yt-downloader/internal/download"
)

func TestModelListUsesDraftWithoutSavingIt(t *testing.T) {
	type credentials struct{ bearer, apiKey string }
	received := make(chan credentials, 8)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- credentials{r.Header.Get("Authorization"), r.Header.Get("x-api-key")}
		io.WriteString(w, `{"data":[{"id":"text-model"}],"has_more":false}`)
	}))
	defer provider.Close()

	a := bareApp(t)
	prefs := defaultPreferences()
	prefs.AI = assist.Config{BaseURL: provider.URL + "/v1", Model: "saved-model"}
	savedKey := "saved-key"
	if err := a.savePreferences(a.OutDir, &prefs, &savedKey); err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(a.root, "queue.json")
	before, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	const origin = "http://127.0.0.1:31001"
	h := routes(a, origin, "session", http.Dir(a.root), func() {})
	launch := &http.Cookie{Name: "ytd_session_31001", Value: "session"}
	user := setupTestAccount(t, h, origin, launch)
	draftKey := "draft-key"
	for _, tc := range []struct {
		name, protocol, path string
		key                  *string
		want                 credentials
	}{
		{"legacy OpenAI session", "openai", "/v1/", nil, credentials{bearer: "Bearer saved-key"}},
		{"different protocol", "anthropic", "/v1", nil, credentials{}},
		{"different OpenAI protocol", "openai-responses", "/v1", nil, credentials{}},
		{"different endpoint", "openai", "/other", nil, credentials{}},
		{"draft key", "anthropic", "/v1", &draftKey, credentials{apiKey: draftKey}},
		{"draft overrides saved key", "openai", "/v1", &draftKey, credentials{bearer: "Bearer draft-key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"ai": assist.Config{BaseURL: provider.URL + tc.path, Protocol: tc.protocol}}
			if tc.key != nil {
				payload["aiKey"] = *tc.key
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, origin+"/api/ai/models", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", origin)
			r.AddCookie(launch)
			r.AddCookie(user)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, r)
			var reply struct{ Models []string }
			if resp.Code != 200 || json.Unmarshal(resp.Body.Bytes(), &reply) != nil || !reflect.DeepEqual(reply.Models, []string{"text-model"}) {
				t.Fatalf("model list: HTTP %d %s", resp.Code, resp.Body)
			}
			select {
			case got := <-received:
				if got != tc.want {
					t.Error("model list sent the wrong credentials")
				}
			default:
				t.Error("model list did not query the provider")
			}
		})
	}
	after, err := os.ReadFile(dataPath)
	if err != nil || !bytes.Equal(after, before) || a.Preferences != prefs || a.aiKey != savedKey {
		t.Fatal("model discovery changed saved preferences or credentials", err)
	}
	prefs.AI.Protocol = "openai"
	if err := a.savePreferences("", &prefs, nil); err != nil || a.aiKey != savedKey {
		t.Fatal("explicit OpenAI should retain an existing OpenAI session", err)
	}
	prefs.AI.Protocol = "anthropic"
	if err := a.savePreferences("", &prefs, nil); err != nil || a.aiKey != "" {
		t.Fatal("switching protocols should clear the old session key", err)
	}
}

// Start with a paused queue saved by an older build, which has no diagnostic signals.
func failedAIQueue(t *testing.T, modelURL, mediaURL string) *application {
	t.Helper()
	seed := bareApp(t)
	seed.Preferences = defaultPreferences()
	seed.Preferences.AI = assist.Config{BaseURL: modelURL, Model: "test"}
	seed.Paused = true
	logPath := filepath.Join(seed.root, "prior-attempt.log")
	if err := os.WriteFile(logPath, []byte("\n保存至 private-folder\nERROR: connection timed out"), 0600); err != nil {
		t.Fatal(err)
	}
	options := download.DefaultOptions()
	options.Retries = 0
	seed.Jobs = []job{{
		ID: "retry", Status: "failed", URL: mediaURL, Options: options, OutDir: seed.root,
		Log: logPath, Failure: &download.Failure{Summary: "连接源站超时", Advice: "检查网络"},
	}}
	if err := seed.save(); err != nil {
		t.Fatal(err)
	}
	return startAIQueue(t, seed.root)
}

func startAIQueue(t *testing.T, root string) *application {
	t.Helper()
	a, err := newApplication(context.Background(), root, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	return a
}

func awaitAIJob(t *testing.T, a *application, status string) job {
	t.Helper()
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); {
		current := a.snapshot(adminActor)["jobs"].([]job)[0]
		if current.Status == status {
			return current
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected %s, got %#v", status, a.snapshot(adminActor)["jobs"])
	return job{}
}

func TestAIRetryPreservesDownloadFailure(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			const secret = "test-session-key"
			var calls atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+secret {
					t.Error("model did not receive session credential")
				}
				body, _ := io.ReadAll(r.Body)
				if !bytes.Contains(body, []byte(`\"timeout\"`)) || bytes.Contains(body, []byte("private-signature")) || bytes.Contains(body, []byte("private-folder")) {
					t.Error("incorrect model diagnostics")
				}
				w.WriteHeader(code)
				io.WriteString(w, `{"choices":[{"message":{"content":"{\"action\":\"stop\",\"advice\":\"请先检查网络接入。\"}"}}]}`)
			}))
			t.Cleanup(model.Close)
			a := failedAIQueue(t, model.URL, "http://127.0.0.1:1/private?token=private-signature")
			prefs := a.snapshot(adminActor)["preferences"].(preferences)
			key := secret
			if err := a.savePreferences("", &prefs, &key); err != nil {
				t.Fatal(err)
			}
			if err := a.action(adminActor, "retry", "ai-retry"); err != nil {
				t.Fatal(err)
			}
			if err := a.action(adminActor, "retry", "ai-retry"); err == nil {
				t.Fatal("accepted a second start for a queued task")
			}
			pending := awaitAIJob(t, a, "queued")
			if pending.AIRetry == nil || !reflect.DeepEqual(pending.Failure.Signals, []string{"timeout"}) || calls.Load() != 0 {
				t.Fatalf("paused assisted retry lost its diagnosis or started early: %#v", pending)
			}
			if err := a.pauseQueue(false); err != nil {
				t.Fatal(err)
			}
			current := awaitAIJob(t, a, "failed")
			if current.AINote == "" || current.AIRetry != nil || !reflect.DeepEqual(current.Failure, pending.Failure) || calls.Load() != 1 {
				t.Fatalf("assistance obscured the original failure: %#v", current)
			}
			if code == http.StatusOK && !strings.Contains(current.AINote, "请先检查网络接入") {
				t.Fatalf("model advice missing: %q", current.AINote)
			}
			if err := a.pauseQueue(true); err != nil {
				t.Fatal(err)
			}
			if err := a.action(adminActor, "retry", "retry"); err != nil {
				t.Fatal(err)
			}
			retry := awaitAIJob(t, a, "queued")
			if retry.AIRetry != nil || retry.AINote != "" || retry.Options != pending.Options {
				t.Fatalf("ordinary retry carried over assistance: %#v", retry)
			}
			stateJSON, err := json.Marshal(a.snapshot(adminActor))
			if err != nil {
				t.Fatal(err)
			}
			queueJSON, err := os.ReadFile(filepath.Join(a.root, "queue.json"))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(stateJSON, []byte(secret)) || bytes.Contains(queueJSON, []byte(secret)) || a.snapshot(adminActor)["aiKeySet"] != true {
				t.Fatal("invalid session credential visibility")
			}
			prefs.AI.BaseURL += "/different-service"
			if err := a.savePreferences("", &prefs, nil); err != nil || a.snapshot(adminActor)["aiKeySet"] != false {
				t.Fatal("changing endpoints retained the prior credential", err)
			}
		})
	}
}

func TestAIJobCancelStopsModelRequest(t *testing.T) {
	entered, disconnected := make(chan struct{}), make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(disconnected)
	}))
	t.Cleanup(model.Close)
	a := failedAIQueue(t, model.URL, "http://127.0.0.1:1/source")
	if err := a.action(adminActor, "retry", "ai-retry"); err != nil {
		t.Fatal(err)
	}
	if err := a.pauseQueue(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(45 * time.Second):
		t.Fatal("model request did not start")
	}
	if err := a.action(adminActor, "retry", "cancel"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel left the model request running")
	}
	if j := awaitAIJob(t, a, "canceled"); j.AIRetry != nil || a.action(adminActor, j.ID, "ai-retry") == nil {
		t.Fatal("canceled task kept or accepted an assisted attempt")
	}
}

func TestAIRetryResumesSelectedStrategyAndDownloads(t *testing.T) {
	for _, name := range []string{"yt-dlp", "ffmpeg", "ffprobe", "deno"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("integration fixture requires " + name)
		}
	}
	media := filepath.Join(t.TempDir(), "fixture.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=blue:s=32x32:r=5", "-t", "1", "-c:v", "mpeg4", media).CombinedOutput(); err != nil {
		t.Fatalf("create fixture: %v %s", err, out)
	}
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"action\":\"ipv4\",\"advice\":\"尝试 IPv4 连接。\"}"}}]}`)
	}))
	t.Cleanup(model.Close)
	entered := make(chan struct{}, 1)
	var resume atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !resume.Load() {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		http.ServeFile(w, r, media)
	}))
	t.Cleanup(source.Close)
	a := failedAIQueue(t, model.URL, source.URL+"/video.mp4")
	prefs := a.snapshot(adminActor)["preferences"].(preferences)
	key := "first-session-only"
	if err := a.savePreferences("", &prefs, &key); err != nil {
		t.Fatal(err)
	}
	if err := a.action(adminActor, "retry", "ai-retry"); err != nil {
		t.Fatal(err)
	}
	if err := a.pauseQueue(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(45 * time.Second):
		t.Fatalf("assisted task did not reach the download: %#v", a.snapshot(adminActor)["jobs"])
	}
	if err := a.pauseQueue(true); err != nil {
		t.Fatal(err)
	}
	a.close()
	resume.Store(true)
	restarted := startAIQueue(t, a.root)
	if restarted.snapshot(adminActor)["aiKeySet"] != false {
		t.Fatal("application restored a previous session's credential")
	}
	pending := awaitAIJob(t, restarted, "queued")
	if pending.AIRetry == nil || pending.AIRetry.Strategy != "ipv4" || pending.AINote == "" {
		t.Fatalf("restart lost the chosen strategy: %#v", pending)
	}
	if err := restarted.pauseQueue(false); err != nil {
		t.Fatal(err)
	}
	finished := awaitAIJob(t, restarted, "done")
	if finished.AIRetry != nil || finished.Failure != nil || calls.Load() != 1 {
		t.Fatalf("incorrect recovery state or repeated model request: %#v", finished)
	}
	if file, err := os.Stat(finished.Path); err != nil || file.Size() == 0 {
		t.Fatal("downloaded artifact missing", err)
	}
	data, err := os.ReadFile(finished.Log)
	if err != nil || !strings.Contains(string(data), "--force-ipv4") {
		t.Fatal("download did not use chosen strategy", err)
	}
}
