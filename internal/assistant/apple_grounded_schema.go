package assistant

import (
	"encoding/json"

	"github.com/hazcod/blur/internal/model"
)

func appleGroundedSchema() (json.RawMessage, error) {
	if !json.Valid([]byte(appleAnswerSchema)) {
		return nil, ErrContext
	}
	return json.RawMessage(append([]byte(nil), appleAnswerSchema...)), nil
}

func (g *OllamaGenerator) memoryRequest(system, user string) (model.Request, error) {
	request := model.Request{System: system, User: user, Format: "json"}
	if len(g.schema) == 0 {
		return request, nil
	}
	if len(user) == 0 {
		return model.Request{}, ErrContext
	}
	var err error
	request.Schema, err = appleGroundedSchema()
	if err != nil {
		return model.Request{}, err
	}
	request.Format = ""
	return request, nil
}
