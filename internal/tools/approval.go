package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/config"
)

const approvalInputID = "approval"

const maxApprovalMessage = 4 << 10

// errNeedsApproval is returned by a handler to ask the user before going on.
// addContentTool turns it into an input-required result; the SDK then asks
// the client (or, for clients on protocol versions before 2026-07-28, sends
// an elicitation itself) and calls the handler again with the answer.
type errNeedsApproval struct {
	state  string
	params *mcp.ElicitParams
}

func (e *errNeedsApproval) Error() string { return "waiting for the user's approval" }

var errDeclined = errors.New("the user did not approve this action")

// needsApproval reports whether changes and commands need the user's approval.
func (d *Deps) needsApproval() bool { return d.Cfg.Mode == config.ModeAsk }

// approvalBySession reports whether sh-mcp itself asks the user, which needs
// a client that can show forms (elicitation). Otherwise the client's own
// confirmation prompt, triggered by destructiveHint, is the only check.
func approvalBySession(req *mcp.CallToolRequest) bool {
	if req == nil || req.Session == nil {
		return false
	}
	caps := req.ClientCapabilities()
	if caps == nil || caps.Elicitation == nil {
		return false
	}
	// An elicitation capability without modes means form mode.
	return caps.Elicitation.Form != nil || caps.Elicitation.URL == nil
}

// approve returns nil when the call may go ahead, errDeclined when the user
// refused, or *errNeedsApproval when the user has to be asked first. message
// describes what will happen. A retry is only accepted with the same
// arguments and the same message (which also covers state the handler read,
// such as the diff of an edit), so the user never approves something other
// than what runs.
func (d *Deps) approve(req *mcp.CallToolRequest, message string) error {
	if !d.needsApproval() || !approvalBySession(req) {
		return nil
	}
	state := approvalState(req.Params.Name, req.Params.Arguments, message)
	if res, ok := req.Params.InputResponses[approvalInputID].(*mcp.ElicitResult); ok && req.Params.RequestState == state {
		if res.Action == "accept" {
			return nil
		}
		return errDeclined
	}
	return &errNeedsApproval{state: state, params: &mcp.ElicitParams{
		Mode:            "form",
		Message:         truncate(message, maxApprovalMessage),
		RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}}
}

// approvalState digests the call. Arguments are re-encoded first so that a
// client re-serializing them on retry (key order, spacing) still matches.
func approvalState(tool string, args json.RawMessage, message string) string {
	canonical := []byte(args)
	var v any
	if json.Unmarshal(args, &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			canonical = b
		}
	}
	h := sha256.New()
	for _, part := range [][]byte{[]byte(tool), canonical, []byte(message)} {
		h.Write(part)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// approvalNote describes, for get_environment, how approvals work for the
// connected client.
func (d *Deps) approvalNote(req *mcp.CallToolRequest) string {
	if !d.needsApproval() {
		return ""
	}
	if approvalBySession(req) {
		return "approvals: sh-mcp asks the user before each change or command; if declined, the tool returns an error"
	}
	return "approvals: this client cannot show sh-mcp's approval prompts, so changes and commands rely on the client's own confirmation"
}
