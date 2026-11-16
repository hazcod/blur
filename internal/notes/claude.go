package notes

import "github.com/hazcod/blur/internal/model"

// NewClaudeExtractor reuses the bounded sanitized-note and strict unconfirmed
// proposal validation. The caller owns explicit consent for remote note use.
func NewClaudeExtractor(modelName, apiKey, workspaceID string) (*AppleExtractor, error) {
	client, err := model.NewAnthropic(modelName, apiKey, workspaceID)
	if err != nil {
		return nil, err
	}
	return &AppleExtractor{client: client}, nil
}
