package main

func mcpLaunchAllowed(mcpOnly bool, args []string) bool {
	if !mcpOnly {
		return true
	}
	if len(args) != 2 {
		return false
	}
	return args[1] == "mcp"
}
