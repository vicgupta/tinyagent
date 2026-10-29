package trajectory

// PluginInfo records one configured plugin that loaded, for trajectories
// and run.started. Digests are hex SHA-256 of the files as read; mcp.json
// env values are never recorded.
type PluginInfo struct {
	Name           string             `json:"name"`
	Version        string             `json:"version,omitempty"`
	Path           string             `json:"path"`
	ManifestSHA256 string             `json:"plugin_json_sha256"`
	MCPSHA256      string             `json:"mcp_json_sha256,omitempty"`
	Skills         []string           `json:"skills,omitempty"`
	MCPServers     []PluginServerInfo `json:"mcp_servers,omitempty"`
}

// PluginServerInfo is one authorized plugin MCP server.
type PluginServerInfo struct {
	Server string `json:"server"`
	Alias  string `json:"alias"`
}
