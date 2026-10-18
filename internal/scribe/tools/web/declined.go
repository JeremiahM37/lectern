package web

import "github.com/JeremiahM37/lectern/v2/internal/scribe/tools"

// UserDeclined is the tool result for an approval the user denied. The file,
// web, MCP and symbol-edit tools all use it, so the model reads the same
// decision whichever tool was refused. It follows the shell's wording
// (tools/exec userDeclined): the decision is the user's, not a sandbox rule,
// it stands for this request, and it covers only this call. Without that
// last clause a model takes a refusal as a reason to stop for good. The
// reason the user gave, if any, is appended last.
//
// what names the action in the user's terms, e.g. "edit sensitive file:
// .github/workflows/ci.yml".
func UserDeclined(what, reason string) string {
	return "Denied: the user chose Deny when asked to " + what + ". " +
		"This was the user's decision, not a sandbox or tool restriction, so tell them it was not done because they declined. " +
		"Do not retry it or a variant of it for the current request, and do not ask for it another way. " +
		"The denial covers this call only: when the user asks for something else later, even something similar, call the tool for it as usual; the user is asked each time and decides. " +
		"Then continue with whatever else the task needs." +
		tools.DeniedWithReason(reason)
}
