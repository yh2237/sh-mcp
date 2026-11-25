package tools

import (
	"encoding/json"
	"testing"
)

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
