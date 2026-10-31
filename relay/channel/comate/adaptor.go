package comate

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// Adaptor relays OpenAI chat requests to the Baidu Comate public SaaS API.
// Comate has no OpenAI-compatible endpoint: each request creates a
// conversation, creates a task, then executes it and converts the SSE answer
// (content.detail.delta) back to OpenAI chunks or a single JSON completion.
type Adaptor struct{}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info.RelayMode != relayconstant.RelayModeChatCompletions {
		return "", fmt.Errorf("Comate channel only supports chat completions, got relay mode %d", info.RelayMode)
	}
	if info.ChannelBaseUrl == "" {
		return "", errors.New("Comate channel base URL is empty")
	}
	return comateURL(info.ChannelBaseUrl, ExecutePath), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	loginName := encodeLicense(info.ApiKey)
	req.Set("Content-Type", "application/json")
	req.Set("login-name", loginName)
	req.Set("Uuap-login-name", loginName)
	req.Set("X-Source", "COMATE")
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
	return &comateExecuteEnvelope{Query: query}, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	// Channel test probes /api/config/global so a misconfigured environment
	// fails fast with a clear message before the agent protocol runs.
	if info.IsChannelTest {
		config, err := ProbeGlobalConfig(c.Request.Context(), info.ChannelBaseUrl)
		if err != nil {
			return nil, err
		}
		if config.ServerHost != "" && config.ServerHost != info.ChannelBaseUrl {
			logger.LogWarn(c.Request.Context(), "comate config/global reports serverHost %s, channel base URL is %s", config.ServerHost, info.ChannelBaseUrl)
		}
	}

	raw, err := io.ReadAll(requestBody)
	if err != nil {
		return nil, fmt.Errorf("read comate request body failed: %w", err)
	}
	var envelope comateExecuteEnvelope
	if err := common.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("parse comate request body failed: %w", err)
	}
	if strings.TrimSpace(envelope.Query) == "" {
		return nil, errNoUserQuery
	}

	client, err := service.GetHttpClientWithProxySettings(info.ChannelSetting.Proxy, info.ChannelSetting)
	if err != nil {
		return nil, fmt.Errorf("new comate http client failed: %w", err)
	}

	conversationID, err := a.createConversation(c, info, client)
	if err != nil {
		return nil, err
	}
	taskID, err := a.createTask(c, info, client, conversationID)
	if err != nil {
		return nil, err
	}
	return a.execute(c, info, client, conversationID, taskID, envelope.Query)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if clientRequestedStream(info) {
		return comateStreamHandler(c, info, resp)
	}
	return comateHandler(c, info, resp)
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
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

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("not implemented")
}

// clientRequestedStream reports whether the client asked for a streamed answer.
// The upstream always answers with SSE, so info.IsStream alone is not reliable.
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

// ---- Agent protocol stages ----

func (a *Adaptor) createConversation(c *gin.Context, info *relaycommon.RelayInfo, client *http.Client) (int64, error) {
	body := map[string]any{
		"agentId":       ClientAgentID,
		"username":      info.ApiKey,
		"ide":           ClientIDE,
		"ideVersion":    ClientIDEVersion,
		"pluginVersion": ClientPluginVersion,
	}
	resp, err := a.postJSON(c, info, client, ConversationPath, body, false)
	if err != nil {
		return 0, fmt.Errorf("comate create conversation failed: %w", err)
	}
	defer service.CloseResponseBodyGracefully(resp)
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := common.DecodeJson(resp.Body, &parsed); err != nil {
		return 0, fmt.Errorf("parse comate conversation response failed: %w", err)
	}
	if parsed.Code != 200 || parsed.Data.ID == 0 {
		return 0, fmt.Errorf("comate conversation rejected: code=%d message=%s", parsed.Code, parsed.Message)
	}
	return parsed.Data.ID, nil
}

func (a *Adaptor) createTask(c *gin.Context, info *relaycommon.RelayInfo, client *http.Client, conversationID int64) (int64, error) {
	body := map[string]any{
		"agentInfo": map[string]any{
			"isProjectAgent":  false,
			"agentName":       "Agent",
			"canInvokeAgents": true,
		},
		"agentId":        ClientAgentID,
		"conversationId": conversationID,
		"signal":         map[string]any{},
		"username":       info.ApiKey,
		"ide":            ClientIDE,
		"ideVersion":     ClientIDEVersion,
		"pluginVersion":  ClientPluginVersion,
	}
	resp, err := a.postJSON(c, info, client, TaskPath, body, false)
	if err != nil {
		return 0, fmt.Errorf("comate create task failed: %w", err)
	}
	defer service.CloseResponseBodyGracefully(resp)
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TaskID int64 `json:"taskId"`
		} `json:"data"`
	}
	if err := common.DecodeJson(resp.Body, &parsed); err != nil {
		return 0, fmt.Errorf("parse comate task response failed: %w", err)
	}
	if parsed.Code != 200 || parsed.Data.TaskID == 0 {
		return 0, fmt.Errorf("comate task rejected: code=%d message=%s", parsed.Code, parsed.Message)
	}
	return parsed.Data.TaskID, nil
}

func (a *Adaptor) execute(c *gin.Context, info *relaycommon.RelayInfo, client *http.Client, conversationID, taskID int64, query string) (*http.Response, error) {
	body := map[string]any{
		"username":       info.ApiKey,
		"ide":            ClientIDE,
		"ideVersion":     ClientIDEVersion,
		"pluginVersion":  ClientPluginVersion,
		"taskId":         taskID,
		"conversationId": conversationID,
		"agentId":        ClientAgentID,
		"agentVersion":   ClientAgentVersion,
		"query":          query,
		"modelKey":       resolveModelKey(info),
		"sendMode":       "normal",
		"toolUseResults": []any{},
		"isUserQuery":    true,
		"isFirstQuery":   true,
		"isMockQuery":    false,
		"localIndex":     false,
		"enableMemory":   false,
		"hasMcp":         false,
		"systemReminder": "",
		"contexts":       []any{},
		"subAgents":      []any{},
		"skillInfos":     []any{},
		"extendUserQueryInfo": map[string]any{
			"commands":  []any{},
			"skills":    []any{},
			"subagents": []any{},
			"rules":     []any{},
		},
		"extend": map[string]any{
			"isMultiWorkspace": false,
			"useWorkflow":      false,
		},
		"agentInfo": map[string]any{
			"agentName":       "Agent",
			"isProjectAgent":  false,
			"canInvokeAgents": true,
			"isCustomAgent":   false,
		},
		"uploadBaseInfo": map[string]any{
			"os":            "macOS",
			"osVersion":     "macOS",
			"extName":       ClientIDE,
			"extVersion":    ClientPluginVersion,
			"ideType":       ClientIDE,
			"ideName":       ClientIDE,
			"ideVersion":    ClientIDEVersion,
			"username":      info.ApiKey,
			"license":       info.ApiKey,
			"pluginVersion": ClientPluginVersion,
			"device":        fmt.Sprintf("new-api-%d", info.ChannelId),
			"triggerSource": "Agent",
		},
		"sysInfo": map[string]any{
			"os":                   "macOS",
			"defaultShell":         "/bin/sh",
			"homeDir":              "/tmp",
			"installedCommands":    []any{},
			"notInstalledCommands": []any{},
			"workspacePath":        "/tmp",
			"workspaceRoots":       []any{"/tmp"},
		},
	}
	return a.postJSON(c, info, client, ExecutePath, body, true)
}

// postJSON sends a JSON request to Comate and returns the raw response.
// The caller owns the response body.
func (a *Adaptor) postJSON(c *gin.Context, info *relaycommon.RelayInfo, client *http.Client, path string, body any, stream bool) (*http.Response, error) {
	payload, err := common.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal comate request failed: %w", err)
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, comateURL(info.ChannelBaseUrl, path), strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("build comate request failed: %w", err)
	}
	header := req.Header
	if err := a.SetupRequestHeader(c, &header, info); err != nil {
		return nil, err
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do comate request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		service.CloseResponseBodyGracefully(resp)
		return nil, fmt.Errorf("comate %s returned status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return resp, nil
}

// resolveModelKey maps the requested model to a Comate modelKey. Unknown
// names (for example the legacy comate-chat alias) fall back to auto.
func resolveModelKey(info *relaycommon.RelayInfo) string {
	if info == nil || info.UpstreamModelName == "" {
		return "auto"
	}
	for _, model := range ModelList {
		if model == info.UpstreamModelName {
			return model
		}
	}
	return "auto"
}

// comateURL joins the channel base URL with a Comate path.
func comateURL(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + path
}

// comateExecuteEnvelope is the internal request produced by
// ConvertOpenAIRequest and consumed by DoRequest.
type comateExecuteEnvelope struct {
	Query string `json:"query"`
}
