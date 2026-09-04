package main

import (
	"fmt"
	"strconv"
	"strings"

	lib "kernel.lane/guests/lib"
)

// init.wasm — the one session the kernel spawns at boot (AGENTS.md).
// Reads init.conf text (delivered via WASI argv by the loader), spawns
// the system servers in order through registry SPAWN, applies kernel.conf
// knobs via the registry, then supervises: dead children are respawned
// per the config's respawn column.

// Service is one init.conf line.
type Service struct {
	Name    string
	Path    string // v1: informational; SPAWN resolves /boot/modules/<name>
	Capmask uint64
	Respawn bool
}

// ParseConf parses the init.conf text format:
//
//	# comment
//	console /boot/modules/console.wasm 0x8 respawn=yes
//	login  /boot/modules/login.wasm    0x48
//
// Blank lines and #-comments are skipped; respawn defaults to yes.
func ParseConf(text string) ([]Service, error) {
	var out []Service
	for ln, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, fmt.Errorf("init.conf:%d: want <name> <path> <capmask> [respawn]", ln+1)
		}
		mask, err := strconv.ParseUint(strings.TrimPrefix(fields[2], "0x"), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("init.conf:%d: bad capmask %q", ln+1, fields[2])
		}
		respawn := true
		if len(fields) >= 4 {
			switch fields[3] {
			case "respawn=yes":
				respawn = true
			case "respawn=no":
				respawn = false
			default:
				return nil, fmt.Errorf("init.conf:%d: bad policy %q", ln+1, fields[3])
			}
		}
		out = append(out, Service{
			Name:    fields[0],
			Path:    fields[1],
			Capmask: mask,
			Respawn: respawn,
		})
	}
	return out, nil
}

// ParseKnobs parses kernel.conf `key=value` text (#-comments allowed).
func ParseKnobs(text string) map[string]string {
	knobs := make(map[string]string)
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.IndexByte(line, '='); i > 0 {
			knobs[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
		}
	}
	return knobs
}

const (
	maxSpawnFails    = 5
	defaultPollEvery = 100000
	// listSaturationCap mirrors the kernel LIST record cap
	// (core/kernsvc.cc: sched_list(..., 12), no truncation flag).
	listSaturationCap = 12
)

type svcState struct {
	svc     Service
	sid     uint32
	fails   int
	spawned bool
}

// InitOptions tunes Run (zero value = production behavior).
type InitOptions struct {
	Services  []Service
	Knobs     string
	PollEvery int
	Stop      <-chan struct{}
	// Log receives one line per lifecycle event (tests); default drops.
	Log func(string)
	// ReadFile reads a config file for `reload_initconf` (tests inject a
	// map; production wires the fs client lazily — nil means unavailable).
	ReadFile func(path string) (string, error)
}

type supervisor struct {
	k      lib.Kernel
	reg    *lib.RegistryClient
	states []*svcState
	poll   int
	logf   func(string)
	// Phase 19 control surface.
	book      *lib.ReplyBook
	initH     lib.Handle
	knobsText string
	readFile  func(path string) (string, error)
}

// Run boots and supervises until Stop. It never returns in production.
func Run(k lib.Kernel, opts InitOptions) {
	reg, err := lib.BindRegistry(k)
	if err != nil {
		return // no registry → kernel is going down
	}
	reg.SetBudget(5000)
	logf := opts.Log
	if logf == nil {
		logf = func(string) {}
	}
	poll := opts.PollEvery
	if poll == 0 {
		poll = defaultPollEvery
	}

	sup := &supervisor{k: k, reg: reg, poll: poll, logf: logf}
	sup.knobsText = opts.Knobs
	sup.readFile = opts.ReadFile
	if sup.readFile == nil {
		sup.readFile = fsReadFile(k)
	}
	// Phase 19: serve initctl on the well-known "init" port (AGENTS.md:
	// no new kernel op — the kernel just relays these datagrams).
	if h := k.PortCreate(lib.NameInit); h != lib.InvalidHandle {
		sup.initH = h
	} else if h := k.PortBind(lib.NameInit); h != lib.InvalidHandle {
		sup.initH = h
	} else {
		sup.initH = lib.InvalidHandle
	}
	if sup.initH != lib.InvalidHandle {
		sup.book = lib.NewReplyBook(k)
	}
	for _, s := range opts.Services {
		sup.states = append(sup.states, &svcState{svc: s})
	}

	sup.applyKnobs(opts.Knobs)

	for _, st := range sup.states { // boot order = conf order
		sup.spawn(st)
	}

	sweeps := 0
	for {
		if stopped(opts.Stop) {
			return
		}
		sup.pollInitctl() // non-blocking; cheap when no initctl traffic
		sweeps++
		if sweeps%poll == 0 {
			sup.sweep()
		}
		k.Yield()
	}
}

func (s *supervisor) spawn(st *svcState) {
	if st.fails >= maxSpawnFails {
		return // give up; logged on the last attempt
	}
	sid, err := s.reg.Spawn(st.svc.Name, st.svc.Path, st.svc.Capmask)
	if err != nil {
		st.fails++
		s.logf("[init] spawn " + st.svc.Name + " failed (" + fmt.Sprint(st.fails) + "/" +
			fmt.Sprint(maxSpawnFails) + "): " + err.Error())
		if st.fails == maxSpawnFails {
			s.logf("[init] giving up on " + st.svc.Name)
		}
		return
	}
	st.sid = sid
	st.spawned = true
	st.fails = 0
	s.logf("[init] spawned " + st.svc.Name + " sid=" + fmt.Sprint(sid))
}

// sweep reconciles configured services against the live session list.
// Retry applies to any respawn=yes service that isn't currently alive —
// including ones that never managed to boot in the first place.
func (s *supervisor) sweep() {
	list, err := s.reg.List()
	if err != nil {
		return // transient registry hiccup; retry next sweep
	}
	// The kernel LIST caps records (12, no truncation flag): a saturated
	// list cannot distinguish "absent" from "beyond the cap", so respawn
	// decisions would risk double-spawning live services. Defer instead.
	if len(list) >= listSaturationCap {
		s.logf("[init] session list saturated; deferring respawn sweep")
		return
	}
	alive := make(map[uint32]bool, len(list))
	for _, si := range list {
		alive[si.Sid] = lib.Alive(si.State)
	}
	for _, st := range s.states {
		if !st.svc.Respawn {
			continue
		}
		if st.spawned {
			if ok, known := alive[st.sid]; known && ok {
				continue
			}
			s.logf("[init] " + st.svc.Name + " (sid=" + fmt.Sprint(st.sid) + ") gone; respawning")
			st.spawned = false
		} else if st.fails == 0 {
			continue // healthy initial state handled by boot loop
		}
		s.spawn(st)
	}
}

// applyKnobs pushes kernel.conf entries through §7 op 6 SETCONF
// {char key[16], u64 value} (ABI v1.1; needs CAP_CONF). Non-numeric
// values have no u64 representation — logged and skipped. A pre-v1.1
// kernel answers with no reply (unknown op): tolerated and logged.
func (s *supervisor) applyKnobs(knobsText string) {
	if knobsText == "" {
		return
	}
	for key, val := range ParseKnobs(knobsText) {
		num, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			s.logf("[init] knob " + key + "=" + val + " skipped (not numeric)")
			continue
		}
		if err := s.reg.SetConf(key, num); err != nil {
			s.logf("[init] knob " + key + "=" + val + " rejected (" + err.Error() + ")")
			continue
		}
		s.logf("[init] knob " + key + "=" + val + " applied")
	}
}

// fsReadFile returns a lazy /etc reader over the fs port client: the "fs"
// server may not exist yet at init boot (init spawns it), so the client is
// bound on first use, not at startup. Used by reload_initconf.
func fsReadFile(k lib.Kernel) func(string) (string, error) {
	var fs *lib.FSClient
	return func(path string) (string, error) {
		if fs == nil {
			c, err := lib.BindFS(k, "init")
			if err != nil {
				return "", err
			}
			c.SetBudget(20000)
			fs = c
		}
		buf := make([]byte, 4096)
		var data []byte
		for off := uint64(0); ; {
			n, err := fs.ReadFile(path, off, buf)
			if err != nil {
				return "", err
			}
			if n == 0 {
				break
			}
			data = append(data, buf[:n]...)
			off += uint64(n)
			if n < len(buf) {
				break
			}
		}
		return string(data), nil
	}
}

// pollInitctl drains one batch of initctl datagrams on the "init" port
// (Phase 19). Canonical framing: op = subop, payload = service name.
func (s *supervisor) pollInitctl() {
	if s.initH == lib.InvalidHandle {
		return
	}
	buf := make([]byte, lib.MaxMsg)
	for i := 0; i < 4; i++ { // bounded batch per scheduler turn
		n := s.k.PortRecv(s.initH, buf)
		if n <= 0 {
			return
		}
		hdr, ok := lib.ParseHeader(buf[:n])
		if !ok || hdr.RNam == "" {
			continue // not an initctl RPC; ignore
		}
		rh, err := s.book.Bind(hdr.RNam)
		if err != nil {
			continue
		}
		pl := append([]byte(nil), buf[lib.CanonicalHeaderLen:n]...)
		st, detail := s.handleInitctl(hdr.Op, pl)
		rep := make([]byte, lib.CanonicalHeaderLen+4+len(detail))
		lib.Put16(rep, hdr.Op)
		lib.Put16(rep[2:], hdr.Seq)
		lib.Put32(rep[lib.CanonicalHeaderLen:], st)
		copy(rep[lib.CanonicalHeaderLen+4:], detail)
		s.k.PortSend(rh, rep)
	}
}

// handleInitctl executes one initctl subop, returning status + detail
// (lib.InitOK / InitNotFound / InitBadName / InitUnavailable).
func (s *supervisor) handleInitctl(subop uint16, pl []byte) (uint32, string) {
	switch subop {
	case lib.InitSubRestart:
		name := string(pl)
		if name == "" || len(name) > 15 {
			return lib.InitBadName, "bad service name"
		}
		st := s.byName(name)
		if st == nil {
			return lib.InitNotFound, name
		}
		s.restart(st)
		return lib.InitOK, "sid=" + fmt.Sprint(st.sid)
	case lib.InitSubReload:
		n, err := s.reloadInitconf()
		if err != nil {
			if err == errNoConfSource {
				return lib.InitUnavailable, "no conf source"
			}
			return lib.InitBadName, err.Error()
		}
		return lib.InitOK, fmt.Sprint(n) + " services"
	case lib.InitSubApplyKnobs:
		s.applyKnobs(s.knobsText)
		return lib.InitOK, "knobs applied"
	case lib.InitSubPolicy:
		name, yes, ok := lib.SplitPolicyPayload(pl)
		if !ok || name == "" || len(name) > 15 {
			return lib.InitBadName, "want name\\x00[01]"
		}
		st := s.byName(name)
		if st == nil {
			return lib.InitNotFound, name
		}
		st.svc.Respawn = yes
		flag := "no"
		if yes {
			flag = "yes"
		}
		s.logf("[init] respawn " + name + "=" + flag)
		return lib.InitOK, "respawn=" + flag
	}
	return lib.InitBadName, "unknown subop"
}

func (s *supervisor) byName(name string) *svcState {
	for _, st := range s.states {
		if st.svc.Name == name {
			return st
		}
	}
	return nil
}

// restart kills a live service (if any) and spawns it fresh immediately,
// bypassing the sweep throttle. Manual restarts reset the backoff counter.
func (s *supervisor) restart(st *svcState) {
	if st.spawned {
		if _, err := s.reg.Kill(st.sid); err == nil {
			s.logf("[init] " + st.svc.Name + " (sid=" + fmt.Sprint(st.sid) + ") restarted by initctl")
		}
		st.spawned = false
	}
	st.fails = 0
	s.spawn(st)
}

var errNoConfSource = fmt.Errorf("no conf source")

// reloadInitconf re-reads /etc/init.conf and adopts the diff: new names are
// appended and spawned, removed names stop being supervised (left running),
// and respawn flags/paths/masks on surviving names are updated. Dead
// respawn=yes services are respawned at once.
func (s *supervisor) reloadInitconf() (int, error) {
	if s.readFile == nil {
		return 0, errNoConfSource
	}
	text, err := s.readFile("/etc/init.conf")
	if err != nil {
		return 0, errNoConfSource
	}
	svcs, err := ParseConf(text)
	if err != nil {
		return 0, err
	}
	keep := make(map[string]bool, len(svcs))
	for _, svc := range svcs {
		keep[svc.Name] = true
		if st := s.byName(svc.Name); st != nil {
			st.svc.Path = svc.Path
			st.svc.Capmask = svc.Capmask
			st.svc.Respawn = svc.Respawn
			continue
		}
		nst := &svcState{svc: svc}
		s.states = append(s.states, nst)
		s.spawn(nst)
	}
	for _, st := range s.states {
		if !keep[st.svc.Name] {
			st.svc.Respawn = false
			s.logf("[init] " + st.svc.Name + " dropped from init.conf (left running)")
		}
	}
	// Respawn anything dead that the new config wants alive.
	list, err := s.reg.List()
	if err == nil {
		alive := make(map[uint32]bool, len(list))
		for _, si := range list {
			alive[si.Sid] = lib.Alive(si.State)
		}
		for _, st := range s.states {
			if !st.svc.Respawn || !st.spawned {
				continue
			}
			if ok, known := alive[st.sid]; !known || !ok {
				st.spawned = false
				st.fails = 0
				s.spawn(st)
			}
		}
	}
	s.logf("[init] reloaded init.conf (" + fmt.Sprint(len(svcs)) + " services)")
	return len(svcs), nil
}

func stopped(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
