package client

import (
	"encoding/json"
	"testing"
)

func TestClaudeConfigurationUsesActualExecutableWithoutSecrets(t *testing.T) {
	path := "/Users/example/Downloads/Blur.app/Contents/MacOS/blur"
	config, err := claudeMCPConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Servers map[string]struct {
			Command string
			Args    []string
		} `json:"mcpServers"`
	}
	if json.Unmarshal([]byte(config), &got) != nil || len(got.Servers) != 1 || got.Servers["blur"].Command != path || len(got.Servers["blur"].Args) != 1 || got.Servers["blur"].Args[0] != "mcp" {
		t.Fatal("wrong installed MCP entry point")
	}
	if _, err := claudeMCPConfig("relative/blur"); err == nil {
		t.Fatal("accepted unusable relative path")
	}
}
