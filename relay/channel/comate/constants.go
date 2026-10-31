package comate

// ChannelName is the display name of the Comate channel type.
const ChannelName = "Comate"

// DefaultBaseURL is the public SaaS endpoint of Baidu Comate (企业公有云版).
const DefaultBaseURL = "https://comate.baidu.com"

// Public agent protocol paths (verified 2026-10-30): create a conversation,
// create a task inside it, then execute the task and read the SSE answer.
const (
	ConversationPath = "/api/aidevops/autocomate/rest/autowork/v2/conversation"
	TaskPath         = "/api/aidevops/autocomate/rest/autowork/v2/task"
	ExecutePath      = "/api/aidevops/autocomate/rest/autowork/v2/execute"

	// GlobalConfigPath returns env=saas and the authoritative serverHost.
	GlobalConfigPath = "/api/config/global"
)

// Client identity reported to Comate. These values mirror the official CLI and
// are required by the public endpoints.
const (
	ClientIDE           = "zulucli"
	ClientIDEVersion    = "1.7.16"
	ClientPluginVersion = "4.13.0"
	ClientAgentID       = 1
	ClientAgentVersion  = 12
)

// ModelList is the model list bound to an enterprise SaaS license. Values are
// Comate model types accepted by the execute endpoint as modelKey. Availability
// depends on the license; refresh from
// GET /api/v2/api/models/available?key=<license>&type=ZULU.
var ModelList = []string{
	"deepseek-v4-flash",
	"deepseek-v4-pro",
	"deepseek-v4.1-flash",
	"glm-4.7-fc",
	"glm-5-turbo-fc",
	"glm-5.1",
	"glm-5.2",
	"glm-5.3",
	"glm-5.3-flash",
	"glm-5v-turbo",
	"kimi-k3.1-pro",
	"kimi-k2.6-oneapi",
	"minimax-m3",
	"minimax-m2.7-fc",
}
