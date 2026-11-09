package config

import (
	"encoding/json"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that reads and writes Go duration strings
// ("30m", "2h") in both YAML and JSON.
type Duration time.Duration

func (d Duration) String() string { return time.Duration(d).String() }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"30m\" or \"2h\": %w", err)
	}
	return d.parse(s)
}

func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"30m\" or \"2h\": %w", err)
	}
	return d.parse(s)
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) parse(s string) error {
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q (use e.g. \"30m\", \"2h\"): %w", s, err)
	}
	*d = Duration(v)
	return nil
}
