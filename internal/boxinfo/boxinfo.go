// Package boxinfo describes the box facts written into /etc/jauntbox/box.json.
package boxinfo

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/config"
)

// Path is where the box description lives on the box.
const Path = "/etc/jauntbox/box.json"

// BoxInfo is what the on-box supervisor needs to know about its box.
type BoxInfo struct {
	User     string        `json:"user"`
	Hostname string        `json:"hostname"`
	Profile  string        `json:"profile"`
	Timezone string        `json:"timezone"`
	UpAt     time.Time     `json:"up_at"`
	Limits   config.Limits `json:"limits"`
}

// Read loads a BoxInfo from path.
func Read(path string) (BoxInfo, error) {
	var b BoxInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return b, fmt.Errorf("read box info %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, fmt.Errorf("parse box info %s: %w", path, err)
	}
	return b, nil
}

// Write stores b at path as indented JSON (0644).
func Write(path string, b BoxInfo) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
