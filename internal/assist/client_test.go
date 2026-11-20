package assist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestConfig(t *testing.T) {
	if err := (Config{}).Validate(); err != nil {
		t.Fatalf("disabled configuration: %v", err)
	}
	for _, base := range []string{
		"https://models.example/custom/v1/",
		"http://localhost:11434/v1",
		"http://127.0.0.2/v1",
		"http://[::1]:8080/v1",
		"https://127.0.0.1/v1",
	} {
		c := Config{BaseURL: base, Model: "local-model:8b"}
		if err := c.Validate(); err != nil {
			t.Errorf("base %q: %v", base, err)
		}
	}
	for _, base := range []string{
		"", "https:///v1", "ftp://localhost/model", "http://models.example/v1",
		"http://localhost.example/v1", "https://user:secret@models.example/v1",
		"https://models.example/v1?", "https://models.example/v1#secret",
		"https://models.example:65536/v1", "https://models.example:/v1",
		"https://models.example/" + strings.Repeat("x", 2048),
	} {
		if err := (Config{BaseURL: base, Model: "model"}).Validate(); err == nil {
			t.Errorf("accepted base %q", base)
		}
	}
	for _, model := range []string{"", " ", "a\nb", strings.Repeat("x", 257)} {
		if err := (Config{BaseURL: "https://models.example/v1", Model: model}).Validate(); err == nil {
			t.Errorf("accepted model %q", model)
		}
	}
}

func chatReply(content string) string {
	payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	return string(payload)
}

func TestPlanChatCompletions(t *testing.T) {
	input := Input{Summary: "连接超时", Format: "mp4", YouTube: true, TimeoutSeconds: 25, Retries: 3, Signals: []string{"timeout"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/service/v1/chat/completions" || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			Model    string
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "demo-model" || len(request.Messages) != 2 {
			t.Errorf("invalid request structure: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
			t.Error("missing instructions or diagnostics")
		}
		var sent Input
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &sent); err != nil || !reflect.DeepEqual(sent, input) {
			t.Errorf("wrong diagnostic input: %#v, %v", sent, err)
		}
		w.Write([]byte(chatReply("```json\n{\"action\":\"network\",\"advice\":\"延长超时后重试。\"}\n```")))
	}))
	defer srv.Close()
	config := Config{BaseURL: srv.URL + "/service/v1/", Model: "demo-model"}
	got, err := Plan(context.Background(), config, "secret", input)
	if err != nil || got != (Decision{Action: "network", Advice: "延长超时后重试。"}) {
		t.Fatalf("decision %#v, error %v", got, err)
	}
}

func TestPlanUntrustedResponses(t *testing.T) {
	const privateDetail = "private-server-detail"
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"bad action", chatReply(`{"action":"run-command","advice":"private-server-detail"}`), 200},
		{"echoed key", chatReply(`{"action":"stop","advice":"check private-server-detail"}`), 200},
		{"empty advice", chatReply(`{"action":"stop","advice":"  "}`), 200},
		{"invalid content", chatReply(privateDetail), 200},
		{"invalid envelope", privateDetail, 200},
		{"missing choice", `{"choices":[]}`, 200},
		{"oversize", strings.Repeat("x", (128<<10)+1), 200},
		{"forbidden", privateDetail, 403},
		{"unavailable", privateDetail, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			got, err := Plan(context.Background(), Config{BaseURL: srv.URL, Model: "local"}, privateDetail, Input{})
			if got != (Decision{}) || err == nil || strings.Contains(err.Error(), privateDetail) {
				t.Fatalf("unexpected decision %#v or unsafe error %v", got, err)
			}
		})
	}
}

func TestPlanPreventRedirectAndOversizedInput(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("redirect forwarded")
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	config := Config{BaseURL: srv.URL, Model: "local"}
	_, err := Plan(context.Background(), config, "test-secret", Input{})
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("redirect: %v", err)
	}
	for _, input := range []Input{{Summary: strings.Repeat("x", 1025)}, {Signals: make([]string, 33)}, {Signals: []string{strings.Repeat("x", 129)}}} {
		if _, err := Plan(context.Background(), config, "", input); err == nil || !strings.Contains(err.Error(), "长度") {
			t.Fatalf("input limit: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Plan(ctx, config, "", Input{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation: %v", err)
	}
}
