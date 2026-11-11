package main

import "testing"

func TestMCPOnlyExecutableRejectsDesktopLaunch(t *testing.T) {
	if mcpLaunchAllowed(true, []string{"blur-mcp.exe"}) {
		t.Fatal("MCP-only executable accepted a desktop launch")
	}
	if !mcpLaunchAllowed(true, []string{"blur-mcp.exe", "mcp"}) {
		t.Fatal("MCP-only executable rejected its stdio command")
	}
	if !mcpLaunchAllowed(false, []string{"Blur.exe"}) {
		t.Fatal("desktop executable rejected its default launch")
	}
}
