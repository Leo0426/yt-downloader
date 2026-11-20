package main

import (
	"errors"
	"os"
	"path/filepath"

	"yt-downloader/internal/assist"
	"yt-downloader/internal/download"
)

const defaultConcurrency = 2

type preferences struct {
	Concurrency int              `json:"concurrency"`
	Download    download.Options `json:"download"`
	Theme       string           `json:"theme"`
	Glass       bool             `json:"glass"`
	AI          assist.Config    `json:"ai"`
}

func defaultPreferences() preferences {
	return preferences{Concurrency: defaultConcurrency, Download: download.DefaultOptions(), Theme: "system", Glass: true}
}

func defaultOutputDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads"), nil
}

func (a *application) defaultOutputDirectory() (string, error) {
	if a.downloadRoot != "" {
		return a.downloadRoot, nil
	}
	return defaultOutputDirectory()
}

func (p preferences) validate() error {
	if p.Concurrency < 1 || p.Concurrency > 6 {
		return errors.New("同时下载的任务数需要在 1 到 6 之间")
	}
	switch p.Theme {
	case "system", "light", "dark":
	default:
		return errors.New("请选择有效的外观主题")
	}
	if err := p.AI.Validate(); err != nil {
		return err
	}
	return p.Download.Validate()
}

func (a *application) savePreferences(out string, prefs *preferences, key *string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if prefs != nil {
		// Version 1 configuration exports predate the concurrency setting.
		if prefs.Concurrency == 0 {
			prefs.Concurrency = defaultConcurrency
		}
		if err := prefs.validate(); err != nil {
			return err
		}
	}
	if key != nil {
		if prefs == nil || prefs.AI.BaseURL == "" {
			return errors.New("请先填写 AI 服务地址")
		}
		if err := assist.ValidateKey(*key); err != nil {
			return err
		}
	}
	// Changing appearance or network options must not probe a protected or offline directory.
	if out == "" || (a.mediaRoot == nil && out == a.OutDir) {
		out = a.OutDir
	} else {
		var err error
		out, err = a.outputDirectory(out)
		if err != nil {
			return err
		}
	}
	previous := a.state
	a.OutDir = out
	if prefs != nil {
		a.Preferences = *prefs
	}
	if err := a.save(); err != nil {
		a.state = previous
		return err
	}
	if !previous.Preferences.AI.SameService(a.Preferences.AI) {
		a.aiKey = ""
	}
	if key != nil {
		a.aiKey = *key
	}
	a.notify()
	return nil
}

func (a *application) pauseQueue(paused bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	previous := a.Paused
	a.Paused = paused
	if err := a.save(); err != nil {
		a.Paused = previous
		return err
	}
	a.notify()
	return nil
}
