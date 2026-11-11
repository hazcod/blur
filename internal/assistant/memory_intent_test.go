package assistant

import "testing"

func TestQuestionSeeksPersonalMemory(t *testing.T) {
	tests := []struct {
		question string
		want     bool
	}{
		{"What was the launch decision?", true},
		{"Summarize the meeting with the vendor", true},
		{"what did 2+2?", true},
		{"Anything from yesterday?", true},
		{"Show recent emails.", true},
		{"What is on this morning?", true},
		{"What did we decide?", true},
		{"what did you capture?", true},
		{"recap please", true},
		{"summary of the week", true},
		{"what have you seen", true},
		{"what are they doing", true},
		{"how do you pronounce it", false},
		{"What is 2+2?", false},
		{"Explain TLS 1.3", false},
		{"Write a haiku about rain", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := QuestionSeeksPersonalMemory(tt.question); got != tt.want {
			t.Errorf("QuestionSeeksPersonalMemory(%q) = %v, want %v", tt.question, got, tt.want)
		}
	}
}
