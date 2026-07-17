package auth

import (
	"os"
	"strings"
)

// DevAuthEnv names the env var that disables authentication.
const DevAuthEnv = "LONGBOX_DEV_AUTH"

// DevAuthRequested reports whether the bypass was asked for, regardless of
// whether this binary can honour it.
//
// It exists so a release binary can say "you asked for the bypass and did not
// get it" rather than ignoring the variable in silence. A developer who forgets
// -tags dev otherwise sees a login page with no explanation, and the alternative
// — refusing to boot — would turn a stale env var on a real deployment into an
// outage for no safety gain, since the bypass is already absent from the binary.
func DevAuthRequested() bool { return strings.TrimSpace(os.Getenv(DevAuthEnv)) != "" }
