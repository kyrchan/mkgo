package main

import (
	"strconv"
	"strings"

	lib "kernel.lane/guests/lib"
)

// shell.wasm — the interactive shell (AGENTS.md Phase 7): prompt loop
// over §4 input records, output relayed through the "console" service,
// file ops via the fs port client, session control via registry §7 ops.
// Built-ins: echo ls cat stat kill-session run help. There is no
// fork/exec: `run` goes through registry SPAWN (abi/ABI.md §7).

// ShellOptions tunes Run (zero value = production behavior).
type ShellOptions struct {
	// Root is prefixed to relative paths (login passes /home/<user> via
	// argv[1] per services/ABI-NOTES.md §4). Empty disables prefixing.
	Root string
	// Stop closes to end the loop (tests); nil runs forever.
	Stop <-chan struct{}
}

// Shell bundles one shell session's dependencies.
type Shell struct {
	k    lib.Kernel
	fs   *lib.FSClient
	reg  *lib.RegistryClient
	con  lib.Handle // bind handle of "console"
	root string

	line []rune
}

// Run drives a shell session until Stop.
func Run(k lib.Kernel, opts ShellOptions) {
	// NOTE: 0 is a VALID port handle; only -1 means "none".
	sh := &Shell{k: k, root: opts.Root, con: lib.InvalidHandle}
	for sh.con == lib.InvalidHandle {
		sh.con = k.PortBind(lib.NameConsole)
		if sh.con != lib.InvalidHandle {
			break
		}
		if stopped(opts.Stop) {
			return
		}
		k.Yield()
	}
	if c, err := lib.BindFS(k, "shell"); err == nil {
		c.SetBudget(20000)
		sh.fs = c
	} else {
		sh.out("fs unavailable")
	}
	if r, err := lib.BindRegistry(k); err == nil {
		r.SetBudget(5000)
		sh.reg = r
	} else {
		sh.out("registry unavailable")
	}

	sh.out("shell ready (user root: " + sh.rootLabel() + ")")
	sh.prompt()
	buf := make([]byte, lib.InputRecLen)
	for {
		n := k.InputRecv(buf)
		if n >= lib.InputRecLen {
			if ev, ok := lib.DecodeInputEvent(buf[:]); ok && ev.Kind == lib.KeyDown {
				sh.key(ev)
			}
		}
		if stopped(opts.Stop) {
			return
		}
		if n == 0 {
			k.Yield()
		}
	}
}

func (s *Shell) rootLabel() string {
	if s.root == "" {
		return "/"
	}
	return s.root
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

// out relays one tagged line through the console service.
func (s *Shell) out(line string) {
	msg := "[shell] " + line
	s.k.PortSend(s.con, []byte(msg))
}

func (s *Shell) prompt() { s.out("> ") }

// key applies one input event to the line editor.
func (s *Shell) key(ev lib.InputEvent) {
	switch ev.Codepoint {
	case '\r', '\n':
		line := strings.TrimSpace(string(s.line))
		s.line = s.line[:0]
		s.exec(line)
		s.prompt()
	case 8, 127: // backspace / delete
		if len(s.line) > 0 {
			s.line = s.line[:len(s.line)-1]
		}
	default:
		if ev.Codepoint >= 32 && len(s.line) < 512 {
			s.line = append(s.line, rune(ev.Codepoint))
		}
	}
}

// resolve joins a user-supplied path with the session root when relative.
func (s *Shell) resolve(p string) string {
	if p == "" || strings.HasPrefix(p, "/") || s.root == "" {
		if p == "" {
			return "/"
		}
		return p
	}
	if !strings.HasPrefix(s.root, "/") {
		return "/" + s.root + "/" + p
	}
	return s.root + "/" + p
}

func (s *Shell) exec(line string) {
	if line == "" {
		return
	}
	fields := strings.Fields(line)
	cmd, args := fields[0], fields[1:]
	switch cmd {
	case "help":
		s.out("built-ins: echo ls cat stat kill-session run help")
	case "echo":
		s.out(strings.Join(args, " "))
	case "ls":
		s.cmdLs(args)
	case "cat":
		s.cmdCat(args)
	case "stat":
		s.cmdStat(args)
	case "kill-session":
		s.cmdKill(args)
	case "run":
		s.cmdRun(args)
	default:
		s.out("sh: unknown command: " + cmd)
	}
}

func (s *Shell) cmdLs(args []string) {
	if s.fs == nil {
		s.out("fs unavailable")
		return
	}
	path := s.root // bare ls: list this session's root
	if path == "" {
		path = "/"
	}
	if len(args) > 0 {
		path = s.resolve(args[0])
	}
	ents, err := s.fs.List(path)
	if err != nil {
		s.out("ls: " + err.Error())
		return
	}
	if len(ents) == 0 {
		s.out("(empty)")
		return
	}
	for _, e := range ents {
		name := e.Name
		if e.IsDir() {
			name += "/"
		}
		s.out(name)
	}
}

func (s *Shell) cmdCat(args []string) {
	if s.fs == nil || len(args) == 0 {
		s.out("usage: cat <file>")
		return
	}
	path := s.resolve(args[0])
	buf := make([]byte, 2048)
	var all []byte
	off := uint64(0)
	for {
		n, err := s.fs.ReadFile(path, off, buf)
		if err != nil {
			s.out("cat: " + err.Error())
			return
		}
		all = append(all, buf[:n]...)
		off += uint64(n)
		if n < len(buf) || len(all) > lib.MaxMsg*8 {
			break
		}
	}
	// text-oriented v1: emit line-wise so the console relay stays sane
	for _, ln := range strings.Split(strings.TrimRight(string(all), "\n"), "\n") {
		if ln != "" {
			s.out(ln)
		}
	}
}

func (s *Shell) cmdStat(args []string) {
	if s.fs == nil || len(args) == 0 {
		s.out("usage: stat <path>")
		return
	}
	st, err := s.fs.Stat(s.resolve(args[0]))
	if err != nil {
		s.out("stat: " + err.Error())
		return
	}
	kind := "file"
	if st.IsDir() {
		kind = "dir"
	}
	s.out(args[0] + " " + kind + " size=" + strconv.FormatUint(uint64(st.Size), 10))
}

func (s *Shell) cmdKill(args []string) {
	if s.reg == nil || len(args) == 0 {
		s.out("usage: kill-session <sid>")
		return
	}
	sid, err := strconv.ParseUint(args[0], 10, 32)
	if err != nil {
		s.out("kill-session: bad sid")
		return
	}
	st, err := s.reg.Kill(uint32(sid))
	switch {
	case err != nil:
		s.out("kill-session: " + err.Error())
	case st != 0:
		s.out("kill-session: denied (rc=" + strconv.Itoa(int(st)) + ")")
	default:
		s.out("killed " + args[0])
	}
}

// cmdRun launches a module via registry SPAWN with this shell's own
// capability set (looked up through LIST+CAPS; never-more-than-caller).
func (s *Shell) cmdRun(args []string) {
	if s.reg == nil || len(args) == 0 {
		s.out("usage: run <module> [args...]")
		return
	}
	mask := s.ownMask()
	sid, err := s.reg.Spawn(args[0], args[0], mask, args...)
	if err != nil {
		s.out("run: " + err.Error())
		return
	}
	s.out("spawned sid=" + strconv.FormatUint(uint64(sid), 10))
}

func (s *Shell) ownMask() uint64 {
	if s.reg == nil {
		return 0
	}
	list, err := s.reg.List()
	if err != nil {
		return 0
	}
	// v1 identity heuristic: our session name is our argv[0]
	for _, si := range list {
		if si.Name == lib.NameShell && lib.Alive(si.State) {
			caps, err := s.reg.Caps(si.Sid)
			if err != nil {
				return 0
			}
			var m uint64
			for _, c := range caps {
				m |= c.Rights
			}
			return m
		}
	}
	return 0
}
