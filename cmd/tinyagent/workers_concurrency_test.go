package main

import (
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func TestNewWorkersPassesMaxConcurrent(t *testing.T) {
	cfg := config.Default()
	setWorkers(&cfg, true)
	cfg.Subagents.Workers.MaxConcurrent = 3
	w, err := newWorkers(cfg, "openai/gpt-5-mini", "")
	if err != nil || w == nil || w.MaxConcurrent != 3 {
		t.Errorf("newWorkers = %+v, %v", w, err)
	}
}
