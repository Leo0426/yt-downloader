package download

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRecoveryStrategiesAreAttemptScoped(t *testing.T) {
	initial := DefaultOptions()
	initial.MaxResolution = 720
	initial.RateLimitKiB = 200
	getArg := func(args []string, flag string) string {
		i := slices.Index(args, flag)
		if i < 0 || i+1 >= len(args) {
			return ""
		}
		return args[i+1]
	}
	for _, test := range []struct {
		strategy, format, selector, timeout, retries string
		audio                                        bool
	}{
		{"", "mp4", videoSelector, "25", "3", false},
		{"network", "mp4", videoSelector, "60", "5", false},
		{"ipv4", "mp4", videoSelector, "25", "3", false},
		{"combined", "mkv", "b[protocol=https]/b/" + videoSelector, "25", "3", false},
		{"combined", "mp3", "b[protocol=https]/b/ba/b/ba*", "25", "3", true},
	} {
		options := initial
		options.OutputFormat = test.format
		regular, _ := json.Marshal(options)
		profile := options.Profile()
		options.RecoveryStrategy = test.strategy
		j := job{Engine: New(t.TempDir()), options: options}
		args := j.args()
		if getArg(args, "-f") != test.selector || getArg(args, "--socket-timeout") != test.timeout ||
			getArg(args, "--retries") != test.retries || getArg(args, "--fragment-retries") != test.retries ||
			getArg(args, "--extractor-retries") != test.retries || getArg(args, "--limit-rate") != "200K" ||
			slices.Contains(args, "--force-ipv4") != (test.strategy == "ipv4") ||
			slices.Contains(args, "--retry-sleep") != (test.strategy == "network") ||
			(!test.audio && getArg(args, "-S") != "res:720") || (test.audio && slices.Contains(args, "-S")) {
			t.Fatalf("%s %s: incorrect arguments %q", test.strategy, test.format, args)
		}
		stored, err := json.Marshal(j.options)
		if err != nil || !bytes.Equal(regular, stored) || j.options.Profile() != profile {
			t.Fatalf("strategy %q changed settings or output identity", test.strategy)
		}
	}
	j := job{Engine: New(t.TempDir()), options: Options{RecoveryStrategy: "network", TimeoutSeconds: 150, Retries: 9}}
	if args := j.args(); getArg(args, "--socket-timeout") != "180" || getArg(args, "--retries") != "10" {
		t.Fatalf("network recovery exceeded limits: %q", args)
	}
	j.options.RecoveryStrategy = "--exec"
	if j.options.Validate() == nil {
		t.Fatal("unrecognized strategy accepted")
	}
}

func TestReadSignalsUsesOnlyLabelsFromLatestAttempt(t *testing.T) {
	log := filepath.Join(t.TempDir(), "job.log")
	const latest = "ERROR: [youtube] M7AFeRw44zQ: HTTP Error 403: Forbidden (https://host/private?token=secret)\n" +
		"ERROR: [youtube] Sign in to confirm your age.\n"
	data := "\n[old] https://example.com\n保存至 /old\nHTTP Error 429\n" + strings.Repeat("x", 66000) +
		"\n[new] https://example.com\n保存至 /private video\n$ yt-dlp -- https://host/ipv6\n" + latest
	if err := os.WriteFile(log, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	want := []string{"auth_required", "http_403"}
	if got := ReadSignals(log); !slices.Equal(got, want) {
		t.Fatalf("unexpected signals %q", got)
	}
	failure := ReadFailure(log, errors.New("exit status 1"))
	if !slices.Equal(failure.Signals, want) || failure.Summary != "来源要求登录或验证身份" {
		t.Fatalf("unexpected failure %#v", failure)
	}
	if got := ReadSignals(log + ".missing"); len(got) != 0 {
		t.Fatalf("unexpected signals without a log: %q", got)
	}
}
