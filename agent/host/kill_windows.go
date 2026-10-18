package host

import "errors"

// The agent manages Linux hosts; Windows builds are for development only.
func killPID(int, bool) error { return errors.New("not supported on this platform") }
