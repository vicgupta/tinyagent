package telemetry

// Span and attribute names. The gen_ai.* names follow the OpenTelemetry
// GenAI semantic conventions, which are in Development status and may
// change; keep every name here so they can follow.
const (
	opInvokeAgent = "invoke_agent"
	opChat        = "chat"
	opExecuteTool = "execute_tool"

	attrOperation        = "gen_ai.operation.name"
	attrAgentName        = "gen_ai.agent.name"
	attrProvider         = "gen_ai.provider.name"
	attrRequestModel     = "gen_ai.request.model"
	attrResponseModel    = "gen_ai.response.model"
	attrInputTokens      = "gen_ai.usage.input_tokens"
	attrOutputTokens     = "gen_ai.usage.output_tokens"
	attrCacheReadTokens  = "gen_ai.usage.cache_read.input_tokens"
	attrCacheWriteTokens = "gen_ai.usage.cache_creation.input_tokens"
	attrToolName         = "gen_ai.tool.name"
	attrToolCallID       = "gen_ai.tool.call.id"
	attrToolArguments    = "gen_ai.tool.call.arguments"
	attrToolResult       = "gen_ai.tool.call.result"
	attrErrorType        = "error.type"

	attrServiceName    = "service.name"
	attrServiceVersion = "service.version"

	attrModel        = "tinyagent.model"
	attrStatus       = "tinyagent.status"
	attrSteps        = "tinyagent.steps"
	attrStep         = "tinyagent.step"
	attrCostUSD      = "tinyagent.cost_usd"
	attrDurationMS   = "tinyagent.duration_ms"
	attrResumed      = "tinyagent.resumed"
	attrReadOnly     = "tinyagent.read_only"
	attrVersion      = "tinyagent.version"
	attrTask         = "tinyagent.task"
	attrAssistant    = "tinyagent.assistant.text"
	attrOutputBytes  = "tinyagent.output_bytes"
	attrRedactions   = "tinyagent.redactions"
	attrTruncated    = "tinyagent.truncated"
	attrFormatError  = "tinyagent.format_error"
	attrHookEvent    = "tinyagent.hook.event"
	attrHookDecision = "tinyagent.hook.decision"
	attrSubagentID   = "tinyagent.subagent.id"

	attrSubagentKind      = "tinyagent.subagent.kind"
	attrWorkerBranch      = "tinyagent.worker.branch"
	attrWorkerBaseCommit  = "tinyagent.worker.base_commit"
	attrWorkerCommit      = "tinyagent.worker.commit"
	attrWorkerFilesChange = "tinyagent.worker.files_changed"

	scopeName = "github.com/vicgupta/tinyagent"
)
