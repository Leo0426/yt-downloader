package main

import (
	"context"
	"errors"
	"strings"

	"yt-downloader/internal/assist"
)

// An explicitly requested attempt keeps its provider and selected strategy across restarts.
type assistedRetry struct {
	Config   assist.Config `json:"config"`
	Strategy string        `json:"strategy,omitempty"`
}

// A draft can query its catalogue before either its model or its key is saved.
func (a *application) listModels(ctx context.Context, draft assist.Config, draftKey *string) ([]string, error) {
	var key string
	if draftKey != nil {
		key = *draftKey
	} else {
		a.mu.Lock()
		if draft.SameService(a.Preferences.AI) {
			key = a.aiKey
		}
		a.mu.Unlock()
	}
	return assist.ListModels(ctx, draft, key)
}

func strategyLabel(strategy string) string {
	switch strategy {
	case "network":
		return "延长超时并退避重试"
	case "combined":
		return "优先下载音画合一的媒体流"
	case "ipv4":
		return "使用 IPv4 连接"
	}
	return ""
}

func (a *application) planRetry(ctx context.Context, j job) (string, error) {
	if strategy := j.AIRetry.Strategy; strategy != "" {
		if strategyLabel(strategy) == "" {
			return "", errors.New("保存的 AI 下载策略无效，请重新启动辅助下载")
		}
		return strategy, nil
	}
	a.mu.Lock()
	config, key := a.Preferences.AI, a.aiKey
	a.mu.Unlock()
	if !config.SameService(j.AIRetry.Config) || config.Model != j.AIRetry.Config.Model {
		return "", errors.New("排队期间 AI 服务设置已更改，请用新设置重新启动辅助下载")
	}
	if j.Failure == nil {
		return "", errors.New("找不到这项任务的失败记录，请先重试下载")
	}
	decision, err := assist.Plan(ctx, config, key, assist.Input{
		Summary: j.Failure.Summary, Signals: j.Failure.Signals,
		YouTube: j.Failure.YouTubeID != "" || strings.HasPrefix(j.URL, "https://www.youtube.com/watch?v="),
		Format:  j.Options.Format(), TimeoutSeconds: j.Options.TimeoutSeconds, Retries: j.Options.Retries,
	})
	if err != nil {
		return "", err
	}
	if decision.Action == "stop" {
		return "", errors.New("建议先手动处理。" + decision.Advice)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	current := a.find(j.ID)
	previous := *current
	current.AIRetry = &assistedRetry{Config: config, Strategy: decision.Action}
	current.AINote = strategyLabel(decision.Action) + "。" + decision.Advice
	if err := a.save(); err != nil {
		*current = previous
		return "", errors.New("无法保存 AI 下载策略，请检查数据目录后重试")
	}
	return decision.Action, nil
}
