package kern

import "runtime/debug"

func init() {
	debug.SetMemoryLimit(200 << 20)
}
