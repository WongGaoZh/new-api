package comate

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// comateStreamPayload is one "data:" line of the Comate execute SSE.
// v2/execute streams increments in content.detail.delta; the legacy
// chat/stream protocol put the text in content.detail.summary.
type comateStreamPayload struct {
	Content struct {
		Type   string `json:"type"` // ANSWER | EXCEPTION
		Text   string `json:"text"`
		Detail struct {
			Delta          string `json:"delta"`
			Summary        string `json:"summary"`
			ExceptionMsg   string `json:"exceptionMsg"`
			ConversationId string `json:"conversationId"`
			MessageId      string `json:"messageId"`
			End            bool   `json:"end"`
		} `json:"detail"`
		End bool `json:"end"`
	} `json:"content"`
}

// comateHandler buffers the full Comate SSE answer and returns a single
// OpenAI chat completion JSON for non-streaming clients.
func comateHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (usage any, err *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)
	answer, streamErr := readComateSSE(resp.Body)
	if streamErr != nil {
		return nil, types.NewErrorWithStatusCode(streamErr, types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	query := requestQuery(info)
	completion := &dto.OpenAITextResponse{
		Id:      fmt.Sprintf("chatcmpl-comate-%d", time.Now().UnixNano()),
		Model:   responseModelName(info),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Choices: []dto.OpenAITextResponseChoice{{
			Index:        0,
			Message:      dto.Message{Role: "assistant", Content: answer},
			FinishReason: "stop",
		}},
		Usage: estimateUsage(query, answer),
	}
	jsonResponse, marshalErr := common.Marshal(completion)
	if marshalErr != nil {
		return nil, types.NewError(marshalErr, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(jsonResponse)
	return &completion.Usage, nil
}

// comateStreamHandler forwards Comate SSE increments as OpenAI chunks and
// finishes with a stop chunk and the [DONE] marker.
func comateStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (usage any, err *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)
	helper.SetEventStreamHeaders(c)
	answer, streamErr := streamComateSSE(c, info, resp.Body)
	if streamErr != nil {
		return nil, types.NewErrorWithStatusCode(streamErr, types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	estimated := estimateUsage(requestQuery(info), answer)
	return &estimated, nil
}

// readComateSSE consumes the whole body and returns the final answer text.
func readComateSSE(body io.Reader) (string, error) {
	scanner := newComateScanner(body)
	var answer strings.Builder
	for scanner.Scan() {
		payload, ok, err := parseComateLine(scanner.Bytes())
		if err != nil {
			return answer.String(), err
		}
		if !ok {
			continue
		}
		if payload.Content.Type == "EXCEPTION" {
			return answer.String(), errors.New(comateExceptionMessage(payload))
		}
		appendPayloadText(&answer, payload)
		if payloadIsEnd(payload) {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return answer.String(), fmt.Errorf("read Comate stream failed: %w", err)
	}
	return answer.String(), nil
}

// streamComateSSE forwards each text increment as an OpenAI chunk.
func streamComateSSE(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (string, error) {
	scanner := newComateScanner(body)
	var answer strings.Builder
	finished := false
	for scanner.Scan() {
		payload, ok, err := parseComateLine(scanner.Bytes())
		if err != nil {
			return answer.String(), err
		}
		if !ok {
			continue
		}
		if payload.Content.Type == "EXCEPTION" {
			return answer.String(), errors.New(comateExceptionMessage(payload))
		}
		delta := payloadText(payload)
		if delta != "" {
			answer.WriteString(delta)
			if err := helper.ObjectData(c, buildStreamChunk(info, delta, nil)); err != nil {
				return answer.String(), err
			}
		}
		if payloadIsEnd(payload) {
			finished = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return answer.String(), fmt.Errorf("read Comate stream failed: %w", err)
	}
	stop := constant.FinishReasonStop
	if err := helper.ObjectData(c, buildStreamChunk(info, "", &stop)); err != nil {
		return answer.String(), err
	}
	if err := helper.StringData(c, "[DONE]"); err != nil {
		return answer.String(), err
	}
	if !finished {
		// Upstream closed without an explicit end frame: keep the answer that
		// was streamed instead of failing the request.
		return answer.String(), nil
	}
	return answer.String(), nil
}

// parseComateLine parses one "data: {...}" line. ok=false means the line is
// not a data event (blank lines, comments, heartbeats, [DONE]).
func parseComateLine(line []byte) (payload comateStreamPayload, ok bool, err error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, []byte("data:")) {
		return payload, false, nil
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return payload, false, nil
	}
	if err := common.Unmarshal(data, &payload); err != nil {
		return payload, false, fmt.Errorf("parse Comate data line failed: %w", err)
	}
	return payload, true, nil
}

// payloadText returns the incremental text carried by one frame.
func payloadText(payload comateStreamPayload) string {
	if delta := payload.Content.Detail.Delta; delta != "" {
		return delta
	}
	return payload.Content.Detail.Summary
}

// appendPayloadText adds a frame's text to the buffer. Delta frames are
// incremental; summary frames may repeat the full answer and need dedupe.
func appendPayloadText(answer *strings.Builder, payload comateStreamPayload) {
	delta := payload.Content.Detail.Delta
	if delta != "" {
		answer.WriteString(delta)
		return
	}
	summary := payload.Content.Detail.Summary
	if summary == "" {
		return
	}
	current := answer.String()
	if len(summary) <= len(current) {
		return
	}
	if strings.HasPrefix(summary, current) {
		answer.WriteString(summary[len(current):])
		return
	}
	answer.Reset()
	answer.WriteString(summary)
}

func payloadIsEnd(payload comateStreamPayload) bool {
	return payload.Content.Detail.End || payload.Content.End
}

func buildStreamChunk(info *relaycommon.RelayInfo, delta string, finishReason *string) *dto.ChatCompletionsStreamResponse {
	choice := dto.ChatCompletionsStreamResponseChoice{Index: 0, FinishReason: finishReason}
	choice.Delta.SetContentString(delta)
	return &dto.ChatCompletionsStreamResponse{
		Id:      fmt.Sprintf("chatcmpl-comate-%d", time.Now().UnixNano()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   responseModelName(info),
		Choices: []dto.ChatCompletionsStreamResponseChoice{choice},
	}
}

// responseModelName returns the model name to echo back to the client.
func responseModelName(info *relaycommon.RelayInfo) string {
	if info != nil && info.OriginModelName != "" {
		return info.OriginModelName
	}
	return "comate"
}

func comateExceptionMessage(payload comateStreamPayload) string {
	if msg := strings.TrimSpace(payload.Content.Detail.ExceptionMsg); msg != "" {
		return msg
	}
	if msg := strings.TrimSpace(payload.Content.Text); msg != "" {
		return msg
	}
	if msg := strings.TrimSpace(payload.Content.Detail.Summary); msg != "" {
		return msg
	}
	return "Comate returned an unknown exception"
}

func requestQuery(info *relaycommon.RelayInfo) string {
	req, ok := info.Request.(*dto.GeneralOpenAIRequest)
	if !ok || req == nil {
		return ""
	}
	return extractUserText(req.Messages)
}

func newComateScanner(body io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return scanner
}
