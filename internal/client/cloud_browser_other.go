//go:build !darwin

package client

import "errors"

func openChatGPTAuthorization(string) error {
	return errors.New("client: native account browser unavailable")
}
func openChatGPTUsage() error { return errors.New("client: native account browser unavailable") }
