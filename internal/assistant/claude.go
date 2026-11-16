package assistant

import (
	"encoding/json"

	"github.com/hazcod/blur/internal/model"
)

// NewClaudeGenerator keeps the grounded answer, citation, general answer, and
// prompt admission checks shared by the other completion adapters. Callers
// must require explicit consent before sending context to the cloud API.
func NewClaudeGenerator(modelName, apiKey, workspaceID string) (*OllamaGenerator, error) {
	client, err := model.NewAnthropic(modelName, apiKey, workspaceID)
	if err != nil {
		return nil, err
	}
	return &OllamaGenerator{client: client, schema: json.RawMessage(appleAnswerSchema)}, nil
}
