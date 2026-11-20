// Command zip packages desktop output, retaining executable modes and bundle symlinks.
package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: zip source-directory output.zip")
	}
	if err := pack(os.Args[1], os.Args[2]); err != nil {
		log.Fatal(err)
	}
}
func pack(dir, target string) error {
	f, err := os.CreateTemp(filepath.Dir(target), ".package-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	w := zip.NewWriter(f)
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(relative)
		if d.IsDir() {
			h.Name += "/"
		}
		if filepath.Ext(p) == ".sh" {
			h.SetMode(0755)
		}
		if !strings.HasSuffix(h.Name, "/") && info.Mode().IsRegular() {
			h.Method = zip.Deflate
		}
		out, err := w.CreateHeader(h)
		if err != nil || d.IsDir() {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			destination, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(out, destination)
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(out, in)
		return err
	})
	if err = errors.Join(walkErr, w.Close(), f.Close()); err != nil {
		return err
	}
	if err = os.Chmod(f.Name(), 0644); err != nil {
		return err
	}
	return os.Rename(f.Name(), target)
}
