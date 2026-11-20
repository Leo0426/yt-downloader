package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestWebOutputRootAndPersistentPreferences(t *testing.T) {
	parent := t.TempDir()
	data, media := filepath.Join(parent, "data"), filepath.Join(parent, "downloads")
	start := func() *application {
		t.Helper()
		a, err := newApplicationWithDownloadRoot(context.Background(), data, log.New(io.Discard, "", 0), media)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := start()
	if a.OutDir != media {
		t.Fatalf("initial output directory = %q", a.OutDir)
	}
	if err := a.savePreferences("class/recordings", nil, nil); err != nil {
		t.Fatal(err)
	}
	if a.OutDir != filepath.Join(media, "class", "recordings") {
		t.Fatal(a.OutDir)
	}
	if err := a.pauseQueue(true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.add(adminActor, []string{"https://example.test/sample"}, "", nil, "mp4"); err != nil {
		t.Fatal(err)
	}
	a.close()
	a = start()
	defer a.close()
	if a.OutDir != filepath.Join(media, "class", "recordings") || len(a.Jobs) != 1 || a.Jobs[0].OutDir != a.OutDir {
		t.Fatalf("lost saved directory or task: %#v", a.state)
	}
	if err := a.savePreferences(filepath.Join(media, "other"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if a.OutDir != filepath.Join(media, "other") {
		t.Fatal(a.OutDir)
	}
	for _, out := range []string{filepath.Join(data, "logs"), "../external", filepath.Join(media, "alias", "private")} {
		if out == filepath.Join(media, "alias", "private") {
			if err := os.Symlink(data, filepath.Join(media, "alias")); err != nil {
				continue
			}
		}
		if err := a.savePreferences(out, nil, nil); err == nil {
			t.Fatalf("accepted %q", out)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "private")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside directory was created: %v", err)
	}
}

func TestWebRootOverlap(t *testing.T) {
	parent := t.TempDir()
	data := filepath.Join(parent, "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	dirs := []string{parent, data, filepath.Join(data, "media")}
	alias := filepath.Join(parent, "alias")
	if os.Symlink(data, alias) == nil {
		dirs = append(dirs, filepath.Join(alias, "new"))
	}
	for _, dir := range dirs {
		opened, err := openMediaRoot(data, dir)
		if err == nil {
			opened.Close()
			t.Fatalf("overlap allowed: %s", dir)
		}
	}
}

func TestWebQueueAfterMountChange(t *testing.T) {
	data, media, oldOutput := t.TempDir(), t.TempDir(), t.TempDir()
	legacy := state{OutDir: oldOutput, Preferences: defaultPreferences(), Jobs: []job{{ID: "legacy", OwnerID: adminActor.ID, OutDir: oldOutput, Log: filepath.Join(oldOutput, "old.log"), Status: "running"}}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(data, "queue.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newApplicationWithDownloadRoot(context.Background(), data, log.New(io.Discard, "", 0), media)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if a.Jobs[0].Status != "failed" || a.OutDir != media || a.Jobs[0].Log != filepath.Join(data, "logs", "legacy.log") {
		t.Fatalf("incorrect restored state: %#v", a.state)
	}
	if err := a.action(adminActor, "legacy", "retry"); err == nil {
		t.Fatal("requeued job with invalid original destination")
	}
}

func TestWebOpensOnlyRegisteredFinishedFile(t *testing.T) {
	media, other := t.TempDir(), t.TempDir()
	root, err := os.OpenRoot(media)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := application{downloadRoot: media, mediaRoot: root}
	if err = root.WriteFile("clip.mp4", []byte("clip"), 0600); err != nil {
		t.Fatal(err)
	}
	j := job{OutDir: media, Path: filepath.Join(media, "clip.mp4"), Status: "done"}
	f, err := a.openJobFile(j)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(content) != "clip" {
		t.Fatalf("incorrect download: %q %v", content, err)
	}
	for _, item := range []job{{OutDir: media, Path: j.Path, Status: "queued"}, {OutDir: other, Path: j.Path, Status: "done"}, {OutDir: other, Path: filepath.Join(other, "secret.mp4"), Status: "cached"}, {OutDir: media, Path: filepath.Join(media, "config.json"), Status: "done"}} {
		if f, err := a.openJobFile(item); err == nil {
			f.Close()
			t.Fatalf("unexpected deliverable: %#v", item)
		}
	}
	if err := os.Symlink(j.Path, filepath.Join(media, "alias.mp4")); err != nil {
		return
	}
	j.Path = filepath.Join(media, "alias.mp4")
	if f, err := a.openJobFile(j); err == nil {
		f.Close()
		t.Fatal("served symlink")
	}
}
