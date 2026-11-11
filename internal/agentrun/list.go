package agentrun

import "context"

// Run states reported by List.
const (
	StateRunning    = "running"
	StateWrappingUp = "wrapping-up"
	StateFinished   = "finished"
	StateGone       = "gone" // a record without a container
)

// Status is a run with its container's current state.
type Status struct {
	Run
	State string `json:"state"`
	IP    string `json:"ip,omitempty"`
}

// List returns every readable run record, oldest first, with its state. A
// finished record is not inspected; an exited container that the supervisor
// has not finished yet also reads as finished.
func List(ctx context.Context, d Docker, p Paths) ([]Status, error) {
	runs, err := p.All()
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(runs))
	for _, r := range runs {
		s := Status{Run: r, State: StateFinished}
		if r.FinishedAt.IsZero() {
			st, err := d.Inspect(ctx, ContainerName(r.ID))
			if err != nil {
				return nil, err
			}
			switch {
			case !st.Exists:
				s.State = StateGone
			case !st.Running:
				s.State = StateFinished
			case !r.WrapUpAt.IsZero():
				s.State, s.IP = StateWrappingUp, st.IP
			default:
				s.State, s.IP = StateRunning, st.IP
			}
		}
		out = append(out, s)
	}
	return out, nil
}
