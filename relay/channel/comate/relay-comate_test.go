package comate

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseComateLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		ok        bool
		msgType   string
		summary   string
		exception string
	}{
		{
			name:    "ANSWER with end flag",
			line:    `data: {"content":{"type":"ANSWER","detail":{"summary":"hello"},"end":true}}`,
			ok:      true,
			msgType: "ANSWER",
			summary: "hello",
		},
		{
			name:      "EXCEPTION with message",
			line:      `data: {"content":{"type":"EXCEPTION","detail":{"exceptionMsg":"plugin version too low"},"end":true}}`,
			ok:        true,
			msgType:   "EXCEPTION",
			exception: "plugin version too low",
		},
		{
			name: "blank line is skipped",
			line: "",
			ok:   false,
		},
		{
			name: "DONE marker is skipped",
			line: "[DONE]",
			ok:   false,
		},
		{
			name: "comment line is skipped",
			line: ": keepalive",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, ok, err := parseComateLine([]byte(tt.line))
			require.NoError(t, err)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.msgType, payload.Content.Type)
				assert.Equal(t, tt.summary, payload.Content.Detail.Summary)
				assert.Equal(t, tt.exception, payload.Content.Detail.ExceptionMsg)
			}
		})
	}
}

func TestReadComateSSE(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"content":{"type":"ANSWER","detail":{"summary":"I am a"},"end":false}}`,
		`data: {"content":{"type":"ANSWER","detail":{"summary":"I am a helpful assistant"},"end":true}}`,
		``,
	}, "\n")
	answer, _, err := readComateSSE(strings.NewReader(stream))
	require.NoError(t, err)
	assert.Equal(t, "I am a helpful assistant", answer)
}

func TestReadComateSSEException(t *testing.T) {
	stream := `data: {"content":{"type":"EXCEPTION","detail":{"exceptionMsg":"upgrade your plugin"},"end":true}}`
	_, _, err := readComateSSE(strings.NewReader(stream))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upgrade your plugin")
}

func TestAppendAnswerDelta(t *testing.T) {
	var emitted strings.Builder
	assert.Equal(t, "", appendAnswerDelta(&emitted, ""))
	assert.Equal(t, "hello", appendAnswerDelta(&emitted, "hello"))
	// Prefix extension returns only the new part.
	assert.Equal(t, " world", appendAnswerDelta(&emitted, "hello world"))
	// Non-prefix summary replaces the accumulated text.
	assert.Equal(t, "completely different", appendAnswerDelta(&emitted, "completely different"))
}

func TestExtractUserText(t *testing.T) {
	tests := []struct {
		name string
		msgs []dto.Message
		want string
	}{
		{
			name: "last user message wins",
			msgs: []dto.Message{
				{Role: "system", Content: "be brief"},
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "ok"},
				{Role: "user", Content: "second"},
			},
			want: "second",
		},
		{
			name: "multi-part text content",
			msgs: []dto.Message{
				{Role: "user", Content: []any{map[string]any{"type": "text", "text": "part1"}, map[string]any{"type": "text", "text": "part2"}}},
			},
			want: "part1\npart2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractUserText(tt.msgs))
		})
	}
}
