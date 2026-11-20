package assist

import (
	"encoding/json"
	"strings"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func planRequest(config Config, diagnostics string) (string, []byte) {
	var path string
	body := map[string]any{"model": config.Model}
	switch config.protocol() {
	case "anthropic":
		path = "/messages"
		body["system"] = instructions
		body["messages"] = []message{{Role: "user", Content: diagnostics}}
		body["max_tokens"] = 1024
	case "openai-responses":
		path = "/responses"
		body["instructions"] = instructions
		body["input"] = diagnostics
		body["store"] = false
	default:
		path = "/chat/completions"
		body["messages"] = []message{{Role: "system", Content: instructions}, {Role: "user", Content: diagnostics}}
	}
	payload, _ := json.Marshal(body)
	return path, payload
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func planReply(protocol string, data []byte) string {
	var reply struct {
		Choices []struct {
			Message message `json:"message"`
		} `json:"choices"`
		Content []contentBlock `json:"content"`
		Output  []struct {
			Type    string         `json:"type"`
			Phase   string         `json:"phase"`
			Content []contentBlock `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return ""
	}
	var text strings.Builder
	switch protocol {
	case "anthropic":
		for _, block := range reply.Content {
			if block.Type == "text" {
				text.WriteString(block.Text)
			}
		}
	case "openai-responses":
		for _, item := range reply.Output {
			if item.Type != "message" || item.Phase == "commentary" {
				continue
			}
			for _, block := range item.Content {
				if block.Type == "output_text" {
					text.WriteString(block.Text)
				}
			}
		}
	default:
		if len(reply.Choices) > 0 {
			return reply.Choices[0].Message.Content
		}
	}
	return text.String()
}
