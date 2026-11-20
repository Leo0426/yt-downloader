package assist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ListModels discovers IDs for a draft configuration, before a model has been chosen.
func ListModels(ctx context.Context, config Config, key string) ([]string, error) {
	if err := config.validateService(); err != nil {
		return nil, err
	}
	if config.BaseURL == "" {
		return nil, errors.New("请先填写 AI 服务地址")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	path := "/models"
	if config.protocol() == "anthropic" {
		path += "?limit=100"
	}
	models := []string{}
	seen := make(map[string]bool)
	cursors := make(map[string]bool)
	remaining := int64(8 << 20)
	// Local OpenAI-compatible servers often use "ollama" as both the documented
	// placeholder credential and the provider namespace in model IDs.
	base, _ := url.Parse(config.BaseURL) // Already validated above.
	localOllama := key == "ollama" && config.protocol() != "anthropic" && loopback(base.Hostname())
	for page := 0; page < 64; page++ {
		data, err := requestAPI(ctx, config, key, http.MethodGet, path, nil, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(data))
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(data, &list); err != nil || list.Data == nil {
			return nil, errors.New("AI 服务未返回有效的模型列表")
		}
		for _, model := range list.Data {
			if !validModelID(model.ID) {
				return nil, errors.New("AI 服务返回了无效的模型 ID")
			}
			credentialCheck := model.ID
			if localOllama {
				credentialCheck = strings.TrimPrefix(credentialCheck, "ollama/")
			}
			if key != "" && strings.Contains(credentialCheck, key) {
				return nil, errors.New("模型列表中包含请求密钥，请检查服务配置")
			}
			if !seen[model.ID] {
				seen[model.ID] = true
				models = append(models, model.ID)
			}
		}
		if config.protocol() != "anthropic" || !list.HasMore {
			return models, nil
		}
		if !validModelID(list.LastID) || cursors[list.LastID] || (key != "" && strings.Contains(list.LastID, key)) {
			return nil, errors.New("AI 服务返回了无效的模型列表分页信息")
		}
		cursors[list.LastID] = true
		path = "/models?limit=100&after_id=" + url.QueryEscape(list.LastID)
	}
	return nil, errors.New("AI 服务返回的模型列表页数过多")
}
