package model

import (
	"net"
	"net/url"
	"os"
	"strings"
)

// NewOllama returns a client for a local Ollama server's OpenAI-compatible endpoint.
func NewOllama(model string) *OpenAI {
	return &OpenAI{BaseURL: ollamaBaseURL(os.Getenv("OLLAMA_HOST")) + "/v1", Model: model}
}

// ollamaBaseURL interprets OLLAMA_HOST the way the Ollama CLI does: the scheme
// and port are optional (default http and 11434), and a bind-all address such
// as 0.0.0.0, which is commonly set for the server, means connect to localhost.
func ollamaBaseURL(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "http://127.0.0.1:11434"
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	u, err := url.Parse(host)
	if err != nil {
		return "http://127.0.0.1:11434"
	}
	hostname, port := u.Hostname(), u.Port()
	if hostname == "" || hostname == "0.0.0.0" || hostname == "::" {
		hostname = "127.0.0.1"
	}
	if port == "" {
		port = "11434"
	}
	u.Host = net.JoinHostPort(hostname, port)
	return strings.TrimRight(u.String(), "/")
}

// OllamaBaseURL returns the Ollama server URL that OLLAMA_HOST resolves to.
func OllamaBaseURL() string {
	return ollamaBaseURL(os.Getenv("OLLAMA_HOST"))
}
