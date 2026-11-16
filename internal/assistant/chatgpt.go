package assistant

import (
	"github.com/hazcod/blur/internal/chatgpt"
	"github.com/hazcod/blur/internal/model"
)

// NewChatGPTGenerator is remote. Routing and independent permission to send
// saved evidence are enforced by the client, before this adapter is called.
func NewChatGPTGenerator(accounts *chatgpt.Manager, accountID, name string) (*OllamaGenerator, error) {
	completion, err := model.NewChatGPT(accounts, accountID, name, model.ChatGPTOptions{AllowClientOutputLimit: true})
	if err != nil {
		return nil, err
	}
	return &OllamaGenerator{client: completion}, nil
}
