package comate

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// Adaptor relays OpenAI chat requests to the Baidu Comate public SaaS API.
// Comate has no OpenAI-compatible endpoint: requests are converted to the
// custom SSE chat/stream protocol and answers are converted back to OpenAI SSE.
type Adaptor struct{}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info.RelayMode != relayconstant.RelayModeChatCompletions {
		return "", fmt.Errorf("Comate channel only supports chat completions, got relay mode %d", info.RelayMode)
	}
	if info.ChannelBaseUrl == "" {
		return "", errors.New("Comate channel base URL is empty")
	}
	return buildComateChatURL(info.ChannelBaseUrl), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	// The public SaaS accepts the license as the user identity in these two
	// headers; there is no Authorization header on the public endpoint.
	loginName := encodeLicense(info.ApiKey)
	req.Set("login-name", loginName)
	req.Set("Uuap-login-name", loginName)
	req.Set("X-Source", "COMATE")
	req.Set("plugin-version", "VsCode-4.14.1")
	req.Set("x-skip-fcnap", "yes")
	req.Set("Accept", "text/event-stream")
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	query := extractUserText(request.Messages)
	if query == "" {
		return nil, errNoUserQuery
	}
	return &comateChatRequest{Query: query, ConversationId: "", ChatId: ""}, nil
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	// Channel test additionally probes /api/config/global so a misconfigured
	// environment fails fast with a clear message before a chat request is sent.
	if info.IsChannelTest {
		config, err := ProbeGlobalConfig(c.Request.Context(), info.ChannelBaseUrl)
		if err != nil {
			return nil, err
		}
		if config.ServerHost != "" && config.ServerHost != info.ChannelBaseUrl {
			logger.LogWarn(c.Request.Context(), "comate config/global reports serverHost %s, channel base URL is %s", config.ServerHost, info.ChannelBaseUrl)
		}
	}
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	// The upstream always answers with SSE, so new-api marks the request as
	// streaming based on the response Content-Type. Decide the client-facing
	// format from the client's original stream flag instead: non-streaming
	// callers must receive a single JSON completion.
	if clientRequestedStream(info) {
		return comateStreamHandler(c, info, resp)
	}
	return comateHandler(c, info, resp)
}

// clientRequestedStream reports whether the client asked for a streamed answer.
func clientRequestedStream(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	req, ok := info.Request.(*dto.GeneralOpenAIRequest)
	if !ok || req == nil || req.Stream == nil {
		return info.IsStream
	}
	return *req.Stream
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("not implemented")
}

// comateChatRequest is the body of POST /api/aidevops/autocomate/rest/autowork/v1/chat/stream.
type comateChatRequest struct {
	Query          string `json:"query"`
	ConversationId string `json:"conversationId"`
	ChatId         string `json:"chatId"`
}
