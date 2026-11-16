package client

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func claudeMCPConfig(executable string) (string, error) {
	if !filepath.IsAbs(executable) {
		return "", ErrInvalidRequest
	}
	config := struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}{}
	config.Servers = map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}{"blur": {Command: executable, Args: []string{"mcp"}}}
	data, err := json.MarshalIndent(config, "", "  ")
	return string(data), err
}

func (d *desktop) copyClaudeSetup() {
	executable, err := os.Executable()
	if err == nil {
		var config string
		config, err = claudeMCPConfig(executable)
		if err == nil {
			err = copyToPasteboard(config)
		}
	}
	if err != nil {
		d.mcpStatus = "The connection configuration could not be copied. Try again."
	} else {
		d.mcpStatus = "Configuration copied. Add it in Claude’s MCP settings, and keep Blur running."
	}
	d.app.RequestUpdate()
}
