package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"
)

func TestQueueConcurrencyAndLiveLimit(t *testing.T) {
	for _, tool := range []string{"yt-dlp", "ffmpeg", "ffprobe", "deno"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("needs " + tool)
		}
	}
	started := make(chan string, 12)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- r.URL.Path
		<-r.Context().Done()
	}))
	defer server.Close()
	root := t.TempDir()
	a, err := newApplication(context.Background(), root, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()

	prefs := defaultPreferences()
	if err := a.savePreferences(root, &prefs, nil); err != nil {
		t.Fatal(err)
	}
	urls := []string{server.URL + "/first.mp4", server.URL + "/second.mp4", server.URL + "/third.mp4"}
	if count, _, err := a.add(adminActor, urls, "", nil, ""); err != nil || count != len(urls) {
		t.Fatalf("queue test jobs: %d %v", count, err)
	}
	waitStart := func() string {
		t.Helper()
		select {
		case path := <-started:
			return path
		case <-time.After(10 * time.Second):
			t.Fatal("expected a task to enter the download tool")
		}
		return ""
	}
	first, second := waitStart(), waitStart()
	if first == second || first == "/third.mp4" || second == "/third.mp4" {
		t.Fatalf("unexpected first wave: %s, %s", first, second)
	}
	jobs := a.snapshot(adminActor)["jobs"].([]job)
	if jobs[0].Status != "running" || jobs[1].Status != "running" || jobs[2].Status != "queued" {
		t.Fatalf("two active downloads should leave one queued: %#v", jobs)
	}
	prefs.Concurrency = 1
	if err := a.savePreferences(root, &prefs, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.action(adminActor, jobs[0].ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for a.snapshot(adminActor)["jobs"].([]job)[0].Status != "canceled" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	jobs = a.snapshot(adminActor)["jobs"].([]job)
	if jobs[0].Status != "canceled" || jobs[1].Status != "running" || jobs[2].Status != "queued" {
		t.Fatalf("lowering the limit should preserve the remaining worker: %#v", jobs)
	}
	if err := a.action(adminActor, jobs[1].ID, "delete"); err != nil {
		t.Fatal(err)
	}
	if third := waitStart(); third != "/third.mp4" {
		t.Fatalf("expected next queued task, got %s", third)
	}
	remaining := a.snapshot(adminActor)["jobs"].([]job)
	if len(remaining) != 2 || remaining[0].ID != jobs[0].ID || remaining[1].ID != jobs[2].ID {
		t.Fatalf("worker recreated its deleted queue entry: %#v", remaining)
	}
}
