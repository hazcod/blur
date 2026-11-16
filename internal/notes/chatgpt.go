package notes

import (
	"github.com/hazcod/blur/internal/chatgpt"
	"github.com/hazcod/blur/internal/model"
)

// NewChatGPTExtractor reuses the bounded proposal schema and validation. The
// caller must separately admit sending user-authored notes to this account.
func NewChatGPTExtractor(accounts *chatgpt.Manager, accountID, name string) (*AppleExtractor, error) {
	completion, err := model.NewChatGPT(accounts, accountID, name, model.ChatGPTOptions{AllowClientOutputLimit: true})
	if err != nil {
		return nil, err
	}
	return &AppleExtractor{client: completion}, nil
}
