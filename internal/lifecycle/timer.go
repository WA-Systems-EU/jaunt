package lifecycle

import "time"

// TimerInterval is the supervise timer's OnUnitActiveSec and PassTimeout
// the supervise unit's TimeoutStartSec (systemd kills a longer pass). They
// must match internal/cloudinit/units (a test checks); the laptop's
// `jauntbox down` sizes its wait from them.
const (
	TimerInterval = 2 * time.Minute
	PassTimeout   = 600 * time.Second
)
