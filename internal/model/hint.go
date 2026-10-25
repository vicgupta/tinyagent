package model

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// keyEnv is the API key variable for each provider that uses one.
var keyEnv = map[string]string{
	"openai":     "OPENAI_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
	"anthropic":  "ANTHROPIC_API_KEY",
}

var httpStatusRe = regexp.MustCompile(`HTTP (\d{3}):`)

// Hint returns a one-line setup fix for a failed run of the model spec, given
// the error text, or "" for errors that aren't a common setup problem. It
// names environment variables but never prints their values.
func Hint(spec, errMsg string) string {
	provider, name, ok := strings.Cut(spec, "/")
	if !ok {
		provider, name = "openai", spec
	}
	status := 0
	if m := httpStatusRe.FindStringSubmatch(errMsg); m != nil {
		status, _ = strconv.Atoi(m[1])
	}
	switch provider {
	case "openai", "openrouter", "anthropic":
		env := keyEnv[provider]
		switch {
		case (status == 401 || status == 403) && os.Getenv(env) == "":
			return fmt.Sprintf("%s isn't set: export %s=... and rerun, e.g. tinyagent -m %s -t \"...\"", env, env, spec)
		case status == 401 || status == 403:
			return fmt.Sprintf("%s was rejected by %s; check that it's current and has access to %s", env, provider, name)
		case status == 404:
			return fmt.Sprintf("%s has no model %q; check the name after %s/", provider, name, provider)
		case provider == "openai" && status == 400 && needsResponsesAPI(errMsg):
			return fmt.Sprintf("%s needs OpenAI's Responses API for this request; set openai_api: responses in tinyagent.yaml (or pass --openai-api responses)", name)
		}
	case "ollama":
		switch {
		case status == 404:
			return fmt.Sprintf("pull the model first: ollama pull %s", name)
		case strings.Contains(errMsg, "connection refused") || strings.Contains(errMsg, "no such host"):
			return fmt.Sprintf("can't reach Ollama at %s; start it with `ollama serve`, or set OLLAMA_HOST to where it runs", ollamaBaseURL(os.Getenv("OLLAMA_HOST")))
		}
	case "codex":
		switch {
		case strings.Contains(errMsg, "not supported"):
			return fmt.Sprintf("your ChatGPT plan doesn't offer %q in Codex; use a model the Codex CLI's model picker shows, e.g. tinyagent -m codex/<model> -t \"...\"", name)
		case strings.Contains(errMsg, "codex login"):
			return "sign in once with the Codex CLI: run `codex login`, choose ChatGPT, then rerun tinyagent"
		}
	}
	return ""
}

// needsResponsesAPI reports whether a Chat Completions error says the request
// needs the Responses API, as OpenAI's reply to tools combined with reasoning
// on newer models does.
func needsResponsesAPI(errMsg string) bool {
	m := strings.ToLower(errMsg)
	if strings.Contains(m, "responses api") || strings.Contains(m, "/v1/responses") {
		return true
	}
	return strings.Contains(m, "reasoning_effort") && (strings.Contains(m, "tool") || strings.Contains(m, "function"))
}
