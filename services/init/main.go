// init.wasm -- the ONE session the kernel spawns at boot (Phase 7).
// argv[1] = \etc\init.conf contents (preloaded from ESP by the loader).
// Spawns console/fs/login/shell per conf, then focuses login.
package main

import (
	"os"
	"strings"

	lib "kernel.services/lib"
)

type line struct {
	name string
	mod  string
	caps uint32
}

func parseConf(conf string) []line {
	var out []line
	for _, raw := range strings.Split(conf, "\n") {
		ln := strings.TrimSpace(raw)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		f := strings.Fields(ln)
		if len(f) < 3 {
			continue
		}
		var caps uint32
		for _, ch := range f[2] {
			var d uint32
			if ch >= '0' && ch <= '9' {
				d = uint32(ch - '0')
			} else if ch >= 'a' && ch <= 'f' {
				d = uint32(ch-'a') + 10
			} else {
				continue
			}
			caps = caps*16 + d
		}
		out = append(out, line{f[0], f[1], caps})
	}
	return out
}

/* registry SPAWN frame: {u16 op,u16 seq,u32 uid,char rname[16],
   char name[16], char path[64], u32 capmask, u16 argc} */
func spawnViaRegistry(rg int32, name, mod string, caps uint32) bool {
	req := make([]byte, 24+16+64+4+2)
	req[0] = 4 // SPAWN
	req[3] = 9 // seq
	copy(req[8:24], "init")
	copy(req[24:40], name)
	copy(req[40:104], mod) // path field: module name (preloaded table)
	req[104] = byte(caps)
	req[105] = byte(caps >> 8)
	req[106] = byte(caps >> 16)
	req[107] = byte(caps >> 24)
	lib.SendOrBlock(rg, req)
	mq := lib.Bind("init") // replies land on our own well-known queue
	if mq < 0 {
		os.Stdout.WriteString("[init] no self-queue\n")
		return false
	}
	for i := 0; i < 200000; i++ {
		if m := lib.RecvNonBlocking(mq, 256); m != nil {
			if len(m) >= 8 && m[0] == 4 {
				st := uint32(m[4]) | uint32(m[5])<<8 |
					uint32(m[6])<<16 | uint32(m[7])<<24
				os.Stdout.WriteString("[init] spawn " + name + " rc=" +
					itoa(int(int32(st))) + "\n")
				return st == 0
			}
		}
		lib.SchedYield()
	}
	os.Stdout.WriteString("[init] spawn " + name + " TIMEOUT\n")
	return false
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func main() {
	argv := lib.Argv()
	conf := ""
	if len(argv) > 1 {
		conf = argv[1]
	}
	os.Stdout.WriteString("[init] up, conf " +
		itoa(len(conf)) + " bytes\n")

	// our well-known queue receives SPAWN verdicts
	if lib.Create("init") < 0 {
		os.Stdout.WriteString("[init] name taken\n")
	}
	rg := int32(-1)
	for i := 0; i < 500000 && rg < 0; i++ {
		rg = lib.Bind("registry")
		if rg < 0 {
			lib.SchedYield()
		}
	}
	if rg < 0 {
		os.Stdout.WriteString("[init] FATAL no registry\n")
		return
	}

	for _, ln := range parseConf(conf) {
		spawnViaRegistry(rg, ln.name, ln.mod, ln.caps)
	}

	// focus login: user authentication happens first
	lh := int32(-1)
	for i := 0; i < 500000 && lh < 0; i++ {
		lh = lib.Bind("login")
		if lh < 0 {
			lib.SchedYield()
		}
	}
	if lh >= 0 {
		lib.FocusTo(lh)
		os.Stdout.WriteString("[init] focus -> login\n")
	} else {
		os.Stdout.WriteString("[init] WARNING login not found\n")
	}

	// idle forever; supervision (respawn) lands with later phases
	for i := 0; ; i++ {
		lib.SchedYield()
	}
}
