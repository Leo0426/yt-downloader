package download

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishConfinedToCurrentOutputRoot(t *testing.T) {
	ctx := context.Background()
	cache, mediaDir, outside := t.TempDir(), t.TempDir(), t.TempDir()
	mediaRoot, err := os.OpenRoot(mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	defer mediaRoot.Close()
	e := NewWithOutputRoot(cache, mediaRoot)
	subfolder := filepath.Join(mediaDir, "library")
	j, err := e.newJob(ctx, "https://example.test/clip", subfolder, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := media{ID: "test", Extractor: "generic", Title: "clip"}
	src := filepath.Join(cache, "input")
	if err := os.WriteFile(src, []byte("cached media"), 0600); err != nil {
		t.Fatal(err)
	}
	rec, err := j.store(m, src, "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	result, err := j.publish(m, rec)
	if err != nil || result != filepath.Join(subfolder, "clip.mp4") {
		t.Fatalf("publish: %q %v", result, err)
	}
	if repeated, err := j.publish(m, rec); err != nil || result != repeated {
		t.Fatalf("reuse: %q %v", repeated, err)
	}
	if err := os.Rename(subfolder, filepath.Join(mediaDir, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, subfolder); err != nil {
		t.Skip("directory symlinks unavailable")
	}
	if _, err := j.publish(m, rec); err == nil {
		t.Fatal("used removed output folder")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside content changed: %v, %v", entries, err)
	}
}
