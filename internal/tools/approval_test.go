package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApprovalStoreIsSingleUse(t *testing.T) {
	var s approvalStore
	action := approvalState("write_file", json.RawMessage(`{"path":"a"}`), "Create a?")
	state := s.issue(action)
	if s.consume(action+".forged", action) {
		t.Error("an answer to a request that was never issued was accepted")
	}
	if s.consume(state, approvalState("write_file", json.RawMessage(`{"path":"b"}`), "Create b?")) {
		t.Error("an answer was accepted for a different action")
	}
	if !s.consume(state, action) {
		t.Fatal("the answer to an issued request was refused")
	}
	if s.consume(state, action) {
		t.Error("an approval was accepted twice")
	}
}

func TestApprovalMessageSaysWhenCut(t *testing.T) {
	long := strings.Repeat("x", maxApprovalMessage+100)
	if got := approvalMessage(long); !strings.HasSuffix(got, "[... 100 more bytes not shown]") {
		t.Errorf("cut message: ...%q", got[len(got)-40:])
	}
	if got := approvalMessage("short"); got != "short" {
		t.Errorf("short message changed: %q", got)
	}
}

func TestApprovalStateBindsArguments(t *testing.T) {
	msg := "Run this command?\ncommand: cat"
	base := approvalState("run_command", json.RawMessage(`{"command":"cat","stdin":"aaa"}`), msg)
	if got := approvalState("run_command", json.RawMessage(`{ "stdin": "aaa", "command": "cat" }`), msg); got != base {
		t.Error("re-encoded arguments should give the same state")
	}
	// The message only shows a prefix of stdin; the arguments still differ.
	if got := approvalState("run_command", json.RawMessage(`{"command":"cat","stdin":"aab"}`), msg); got == base {
		t.Error("different arguments with the same message must not match")
	}
	if got := approvalState("run_shell", json.RawMessage(`{"command":"cat","stdin":"aaa"}`), msg); got == base {
		t.Error("different tools must not match")
	}
}
