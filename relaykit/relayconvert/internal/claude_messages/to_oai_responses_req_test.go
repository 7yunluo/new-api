package claudemessages

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeMessagesRequestToOpenAIResponsesKeepsThinkingAsReasoningItem(t *testing.T) {
	got, err := ClaudeMessagesRequestToOpenAIResponses(context.Background(), dto.ClaudeRequest{
		Model:     "claude-test",
		MaxTokens: lo.ToPtr(uint(64)),
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "thinking", Thinking: lo.ToPtr("first thought ")},
				{Type: "thinking", Thinking: lo.ToPtr("second thought")},
				{Type: "text", Text: lo.ToPtr("hello")},
			}},
			{Role: "user", Content: "again"},
		},
	}, &convmeta.Values{})
	require.NoError(t, err)

	var items []map[string]any
	require.NoError(t, json.Unmarshal(got.Input, &items))

	reasoningIndex := -1
	var reasoningText string
	for i, item := range items {
		if item["type"] == "reasoning" {
			reasoningIndex = i
			summary, ok := item["summary"].([]any)
			require.True(t, ok, "reasoning item must carry a summary array: %v", item)
			require.Len(t, summary, 1)
			part, ok := summary[0].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "summary_text", part["type"])
			reasoningText, _ = part["text"].(string)
		}
	}
	require.NotEqual(t, -1, reasoningIndex, "expected a reasoning input item, got: %s", string(got.Input))
	assert.Equal(t, "first thought second thought", reasoningText)

	assistantIndex := -1
	for i, item := range items {
		if item["role"] == "assistant" {
			assistantIndex = i
		}
	}
	require.NotEqual(t, -1, assistantIndex, "expected an assistant message item, got: %s", string(got.Input))
	assert.Less(t, reasoningIndex, assistantIndex, "reasoning item must precede the assistant message")
}

func TestClaudeMessagesRequestToOpenAIResponsesSkipsEmptyThinking(t *testing.T) {
	got, err := ClaudeMessagesRequestToOpenAIResponses(context.Background(), dto.ClaudeRequest{
		Model:     "claude-test",
		MaxTokens: lo.ToPtr(uint(64)),
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "thinking", Thinking: lo.ToPtr("")},
				{Type: "text", Text: lo.ToPtr("hello")},
			}},
		},
	}, &convmeta.Values{})
	require.NoError(t, err)

	var items []map[string]any
	require.NoError(t, json.Unmarshal(got.Input, &items))
	for _, item := range items {
		assert.NotEqual(t, "reasoning", item["type"], "empty thinking must not emit a reasoning item: %s", string(got.Input))
	}
}

func TestClaudeMessagesRequestToOpenAIResponsesKeepsRedactedThinkingPlaceholder(t *testing.T) {
	got, err := ClaudeMessagesRequestToOpenAIResponses(context.Background(), dto.ClaudeRequest{
		Model:     "claude-test",
		MaxTokens: lo.ToPtr(uint(64)),
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "redacted_thinking", Data: "encrypted-blob"},
				{Type: "text", Text: lo.ToPtr("hello")},
			}},
		},
	}, &convmeta.Values{})
	require.NoError(t, err)

	var items []map[string]any
	require.NoError(t, json.Unmarshal(got.Input, &items))

	var reasoningText string
	found := false
	for _, item := range items {
		if item["type"] == "reasoning" {
			found = true
			summary, ok := item["summary"].([]any)
			require.True(t, ok, "reasoning item must carry a summary array: %v", item)
			require.Len(t, summary, 1)
			part, ok := summary[0].(map[string]any)
			require.True(t, ok)
			reasoningText, _ = part["text"].(string)
		}
	}
	require.True(t, found, "expected a reasoning input item, got: %s", string(got.Input))
	assert.Equal(t, "[redacted thinking]", reasoningText)
}
