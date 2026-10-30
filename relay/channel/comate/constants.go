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

// ModelList is the model suggestion list shown when creating the channel.
// Chat requests always use modelKey=auto; keep one alias for the UI.
var ModelList = []string{
	"comate-chat",
}
