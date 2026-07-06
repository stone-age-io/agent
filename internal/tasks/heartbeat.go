package tasks

import (
	"github.com/stone-age-io/agent/internal/utils"
)

// Heartbeat is the liveness beacon payload, matching the shape used by the
// other stone-age.io applications (access-control, kiosk). The code/location
// are also in the subject; carrying them keeps the message self-describing
// for any direct subscriber. Agent version is deliberately absent — it is
// surfaced by the health command instead.
type Heartbeat struct {
	Code     string `json:"code"`
	Location string `json:"location"`
	TS       string `json:"ts"`
}

// CreateHeartbeat creates a new heartbeat message
func (e *Executor) CreateHeartbeat(code, location string) *Heartbeat {
	return &Heartbeat{
		Code:     code,
		Location: location,
		TS:       utils.NowRFC3339(),
	}
}
