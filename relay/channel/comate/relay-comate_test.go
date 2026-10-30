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
		delta     string
		summary   string
		exception string
		end       bool
	}{
		{
			name:    "delta frame",
			line:    `data: {"content":{"type":"ANSWER","detail":{"delta":"pong","end":false},"end":false}}`,
			ok:      true,
			msgType: "ANSWER",
			delta:   "pong",
		},
		{
			name:    "end frame",
			line:    `data: {"content":{"type":"ANSWER","detail":{"delta":"","end":true},"end":false}}`,
			ok:      true,
			msgType: "ANSWER",
			end:     true,
		},
		{
			name:    "legacy summary frame",
			line:    `data: {"content":{"type":"ANSWER","detail":{"summary":"hello"},"end":true}}`,
			ok:      true,
			msgType: "ANSWER",
			summary: "hello",
			end:     true,
		},
		{
			name:      "exception frame",
			line:      `data: {"content":{"type":"EXCEPTION","text":"{\"exceptionMsg\":\"参数toolUseResults不能为空\"}","detail":{"exceptionMsg":"参数toolUseResults不能为空"}}}`,
			ok:        true,
			msgType:   "EXCEPTION",
			exception: "参数toolUseResults不能为空",
		},
		{
			name: "heartbeat is skipped",
			line: ":heartbeat",
			ok:   false,
		},
		{
			name: "blank line is skipped",
			line: "",
			ok:   false,
		},
		{
			name: "DONE marker is skipped",
			line: "data: [DONE]",
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
				assert.Equal(t, tt.delta, payload.Content.Detail.Delta)
				assert.Equal(t, tt.summary, payload.Content.Detail.Summary)
				assert.Equal(t, tt.exception, payload.Content.Detail.ExceptionMsg)
				assert.Equal(t, tt.end, payloadIsEnd(payload))
			}
		})
	}
}

func TestReadComateSSE(t *testing.T) {
	stream := strings.Join([]string{
		":heartbeat",
		`data: {"content":{"type":"ANSWER","detail":{"delta":"我是","end":false}}}`,
		`data: {"content":{"type":"ANSWER","detail":{"delta":" Comate","end":false}}}`,
		`data: {"content":{"type":"ANSWER","detail":{"delta":"","end":true}}}`,
	}, "\n")
	answer, err := readComateSSE(strings.NewReader(stream))
	require.NoError(t, err)
	assert.Equal(t, "我是 Comate", answer)
}

func TestReadComateSSEException(t *testing.T) {
	stream := `data: {"content":{"type":"EXCEPTION","text":"{\"exceptionMsg\":\"参数toolUseResults不能为空\"}","detail":{"exceptionMsg":"参数toolUseResults不能为空"}}}`
	_, err := readComateSSE(strings.NewReader(stream))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "toolUseResults")
}

func TestAppendPayloadText(t *testing.T) {
	var empty strings.Builder
	appendPayloadText(&empty, comateStreamPayload{})
	assert.Equal(t, "", empty.String())

	// Legacy summary frames repeat the full answer and need dedupe.
	var legacy strings.Builder
	legacy.WriteString("hello")
	appendPayloadText(&legacy, payloadWithSummary("hello world"))
	assert.Equal(t, "hello world", legacy.String())

	// Delta frames are appended as-is.
	var delta strings.Builder
	delta.WriteString("he")
	appendPayloadText(&delta, payloadWithDelta("llo"))
	assert.Equal(t, "hello", delta.String())
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

func payloadWithSummary(s string) comateStreamPayload {
	var p comateStreamPayload
	p.Content.Type = "ANSWER"
	p.Content.Detail.Summary = s
	return p
}

func payloadWithDelta(s string) comateStreamPayload {
	var p comateStreamPayload
	p.Content.Type = "ANSWER"
	p.Content.Detail.Delta = s
	return p
}
