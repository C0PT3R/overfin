package takeover

import "time"

// SetKeyWait shortens SetUp's wait between tries, for tests.
func SetKeyWait(d time.Duration) { keyWait = d }
