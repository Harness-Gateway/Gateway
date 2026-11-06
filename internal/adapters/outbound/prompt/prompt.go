// Package prompt adapts stateless chat history to the CLIs' turn-based inputs.
// History is supplied as explicitly labelled data, not imported native turns.
package prompt

import (
	"encoding/json"
	"strings"

	"github.com/harness-gateway/harness-gateway/internal/domain"
)

func Build(messages []domain.Message) (instructions, input string) {
	type entry struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var system []string
	var history []entry
	for _, message := range messages {
		if message.Role == "system" || message.Role == "developer" {
			system = append(system, message.Text)
		} else {
			history = append(history, entry{message.Role, message.Text})
		}
	}
	instructions = strings.Join(system, "\n\n")
	if len(history) == 1 {
		return instructions, history[0].Content
	}
	// Only strings are marshalled, so encoding cannot fail.
	data, _ := json.Marshal(history)
	return instructions, "Continue the conversation below, answering the final user message. The JSON array contains conversation data with explicit author roles; assistant entries are previous replies.\n\n" + string(data)
}
