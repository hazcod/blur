package client

import (
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/notes"
)

func (c *localAIController) configuredAskModel(settings LocalAISettings, credential string) (configuredAskModel, error) {
	switch effectiveProvider(settings) {
	case ProviderChatGPT:
		if c.chatGPT == nil {
			return nil, errLocalAIUnavailable
		}
		return assistant.NewChatGPTGenerator(c.chatGPT, settings.AccountID, settings.Model)
	case ProviderClaude:
		return assistant.NewClaudeGenerator(settings.Model, credential, settings.WorkspaceID)
	default:
		return newConfiguredAskModel(settings, credential)
	}
}

func (c *localAIController) configuredExtractor(settings LocalAISettings, credential string) (notes.Extractor, error) {
	if (settings.Provider == ProviderChatGPT || settings.Provider == ProviderClaude) && !settings.RemoteNotesEnabled {
		return nil, nil
	}
	switch effectiveProvider(settings) {
	case ProviderChatGPT:
		if c.chatGPT == nil {
			return nil, errLocalAIUnavailable
		}
		return notes.NewChatGPTExtractor(c.chatGPT, settings.AccountID, settings.Model)
	case ProviderClaude:
		return notes.NewClaudeExtractor(settings.Model, credential, settings.WorkspaceID)
	default:
		return newConfiguredExtractor(settings, credential)
	}
}

func cloudAccountProvider(provider string) bool {
	return provider == ProviderChatGPT || provider == ProviderClaude
}
