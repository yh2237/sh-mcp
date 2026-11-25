package tools

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yh2237/sh-mcp/internal/config"
	"github.com/yh2237/sh-mcp/internal/security"
)

const approvalInputID = "approval"

const (
	maxApprovalMessage = 4 << 10
	// approvalTTL bounds how long the user can take to answer.
	approvalTTL = 15 * time.Minute
)

// approvalStore remembers the approval requests sh-mcp has sent. An answer is
// only accepted for a request that was really issued, for the same action,
// and only once: a client cannot approve by sending an answer nobody asked
// for, or reuse an old approval for a second identical call.
type approvalStore struct {
	mu      sync.Mutex
	pending map[string]time.Time // request state -> expiry
}

// issue registers a new request for the action digest and returns its state.
func (s *approvalStore) issue(action string) string {
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	state := action + "." + hex.EncodeToString(nonce[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = map[string]time.Time{}
	}
	now := time.Now()
	for k, exp := range s.pending {
		if now.After(exp) {
			delete(s.pending, k)
		}
	}
	s.pending[state] = now.Add(approvalTTL)
	return state
}

// consume reports whether state is an outstanding request for action, and
// forgets it.
func (s *approvalStore) consume(state, action string) bool {
	if !strings.HasPrefix(state, action+".") {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.pending[state]
	delete(s.pending, state)
	return ok && time.Now().Before(exp)
}

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
// describes what will happen. An answer is only accepted for a request
// sh-mcp issued for the same arguments and message (which also covers state
// the handler read, such as the diff of an edit), so the user never approves
// something other than what runs; anything else is asked again.
func (d *Deps) approve(req *mcp.CallToolRequest, message string) error {
	if !d.needsApproval() || !approvalBySession(req) {
		return nil
	}
	action := approvalState(req.Params.Name, req.Params.Arguments, message)
	if res, ok := req.Params.InputResponses[approvalInputID].(*mcp.ElicitResult); ok && d.approvals.consume(req.Params.RequestState, action) {
		if res.Action == "accept" {
			return nil
		}
		return errDeclined
	}
	return &errNeedsApproval{state: d.approvals.issue(action), params: &mcp.ElicitParams{
		Mode:            "form",
		Message:         approvalMessage(message),
		RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}}
}

// approveOutside lets the read tools in ask mode read paths outside the
// allowed directories, asking once for all of them. It reports whether the
// call may resolve its paths with Guard.ResolveOutside. Read tools are marked
// read-only, so no client confirms them on its own: a client that cannot show
// sh-mcp's prompt gets an error instead.
func (d *Deps) approveOutside(req *mcp.CallToolRequest, paths ...string) (bool, error) {
	if !d.needsApproval() {
		return false, nil
	}
	var outside []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := d.Guard.ResolveRead(p); !errors.Is(err, security.ErrOutsideRoots) {
			continue
		}
		abs, err := d.Guard.Abs(p)
		if err != nil {
			continue
		}
		if d.Guard.IsDenied(abs) {
			// Refused either way; say why without asking first.
			return false, fmt.Errorf("%s: %w", abs, security.ErrDenied)
		}
		outside = append(outside, abs)
	}
	if len(outside) == 0 {
		return false, nil
	}
	if !approvalBySession(req) {
		return false, fmt.Errorf("%s is outside the allowed directories (--allow-dir); ask mode can read it after the user's approval, but this client cannot show sh-mcp's approval prompt", outside[0])
	}
	if err := d.approve(req, "Read outside the allowed directories:\n  "+strings.Join(outside, "\n  ")); err != nil {
		return false, err
	}
	return true, nil
}

// resolveRead resolves a path for a read tool; outside comes from
// approveOutside.
func (d *Deps) resolveRead(p string, outside bool) (*security.Resolved, error) {
	if outside {
		return d.Guard.ResolveOutside(p)
	}
	return d.Guard.ResolveRead(p)
}

// approvalMessage shortens a long message for the prompt and says so, so a
// cut-off script or file is not mistaken for the whole of it.
func approvalMessage(message string) string {
	if len(message) <= maxApprovalMessage {
		return message
	}
	cut := maxApprovalMessage
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + fmt.Sprintf("\n[... %d more bytes not shown]", len(message)-cut)
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
