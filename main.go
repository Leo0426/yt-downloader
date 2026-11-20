package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"yt-downloader/internal/instance"
)

//go:embed web/*
var assets embed.FS

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	defaultRoot, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	data := flag.String("data-dir", os.Getenv("YTDL_DATA_DIR"), "application data, caches and logs")
	web := flag.Bool("web", false, "serve the multi-user browser version without opening a desktop window")
	listen := flag.String("listen", envOr("YTDL_LISTEN", "127.0.0.1:8080"), "Web HTTP listen address")
	publicURL := flag.String("public-url", os.Getenv("YTDL_PUBLIC_URL"), "public Web origin, including scheme and optional port")
	downloadRoot := flag.String("download-root", os.Getenv("YTDL_DOWNLOAD_ROOT"), "Web export root (default: ~/Downloads/YT Downloader Web)")
	noBrowser := flag.Bool("no-browser", false, "print the private launch URL instead of opening a browser")
	parentPipe := flag.Bool("parent-pipe", false, "stop when the desktop host closes stdin")
	flag.Parse()
	dataPath := *data
	if dataPath == "" {
		name := "yt-downloader"
		if *web {
			name = "yt-downloader-web"
		}
		dataPath = filepath.Join(defaultRoot, name)
	}
	root, err := filepath.Abs(dataPath)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	lock, err := instance.Hold(filepath.Join(root, "instance.lock"))
	if err != nil {
		return err
	}
	entryFile := filepath.Join(root, "session.url")
	if lock == nil {
		if *web {
			return errors.New("数据目录正在由其他实例使用。请停止原实例或通过 -data-dir 选择另一个目录")
		}
		for i := 0; i < 25; i++ {
			if raw, e := os.ReadFile(entryFile); e == nil {
				u, e := url.Parse(strings.TrimSpace(string(raw)))
				if e == nil && u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != "" && u.User == nil && u.Path == "/start" {
					if conn, e := net.DialTimeout("tcp", u.Host, 200*time.Millisecond); e == nil {
						conn.Close()
						if err := checkAccountService(u); err != nil {
							return err
						}
						return showURL(u.String(), *noBrowser)
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("下载程序已运行，暂时无法打开界面，请稍后重试")
	}
	defer lock.Close()
	appLog, err := os.OpenFile(filepath.Join(root, "app.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer appLog.Close()
	logger := log.New(appLog, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *parentPipe {
		go func() {
			io.Copy(io.Discard, os.Stdin)
			stop()
		}()
	}
	addr := "127.0.0.1:0"
	if *web {
		addr = *listen
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer l.Close()

	var site httpSite
	var launchToken, exports string
	if *web {
		site, err = webSite(*listen, l.Addr(), *publicURL)
		if err != nil {
			return err
		}
		exports = *downloadRoot
		if exports == "" {
			exports, err = defaultOutputDirectory()
			if err != nil {
				return err
			}
			exports = filepath.Join(exports, "YT Downloader Web")
		}
	} else {
		site.origin = &url.URL{Scheme: "http", Host: l.Addr().String()}
		var secret [32]byte
		if _, err = rand.Read(secret[:]); err != nil {
			return err
		}
		launchToken = hex.EncodeToString(secret[:])
	}
	app, err := newApplicationWithDownloadRoot(ctx, root, logger, exports)
	if err != nil {
		return err
	}
	defer app.close()
	content, err := fs.Sub(assets, "web")
	if err != nil {
		return err
	}
	launch := site.origin.String()
	if *web {
		site.prepareSetup(app.identity)
	} else {
		launch += "/start?token=" + launchToken
		if err = atomicWrite(entryFile, []byte(launch)); err != nil {
			return err
		}
		defer os.Remove(entryFile)
	}

	server := &http.Server{Handler: siteRoutes(app, site, launchToken, http.FS(content), stop), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(l) }()
	if *web {
		fmt.Printf("YT Downloader Web · %s\n", launch)
	} else if err = showURL(launch, *noBrowser); err != nil {
		logger.Print(err)
		fmt.Println(launch)
	}
	select {
	case <-ctx.Done():
		timeout, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return server.Shutdown(timeout)
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// A new desktop shell can encounter an older core still holding the data lock.
// Only reuse an instance which actually presents the account gate.
func checkAccountService(launch *url.URL) error {
	endpoint := &url.URL{Scheme: launch.Scheme, Host: launch.Host, Path: "/api/auth/session"}
	request, err := http.NewRequest(http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	request.AddCookie(&http.Cookie{Name: "ytd_session_" + launch.Port(), Value: launch.Query().Get("token")})
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("无法确认已运行下载服务的账户状态，请完全退出后再打开")
	}
	defer response.Body.Close()
	var session struct {
		SetupRequired *bool           `json:"setupRequired"`
		User          json.RawMessage `json:"user"`
		Deployment    string          `json:"deployment"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&session) != nil || session.SetupRequired == nil || string(session.User) != "null" || (session.Deployment != "" && session.Deployment != "desktop") {
		return errors.New("已运行的下载服务不支持账户登录，请完全退出旧版本后再打开")
	}
	return nil
}

func showURL(addr string, printOnly bool) error {
	if printOnly {
		fmt.Println(addr)
		return nil
	}
	return openSystem(addr)
}

func openSystem(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		if strings.HasPrefix(target, "http://127.0.0.1:") {
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
		} else {
			cmd = exec.Command("explorer.exe", target)
		}
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
