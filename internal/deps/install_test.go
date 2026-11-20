package deps

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUpdateOnlyActivatesVerifiedWorkingBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	fixture := func(version string) []byte {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		for _, name := range []string{"ffmpeg", "ffprobe"} {
			f, err := z.Create("bin/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.WriteString(f, "#!/bin/sh\necho "+name+" version "+version+"\n"); err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	var calls atomic.Int32
	data := fixture("9.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write(data) }))
	defer srv.Close()
	sum := sha256.Sum256(data)
	asset := releaseAsset{"fixture.zip", srv.URL, hex.EncodeToString(sum[:]), int64(len(data))}
	m := New(t.TempDir())
	p := bundle{key: "ffmpeg", tools: []string{"ffmpeg", "ffprobe"}}
	if err := m.install(context.Background(), io.Discard, p, asset); err != nil {
		t.Fatal(err)
	}
	if err := m.install(context.Background(), io.Discard, p, asset); err != nil || calls.Load() != 1 {
		t.Fatalf("复用失败: %v, %d requests", err, calls.Load())
	}
	active := m.active(p.key)
	asset.sum = strings.Repeat("0", 64)
	if err := m.install(context.Background(), io.Discard, p, asset); err == nil {
		t.Fatal("incorrect hash accepted")
	}
	if m.active(p.key) != active {
		t.Fatal("failed installation changed active bundle")
	}
	for _, name := range p.tools {
		v, err := m.probe(context.Background(), filepath.Join(active, name), name)
		if err != nil || v != "9.0" {
			t.Fatalf("%s: %s, %v", name, v, err)
		}
	}
}

// Opt-in for release maintainers, as this downloads all provider binaries.
func TestLiveInstall(t *testing.T) {
	if os.Getenv("YTDL_TEST_LIVE_INSTALL") != "1" {
		t.Skip("opt-in network integration test")
	}
	m := New(t.TempDir())
	pkgs, err := bundles(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		t.Run(p.key, func(t *testing.T) {
			a, _, err := latest(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.install(context.Background(), os.Stdout, p, a); err != nil {
				t.Fatal(err)
			}
			for _, name := range p.tools {
				v, err := m.probe(context.Background(), filepath.Join(m.active(p.key), executable(name)), name)
				if err != nil {
					t.Fatal(err)
				}
				t.Log(name, v)
			}
		})
	}
}
