// Package clock is the adapter of ports.Clock: the system time in UTC.
package clock

import (
	"time"

	"github.com/claudioed/offer-availability/internal/application/ports"
)

// System reads the wall clock.
type System struct{}

var _ ports.Clock = System{}

// Now implements ports.Clock.
func (System) Now() time.Time { return time.Now().UTC() }
