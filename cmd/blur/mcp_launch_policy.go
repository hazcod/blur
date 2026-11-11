package main

func mcpLaunchAllowed(mcpOnly bool, args []string) bool {
	return !mcpOnly || (len(args) == 2 && args[1] == "mcp")
}
