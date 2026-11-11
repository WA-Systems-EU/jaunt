package client

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/WA-Systems-EU/jaunt/internal/provider"
)

// StatusReport is what `jauntbox status` shows.
type StatusReport struct {
	Box    *provider.Box // nil when no box exists
	Online bool          // on the tailnet
	// BoxStatus is the raw `box-status --json` output, set when reachable.
	BoxStatus json.RawMessage
	// Locked is the box's own report that its secrets are not in memory, read
	// after any unlock attempt.
	Locked bool
	// Err describes why BoxStatus is missing although the box is online.
	Err string
}

// Status reports the provider's view of the box, tailnet presence and, when
// reachable, the supervisor's own view.
func Status(ctx context.Context, d Deps) (StatusReport, error) {
	b, err := d.Prov.FindBox(ctx, labels(d.Cfg.User, "box"))
	if err != nil {
		return StatusReport{}, err
	}
	if b == nil {
		return StatusReport{}, nil
	}
	rep := StatusReport{Box: b}
	online, err := d.Mesh.DeviceOnline(ctx, hostname(d.Cfg.User))
	if err != nil {
		rep.Err = "tailnet check failed: " + err.Error()
		return rep, nil
	}
	rep.Online = online
	if !online {
		return rep, nil
	}
	tryUnlock(ctx, d)
	out, err := rootRun(ctx, d, "jauntctl", "box-status", "--json")
	if err != nil {
		if errors.Is(err, ErrRootSSH) {
			rep.Err = err.Error()
		} else {
			rep.Err = "box-status failed (supervisor not installed yet?): " + err.Error()
		}
		return rep, nil
	}
	if json.Valid(out) {
		rep.BoxStatus = out
		var st struct {
			Locked bool `json:"locked"`
		}
		_ = json.Unmarshal(out, &st)
		rep.Locked = st.Locked
	} else {
		rep.Err = "box-status returned invalid JSON: " + string(out)
	}
	return rep, nil
}
