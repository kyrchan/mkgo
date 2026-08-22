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
	defaultPollEvery = 10000
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
}

type supervisor struct {
	k      lib.Kernel
	reg    *lib.RegistryClient
	states []*svcState
	poll   int
	logf   func(string)
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

// applyKnobs pushes kernel.conf entries through the proposed §7 SETCONF
// op (services/ABI-NOTES.md §6). The v1 kernel has no SETCONF: each knob
// is rejected with no reply — tolerated and logged.
func (s *supervisor) applyKnobs(knobsText string) {
	if knobsText == "" {
		return
	}
	h := s.reg.Handle()
	for key, val := range ParseKnobs(knobsText) {
		pl := lib.AppendLStr(nil, key)
		pl = lib.AppendLStr(pl, val)
		rep, err := s.reg.Request(h, lib.OpRegistrySetconf, pl)
		switch {
		case err == lib.ErrNoReply:
			s.logf("[init] knob " + key + "=" + val + " rejected (registry lacks SETCONF)")
		case err != nil:
			s.logf("[init] knob " + key + " error: " + err.Error())
		default:
			st := int32(lib.Get32(rep[4:]))
			s.logf("[init] knob " + key + "=" + val + " status=" + fmt.Sprint(st))
		}
	}
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
