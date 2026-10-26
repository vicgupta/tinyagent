// Package embed holds files compiled into the tinyagent binary.
package embed

import stdembed "embed"

//go:embed prompts/*
var Prompts stdembed.FS

// SystemPrompt returns the default system prompt.
func SystemPrompt() string {
	b, err := Prompts.ReadFile("prompts/system.txt")
	if err != nil {
		panic(err) // embedded at build time; cannot fail
	}
	return string(b)
}

// ReviewPrompt returns the fixed review instructions that open the first
// user message of a review run (--review, --review-base, --review-commit).
func ReviewPrompt() string {
	b, err := Prompts.ReadFile("prompts/review.txt")
	if err != nil {
		panic(err) // embedded at build time; cannot fail
	}
	return string(b)
}
