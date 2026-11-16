package client

import "github.com/hazcod/blur/internal/platform/darwin"

func openChatGPTAuthorization(raw string) error { return darwin.OpenChatGPTAuthorization(raw) }
func openChatGPTUsage() error                   { return darwin.OpenChatGPTUsage() }
