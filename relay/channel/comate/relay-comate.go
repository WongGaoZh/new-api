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

// comateStreamPayload is one "data:" line of the Comate chat/stream SSE.
type comateStreamPayload struct {
	Content struct {
		Type   string `json:"type"` // ANSWER | EXCEPTION
		Detail struct {
			Summary        string `json:"summary"`
			ExceptionMsg   string `json:"exceptionMsg"`
			ConversationId string `json:"conversationId"`
			MessageId      string `json:"messageId"`
		} `json:"detail"`
		End bool `json:"end"`
	} `json:"content"`
}

// comateHandler buffers the full Comate SSE answer and returns a single
// OpenAI chat completion JSON for non-streaming clients.
func comateHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (usage any, err *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)
	answer, _, streamErr := readComateSSE(resp.Body)
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

// comateStreamHandler converts the Comate SSE into OpenAI chat completion
// chunks: incremental text is forwarded as it arrives, the final answer (which
// may only appear on the end line) is emitted before [DONE].
func comateStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (usage any, err *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)
	answer, _, streamErr := readComateSSEStream(c, info, resp.Body)
	if streamErr != nil {
		return nil, types.NewErrorWithStatusCode(streamErr, types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}
	estimated := estimateUsage(requestQuery(info), answer)
	return &estimated, nil
}

// readComateSSE consumes the whole body and returns the final answer text and
// the accumulated streamed text.
func readComateSSE(body io.Reader) (answer string, streamed string, err error) {
	scanner := newComateScanner(body)
	var emitted strings.Builder
	var finalAnswer string
	for scanner.Scan() {
		payload, ok, parseErr := parseComateLine(scanner.Bytes())
		if parseErr != nil {
			return "", emitted.String(), parseErr
		}
		if !ok {
			continue
		}
		if payload.Content.Type == "EXCEPTION" {
			return "", emitted.String(), errors.New(comateExceptionMessage(payload))
		}
		if payload.Content.Type != "ANSWER" {
			continue
		}
		appendAnswerDelta(&emitted, payload.Content.Detail.Summary)
		if payload.Content.End {
			finalAnswer = payload.Content.Detail.Summary
			if finalAnswer == "" {
				finalAnswer = emitted.String()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", emitted.String(), fmt.Errorf("read Comate stream failed: %w", err)
	}
	if finalAnswer == "" {
		finalAnswer = emitted.String()
	}
	return finalAnswer, emitted.String(), nil
}

// readComateSSEStream forwards the Comate SSE to the client as OpenAI chunks.
// It sends the final answer, a stop chunk and the [DONE] marker when the
// upstream completes.
func readComateSSEStream(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (answer string, streamed string, err error) {
	scanner := newComateScanner(body)
	var emitted strings.Builder
	var finalAnswer string
	for scanner.Scan() {
		payload, ok, parseErr := parseComateLine(scanner.Bytes())
		if parseErr != nil {
			return "", emitted.String(), parseErr
		}
		if !ok {
			continue
		}
		if payload.Content.Type == "EXCEPTION" {
			return "", emitted.String(), errors.New(comateExceptionMessage(payload))
		}
		if payload.Content.Type != "ANSWER" {
			continue
		}
		// Forward incremental text while it is a strict extension of what was
		// already sent; the complete answer may arrive only on the end line.
		delta := appendAnswerDelta(&emitted, payload.Content.Detail.Summary)
		if delta != "" {
			chunk := buildStreamChunk(info, delta, nil)
			if err := helper.ObjectData(c, chunk); err != nil {
				return "", emitted.String(), err
			}
		}
		if payload.Content.End {
			finalAnswer = payload.Content.Detail.Summary
			if finalAnswer == "" {
				finalAnswer = emitted.String()
			}
			// Emit any remaining text that only appeared on the end line.
			if remainder := answerRemainder(&emitted, finalAnswer); remainder != "" {
				if err := helper.ObjectData(c, buildStreamChunk(info, remainder, nil)); err != nil {
					return "", emitted.String(), err
				}
			}
			stop := constant.FinishReasonStop
			if err := helper.ObjectData(c, buildStreamChunk(info, "", &stop)); err != nil {
				return "", emitted.String(), err
			}
			if err := helper.StringData(c, "[DONE]"); err != nil {
				return "", emitted.String(), err
			}
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", emitted.String(), fmt.Errorf("read Comate stream failed: %w", err)
	}
	return finalAnswer, emitted.String(), nil
}

// answerRemainder returns the part of the final answer not yet streamed.
func answerRemainder(emitted *strings.Builder, finalAnswer string) string {
	current := emitted.String()
	if finalAnswer == "" || finalAnswer == current {
		return ""
	}
	if len(finalAnswer) > len(current) && strings.HasPrefix(finalAnswer, current) {
		return finalAnswer[len(current):]
	}
	// The final answer replaced the accumulated text: stream it wholesale.
	delta := finalAnswer
	emitted.Reset()
	emitted.WriteString(finalAnswer)
	return delta
}

// parseComateLine parses one "data: {...}" line. ok=false means the line is
// not a data event (blank lines, comments, [DONE], etc.).
func parseComateLine(line []byte) (payload comateStreamPayload, ok bool, err error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || bytes.HasPrefix(trimmed, []byte(":")) {
		return payload, false, nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return payload, false, nil
	}
	data := bytes.TrimPrefix(trimmed, []byte("data:"))
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return payload, false, nil
	}
	if err := common.Unmarshal(data, &payload); err != nil {
		return payload, false, fmt.Errorf("parse Comate data line failed: %w", err)
	}
	return payload, true, nil
}

// appendAnswerDelta grows the emitted answer with the latest summary text and
// returns only the new part (Comate summaries may arrive incrementally or only
// on the final line).
func appendAnswerDelta(emitted *strings.Builder, summary string) string {
	if summary == "" {
		return ""
	}
	current := emitted.String()
	if len(summary) <= len(current) {
		return ""
	}
	if strings.HasPrefix(summary, current) {
		delta := summary[len(current):]
		emitted.WriteString(delta)
		return delta
	}
	// The summary replaced the accumulated text (e.g. first line already full):
	// keep the longer one and emit the whole new value.
	if len(summary) > len(current) {
		delta := summary
		emitted.Reset()
		emitted.WriteString(summary)
		return delta
	}
	return ""
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
