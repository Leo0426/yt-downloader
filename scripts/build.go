// Command build packages the desktop app for the current platform or all six targets.
package main

import (
	"flag"
	"log"
	"os"
	"os/exec"
	"runtime"
)

func main() {
	all := flag.Bool("all", false, "build for macOS, Windows and Linux, amd64 and arm64")
	flag.Parse()
	npm := "npm"
	if runtime.GOOS == "windows" {
		npm = "npm.cmd"
	}
	args := []string{"--prefix", "desktop", "run", "build"}
	if *all {
		args = append(args, "--", "--all")
	}
	cmd := exec.Command(npm, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatal(err)
	}
}
