package profile

import "testing"

func TestExplicitProfileInputPreservesUserWordsAndRecallQuestions(t *testing.T) {
	for _, test := range []struct {
		input string
		kind  Kind
		text  string
		ok    bool
	}{
		{"remember that I prefer Dutch.", Fact, "I prefer Dutch.", true},
		{"Remember: My editor is Zed", Fact, "My editor is Zed", true},
		{"preference: Use tabs", Fact, "Use tabs", true},
		{"writing rule: Keep replies concise.", WritingRule, "Keep replies concise.", true},
		{"rule: Use a formal tone", WritingRule, "Use a formal tone", true},
		{"remember that article I read?", "", "", false},
		{"Do you remember that I prefer Dutch?", "", "", false},
		{"remember that compiler article", "", "", false},
		{"remember that article I was reading", "", "", false},
		{"What writing rules do I have?", "", "", false},
	} {
		kind, text, ok := ParseExplicit(test.input)
		if kind != test.kind || text != test.text || ok != test.ok {
			t.Fatalf("%q => %q %q %t", test.input, kind, text, ok)
		}
	}
}
