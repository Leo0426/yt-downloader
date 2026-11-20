package assist

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSameServiceAndProtocolValidation(t *testing.T) {
	old := Config{BaseURL: "https://example.test/v1/", Model: "old"}
	service := Config{Protocol: "openai", BaseURL: "https://example.test/v1", Model: "new"}
	if !old.SameService(service) {
		t.Fatal("legacy chat settings should use the same endpoint")
	}
	for _, protocol := range []string{"anthropic", "openai-responses"} {
		service.Protocol = protocol
		if old.SameService(service) || service.Validate() != nil {
			t.Errorf("incorrect service identity for %s", protocol)
		}
	}
	if err := (Config{Protocol: "unsupported"}).Validate(); err == nil {
		t.Fatal("accepted unknown protocol")
	}
}

func TestPlanOtherProtocols(t *testing.T) {
	input := Input{Summary: "网络中断", Signals: []string{"connection_reset"}, Format: "mp4"}
	for _, protocol := range []string{"anthropic", "openai-responses"} {
		t.Run(protocol, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Error("expected generation POST")
				}
				var request struct {
					Model, System, Instructions, Input string
					Messages                           []message
					MaxTokens                          int   `json:"max_tokens"`
					Store                              *bool `json:"store"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "gateway-model" {
					t.Errorf("invalid request: %v", err)
				}
				diagnostics := request.Input
				if protocol == "anthropic" {
					if r.URL.Path != "/proxy/v1/messages" || r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "test-secret" || r.Header.Get("anthropic-version") != "2023-06-01" {
						t.Error("incorrect Anthropic endpoint or headers")
					}
					if request.System != instructions || request.MaxTokens <= 0 || len(request.Messages) != 1 || request.Messages[0].Role != "user" {
						t.Error("incorrect Messages body")
						w.WriteHeader(400)
						return
					}
					diagnostics = request.Messages[0].Content
					w.Write([]byte(`{"content":[{"type":"thinking","thinking":"internal"},{"type":"text","text":"{\"action\":\"network\","},{"type":"text","text":"\"advice\":\"检查网络。\"}"}]}`))
				} else {
					if r.URL.Path != "/proxy/v1/responses" || r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("x-api-key") != "" || r.Header.Get("anthropic-version") != "" {
						t.Error("incorrect Responses endpoint or headers")
					}
					if request.Instructions != instructions || request.Store == nil || *request.Store || len(request.Messages) != 0 {
						t.Error("incorrect Responses body")
					}
					w.Write([]byte(`{"output":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"internal"}]},{"type":"message","phase":"commentary","content":[{"type":"output_text","text":"检查失败原因中。"}]},{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"{\"action\":\"network\","},{"type":"output_text","text":"\"advice\":\"检查网络。\"}"}]}]}`))
				}
				var sent Input
				if err := json.Unmarshal([]byte(diagnostics), &sent); err != nil || !reflect.DeepEqual(input, sent) {
					t.Errorf("unexpected diagnostic input %#v: %v", sent, err)
				}
			}))
			defer srv.Close()
			config := Config{BaseURL: srv.URL + "/proxy/v1/", Protocol: protocol, Model: "gateway-model"}
			got, err := Plan(context.Background(), config, "test-secret", input)
			if err != nil || got != (Decision{Action: "network", Advice: "检查网络。"}) {
				t.Fatalf("got %#v, error %v", got, err)
			}
		})
	}
}

func TestListModels(t *testing.T) {
	for _, protocol := range []string{"", "openai", "openai-responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/config/v1/models" || r.ContentLength != 0 || strings.Contains(r.URL.RawQuery, "test-secret") {
					t.Errorf("unexpected list request %s %s", r.Method, r.URL)
				}
				if protocol == "anthropic" {
					if r.Header.Get("x-api-key") != "test-secret" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
						t.Error("incorrect list request headers")
					}
					if calls == 1 {
						w.Write([]byte(`{"data":[{"id":"first+model"}],"has_more":true,"last_id":"first+model"}`))
						return
					}
					if calls != 2 || r.URL.Query().Get("after_id") != "first+model" {
						t.Error("incorrect next-page cursor")
					}
				} else if r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("x-api-key") != "" || r.URL.RawQuery != "" {
					t.Error("incorrect OpenAI models request")
				}
				w.Write([]byte(`{"data":[{"id":"first+model"},{"id":"second/model"}]}`))
			}))
			defer srv.Close()
			got, err := ListModels(context.Background(), Config{BaseURL: srv.URL + "/config/v1/", Protocol: protocol}, "test-secret")
			if err != nil || !reflect.DeepEqual(got, []string{"first+model", "second/model"}) {
				t.Fatalf("got %q, error %v", got, err)
			}
		})
	}
}

func TestListModelsHandlesEmptyAndBadLists(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		empty      bool
	}{
		{"empty", `{"data":[]}`, 200, true},
		{"malformed", "not json", 200, false},
		{"wrong format", `{"models":[]}`, 200, false},
		{"invalid id", `{"data":[{"id":"bad\nid"}]}`, 200, false},
		{"secret in id", `{"data":[{"id":"test-secret"}]}`, 200, false},
		{"missing cursor", `{"data":[{"id":"m"}],"has_more":true}`, 200, false},
		{"repeated cursor", `{"data":[{"id":"m"}],"has_more":true,"last_id":"m"}`, 200, false},
		{"denied", "test-secret", 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			got, err := ListModels(context.Background(), Config{BaseURL: srv.URL, Protocol: "anthropic"}, "test-secret")
			if tc.empty {
				if err != nil || got == nil || len(got) != 0 {
					t.Fatalf("empty list: %v %v", got, err)
				}
			} else if err == nil || strings.Contains(err.Error(), "test-secret") || got != nil || calls > 2 {
				t.Fatalf("unexpected result %q, err %v, calls %d", got, err, calls)
			}
		})
	}
}

func TestListLocalOllamaNamespaceWithPlaceholderKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ollama" {
			t.Error("placeholder must still be sent to the local gateway")
		}
		w.Write([]byte(`{"data":[{"id":"ollama/qwen-local"},{"id":"second-model"}]}`))
	}))
	defer srv.Close()
	got, err := ListModels(context.Background(), Config{BaseURL: srv.URL + "/v1"}, "ollama")
	if err != nil || !reflect.DeepEqual(got, []string{"ollama/qwen-local", "second-model"}) {
		t.Fatalf("local models: %q, %v", got, err)
	}
}
