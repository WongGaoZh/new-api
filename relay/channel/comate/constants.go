package comate

// ChannelName is the display name of the Comate channel type.
const ChannelName = "Comate"

// DefaultBaseURL is the public SaaS endpoint of Baidu Comate (企业公有云版).
// The channel also reads the license from the channel key; the license is sent
// in login-name / Uuap-login-name headers, not as Authorization.
const DefaultBaseURL = "https://comate.baidu.com"

// ChatStreamPath is the custom SSE chat endpoint of the public SaaS Comate API.
const ChatStreamPath = "/api/aidevops/autocomate/rest/autowork/v1/chat/stream"

// GlobalConfigPath is the environment/endpoint discovery endpoint. It returns
// env=saas and the authoritative serverHost, and is probed during channel tests.
const GlobalConfigPath = "/api/config/global"

// ModelList is the model suggestion list shown when creating the channel.
// Comate ignores the model name in chat requests, so a single stable alias is
// enough; actual model availability is bound to the license.
var ModelList = []string{
	"comate-chat",
}
