package application

import "time"

// setNow lets the tests drive the clock.
func SetNow(l *Limiter, f func() time.Time) { l.now = f }
