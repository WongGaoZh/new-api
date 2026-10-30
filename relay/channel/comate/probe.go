package comate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// globalConfig is the subset of GET /api/config/global used for endpoint
// discovery and environment verification during channel tests. The public
// endpoint nests env and feature flags under "system".
type globalConfig struct {
	Endpoint   string `json:"endpoint"`
	ServerHost string `json:"serverHost"`
	System     struct {
		Env           string `json:"env"`
		EnableZuluCLI bool   `json:"enableZuluCLI"`
	} `json:"system"`
}

// ProbeGlobalConfig calls GET {base}/api/config/global and verifies the SaaS
// environment. It is used by the channel test to confirm the endpoint answers
// before a real chat request is sent.
func ProbeGlobalConfig(ctx context.Context, baseURL string) (*globalConfig, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+GlobalConfigPath, nil)
	if err != nil {
		return nil, fmt.Errorf("build config/global request failed: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe %s failed: %w", GlobalConfigPath, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read config/global response failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("config/global returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var config globalConfig
	if err := common.Unmarshal(body, &config); err != nil {
		return nil, fmt.Errorf("parse config/global response failed: %w", err)
	}
	if config.System.Env != "saas" {
		return nil, fmt.Errorf("unexpected Comate environment %q, expected \"saas\"", config.System.Env)
	}
	return &config, nil
}

// ModelListForLicense fetches the models bound to a license. The public SaaS
// endpoint requires the license as a query parameter; the result varies per
// license, so it is fetched on demand instead of being hard-coded.
func ModelListForLicense(ctx context.Context, baseURL, license string) ([]string, error) {
	modelURL := fmt.Sprintf("%s/api/v2/api/models/available?key=%s&type=ZULU&repoName=&loopVersion=v2&source=plugin&ideType=vscode",
		strings.TrimRight(baseURL, "/"), url.QueryEscape(license))
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build model list request failed: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch model list failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read model list response failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model list returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := common.Unmarshal(body, &payload); err != nil {
		logger.LogWarn(context.Background(), "comate: failed to parse model list: %s", err.Error())
		return nil, nil
	}
	models := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		if strings.TrimSpace(m.Name) != "" {
			models = append(models, strings.TrimSpace(m.Name))
		}
	}
	return models, nil
}

// encodeLicense returns the percent-encoded license used in login-name headers.
func encodeLicense(license string) string {
	return url.QueryEscape(license)
}

// estimateTokens is a rough rune-based token estimate for billing. Comate does
// not report token usage, so new-api records an estimate only.
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	runes := []rune(text)
	// Chinese/ASCII mixed text: 1 rune ≈ 1 token for CJK, ~0.25 for ASCII.
	// A fixed 1/2 divisor keeps the estimate in a sane range for mixed input.
	return len(runes)/2 + 1
}

// estimateUsage estimates prompt/completion tokens for billing. Comate does
// not report token usage, so new-api records an estimate only.
func estimateUsage(query, answer string) dto.Usage {
	promptTokens := estimateTokens(query)
	completionTokens := estimateTokens(answer)
	return dto.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
}

// extractUserText returns the last user message text from an OpenAI chat
// request, which becomes the "query" of the Comate protocol.
func extractUserText(messages []dto.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		return messageContentText(messages[i].Content)
	}
	return ""
}

func messageContentText(content any) string {
	switch v := content.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		var parts []string
		for _, part := range v {
			if text := partContentText(part); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func partContentText(part any) string {
	obj, ok := part.(map[string]any)
	if !ok {
		return ""
	}
	if obj["type"] != "text" {
		return ""
	}
	text, _ := obj["text"].(string)
	return strings.TrimSpace(text)
}

var errNoUserQuery = errors.New("no user message content to send to Comate")
