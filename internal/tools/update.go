package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultRestartDelay = time.Second

type updateInput struct {
	Restart bool `json:"restart,omitempty" jsonschema:"exit briefly after updating so a service manager (such as systemd with Restart=on-failure) starts the new binary; only set this when sh-mcp is supervised"`
}

func registerUpdate(s *mcp.Server, d *Deps) {
	desc := "Update the sh-mcp binary to the latest GitHub release (verified against SHA256SUMS). " +
		"Set restart=true only when sh-mcp runs under a service manager that restarts it."
	if d.needsApproval() {
		desc += " Waits for the user's approval."
	}
	addTool(s, d, &mcp.Tool{
		Name:        "update_sh_mcp",
		Title:       "Update sh-mcp",
		Description: desc,
		Annotations: execAnn,
	}, func(in updateInput) map[string]any {
		return map[string]any{"restart": in.Restart}
	}, d.updateSelf)
}

func (d *Deps) updateSelf(ctx context.Context, req *mcp.CallToolRequest, in updateInput) (string, error) {
	if d.Update == nil {
		return "", errors.New("self-update is not configured")
	}
	msg := "Update sh-mcp to the latest GitHub release?"
	if in.Restart {
		msg = "Update sh-mcp to the latest GitHub release and exit so a supervisor restarts it?"
	}
	if err := d.approve(req, msg); err != nil {
		return "", err
	}
	res, err := d.Update(ctx)
	if err != nil {
		return "", err
	}
	if !res.Updated {
		return fmt.Sprintf("sh-mcp %s is already the latest release", res.To), nil
	}
	if in.Restart && d.RequestRestart != nil {
		delay := d.RestartDelay
		if delay <= 0 {
			delay = defaultRestartDelay
		}
		time.AfterFunc(delay, d.RequestRestart)
		return fmt.Sprintf("updated %s from %s to %s; exiting shortly so the service manager restarts sh-mcp", res.Exe, res.From, res.To), nil
	}
	return fmt.Sprintf("updated %s from %s to %s; restart sh-mcp to use it", res.Exe, res.From, res.To), nil
}
