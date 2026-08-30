package main

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

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
	// IOPort, when non-empty, enables port-based I/O mode: the shell
	// binds to a port named IOPort for outbound data (shell → host)
	// and IOPortIn for inbound data (host → shell). When IOPortIn is
	// empty, IOPort is used for both (real kernel supports bidirectional
	// per-handle queue routing; FakeKernel tests use separate ports).
	IOPort  string
	IOPortIn string
}

// Shell bundles one shell session's dependencies.
type Shell struct {
	k      lib.Kernel
	fs     *lib.FSClient
	reg    *lib.RegistryClient
	con    lib.Handle // bind handle of "console"
	root   string
	uid    uint32
	env    map[string]string
	ioh    lib.Handle // handle for --io-port output (InvalidHandle if unused)
	ioin   lib.Handle // handle for --io-in input (InvalidHandle if unused)
	line   []rune
	exitStatus int
}

// Run drives a shell session until Stop.
func Run(k lib.Kernel, opts ShellOptions) {
	// §4: the shell claims focus for itself once it owns its well-known
	// port (v1.1 flow — readiness-driven focus).
	if h := k.PortCreate(lib.NameShell); h != lib.InvalidHandle {
		k.FocusSet(h)
	} else if h := k.PortBind(lib.NameShell); h != lib.InvalidHandle {
		k.FocusSet(h)
	}

	// NOTE: 0 is a VALID port handle; only -1 means "none".
	sh := &Shell{k: k, root: opts.Root, con: lib.InvalidHandle, ioh: lib.InvalidHandle, ioin: lib.InvalidHandle}
	if sh.root == "" {
		sh.root = "/" // init-spawned shells get the global root
	}
	if opts.IOPort != "" {
		sh.ioh = k.PortCreate(opts.IOPort)
		if sh.ioh == lib.InvalidHandle {
			sh.ioh = k.PortBind(opts.IOPort)
		}
		inName := opts.IOPortIn
		if inName == "" {
			inName = opts.IOPort
		}
		sh.ioin = k.PortBind(inName)
		if sh.ioin == lib.InvalidHandle {
			sh.ioin = k.PortCreate(inName)
		}
	}
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
	buf := make([]byte, lib.RecvBufLen) // fits both v1 and v1.3 records
	iobuf := make([]byte, 4096)
	ioin := sh.ioin // capture for loop
	for {
		if ioin != lib.InvalidHandle {
			n := k.PortRecv(ioin, iobuf)
			if n > 0 {
				for _, b := range iobuf[:n] {
					ev := lib.InputEvent{Kind: lib.KeyDown, Codepoint: uint16(b)}
					sh.key(ev)
				}
			}
			if n < 0 {
				return
			}
		}
		n := k.InputRecv(buf)
		if n >= lib.InputRecLen {
			if ev, ok := lib.DecodeInputEvent(buf[:n]); ok && ev.Kind == lib.KeyDown {
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

func (s *Shell) userName() string {
	if s.reg != nil {
		if list, err := s.reg.List(); err == nil {
			for _, si := range list {
				if si.Name == lib.NameShell && lib.Alive(si.State) {
					return lib.Username(si.UID)
				}
			}
		}
	}
	return "unknown"
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
// Prefix [sh\0\0\0\0\0]  ensures bytes [4:8] are null (F32 uid stamping
// for root writes \x00 there — no-op on pre-filled nulls).
func (s *Shell) out(line string) {
	msg := []byte("[sh")
	msg = append(msg, 0, 0, 0, 0, 0)
	msg = append(msg, ']')
	msg = append(msg, ' ')
	msg = append(msg, []byte(line)...)
	s.k.PortSend(s.con, msg)
	if s.ioh != lib.InvalidHandle {
		s.k.PortSend(s.ioh, msg)
	}
}

// toConsole sends a message to the console port only (not the io output
// port). Used for terminal redraw protocol messages that should not
// be relayed back to the SSH/text client.
func (s *Shell) toConsole(msg []byte) {
	s.k.PortSend(s.con, msg)
}

// prompt writes "> " at the start of the current terminal line using
// the echo redraw protocol (\r prefix => no [console] tag, no trailing
// newline), so the cursor sits right after the space waiting for input.
func (s *Shell) prompt() {
	s.toConsole([]byte{'\r', '>', ' '})
}

// key applies one input event to the line editor.
func (s *Shell) key(ev lib.InputEvent) {
	switch ev.Codepoint {
	case '\r', '\n':
		line := strings.TrimSpace(string(s.line))
		s.line = s.line[:0]
		s.echoEnter()
		s.exec(line)
		s.prompt()
	case 8, 127: // backspace / delete
		if len(s.line) > 0 {
			s.line = s.line[:len(s.line)-1]
		}
		s.echo()
	default:
		if ev.Codepoint >= 32 && len(s.line) < 512 {
			s.line = append(s.line, rune(ev.Codepoint))
		}
		s.echo()
	}
}

// echo redraws the prompt and current input buffer in-place using '\r'
// so the serial terminal overwrites the previous line on each keystroke.
// The console service's render() treats the leading '\r' as "no newline,
// no tag" redraw mode.
//
// Bytes [3:8] are null-padded: the kernel's F32 uid stamping overwrites
// bytes [4:8] with the sender's uid. For a root shell (uid=0) the stamp
// writes \x00, which is a no-op on our pre-filled nulls — invisible on
// the terminal and harmless.
func (s *Shell) echo() {
	msg := []byte{'\r', '>', ' '}
	msg = append(msg, 0, 0, 0, 0, 0)
	msg = append(msg, []byte(string(s.line))...)
	msg = append(msg, '\x1b', '[', 'K') // clear to end of line (fixes backspace)
	s.toConsole(msg)
}

// echoEnter sends the final echo redraw with a trailing newline so
// render() advances the cursor after the typed command is displayed.
func (s *Shell) echoEnter() {
	msg := []byte{'\r', '>', ' '}
	msg = append(msg, 0, 0, 0, 0, 0)
	msg = append(msg, []byte(string(s.line))...)
	msg = append(msg, '\x1b', '[', 'K', '\n')
	s.toConsole(msg)
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
	if s.root == "/" {
		return "/" + p
	}
	return s.root + "/" + p
}

// readAll reads the entire file at path through the fs client, returning
// the concatenated bytes. Returns nil on error (caller prints the error).
func (s *Shell) readAll(path string) ([]byte, error) {
	if s.fs == nil {
		return nil, errors.New("fs unavailable")
	}
	buf := make([]byte, 4096)
	var data []byte
	off := uint64(0)
	for {
		n, err := s.fs.ReadFile(path, off, buf)
		if err != nil {
			return data, err
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
	return data, nil
}

func (s *Shell) exec(line string) {
	if line == "" {
		return
	}
	fields := strings.Fields(line)
	cmd, args := fields[0], fields[1:]
	switch cmd {
	case "help":
		s.out("built-ins: echo ls cat stat cp mv rmdir grep find head tail wc sort uniq tr cut sed sleep true false test date clear whoami id env printenv kill-session sessions caps run help vi pwd cd mkdir rm touch")
	case "echo":
		s.out(strings.Join(args, " "))
	case "pwd":
		s.cmdPwd()
	case "cd":
		s.cmdCd(args)
	case "mkdir":
		s.cmdMkdir(args)
	case "rm":
		s.cmdRm(args)
	case "rmdir":
		s.cmdRmdir(args)
	case "touch":
		s.cmdTouch(args)
	case "cp":
		s.cmdCp(args)
	case "mv":
		s.cmdMv(args)
	case "ls":
		s.cmdLs(args)
	case "cat":
		s.cmdCat(args)
	case "stat":
		s.cmdStat(args)
	case "grep":
		s.cmdGrep(args)
	case "find":
		s.cmdFind(args)
	case "head":
		s.cmdHead(args)
	case "tail":
		s.cmdTail(args)
	case "wc":
		s.cmdWc(args)
	case "sort":
		s.cmdSort(args)
	case "uniq":
		s.cmdUniq(args)
	case "tr":
		s.cmdTr(args)
	case "cut":
		s.cmdCut(args)
	case "sed":
		s.cmdSed(args)
	case "sleep":
		s.cmdSleep(args)
	case "true":
		return
	case "false":
		s.exitStatus = 1
	case "test", "[":
		s.cmdTest(args)
	case "expr":
		s.cmdExpr(args)
	case "seq":
		s.cmdSeq(args)
	case "env":
		s.cmdEnv(args)
	case "printenv":
		s.cmdPrintenv(args)
	case "date":
		s.cmdDate(args)
	case "clear":
		s.toConsole([]byte{'\x1b', '[', '2', 'J', '\x1b', '[', 'H'})
	case "reset":
		s.toConsole([]byte{'\x1b', '[', 'c'})
	case "whoami":
		s.out(s.userName())
	case "id":
		s.cmdId(args)
	case "vi":
		s.cmdVi(args)
	case "kill-session":
		s.cmdKill(args)
	case "sessions":
		s.cmdSessions()
	case "caps":
		s.cmdCaps(args)
	case "run":
		s.cmdRun(args)
	default:
		s.out("sh: unknown command: " + cmd)
	}
}

func (s *Shell) cmdPwd() {
	if s.root == "" {
		s.out("/")
	} else {
		s.out(s.root)
	}
}

func (s *Shell) cmdCd(args []string) {
	if s.fs == nil {
		s.out("fs unavailable")
		return
	}
	target := "/"
	if len(args) > 0 {
		target = args[0]
	}
	path := s.resolve(target)
	// must exist and be a directory
	info, err := s.fs.Stat(path)
	if err != nil {
		s.out("cd: " + err.Error())
		return
	}
	if !info.IsDir() {
		s.out("cd: not a directory: " + target)
		return
	}
	s.root = path
}

func (s *Shell) cmdMkdir(args []string) {
	if s.fs == nil || len(args) == 0 {
		s.out("usage: mkdir <dir>")
		return
	}
	p := s.resolve(args[0])
	if err := s.fs.Mkdir(p); err != nil {
		s.out("mkdir: " + err.Error())
		return
	}
}

func (s *Shell) cmdRm(args []string) {
	if s.fs == nil || len(args) == 0 {
		s.out("usage: rm <file|dir>")
		return
	}
	for _, a := range args {
		p := s.resolve(a)
		if err := s.fs.Delete(p); err != nil {
			s.out("rm: " + a + ": " + err.Error())
		}
	}
}

func (s *Shell) cmdTouch(args []string) {
	if s.fs == nil || len(args) == 0 {
		s.out("usage: touch <file>")
		return
	}
	for _, a := range args {
		p := s.resolve(a)
		if err := s.fs.Create(p); err != nil {
			s.out("touch: " + a + ": " + err.Error())
		}
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

// cmdSessions dumps the kernel registry's session table for auditing
// (abi/ABI.md §7 LIST). Lists every session's sid, uid, state, name.
func (s *Shell) cmdSessions() {
	if s.reg == nil {
		s.out("registry unavailable")
		return
	}
	list, err := s.reg.List()
	if err != nil {
		s.out("sessions: " + err.Error())
		return
	}
	if len(list) == 0 {
		s.out("(no sessions)")
		return
	}
	s.out("sid  uid   state name")
	for _, si := range list {
		st := "?"
		switch si.State {
		case lib.StateRunnable:
			st = "R"
		case lib.StateRunning:
			st = "R"
		case lib.StateZombie:
			st = "Z"
		case lib.StateFree:
			st = "."
		}
		s.out(strconv.FormatUint(uint64(si.Sid), 10) + "  " +
			strconv.FormatUint(uint64(si.UID), 10) + "  " +
			st + "     " + si.Name)
	}
}

// cmdCaps dumps one session's capability set for auditing
// (abi/ABI.md §7 CAPS {sid}). Lists each held bit by name.
func (s *Shell) cmdCaps(args []string) {
	if s.reg == nil || len(args) == 0 {
		s.out("usage: caps <sid>")
		return
	}
	sid, err := strconv.ParseUint(args[0], 10, 32)
	if err != nil {
		s.out("caps: bad sid")
		return
	}
	caps, err := s.reg.Caps(uint32(sid))
	if err != nil {
		s.out("caps: " + err.Error())
		return
	}
	if len(caps) == 0 {
		s.out(args[0] + ": (no capabilities)")
		return
	}
	var parts []string
	var mask uint64
	for _, c := range caps {
		parts = append(parts, lib.CapNames(c.Rights)...)
		mask |= c.Rights
	}
	s.out(args[0] + ": " + strings.Join(parts, " ") + " (0x" + strconv.FormatUint(mask, 16) + ")")
}
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

func (s *Shell) cmdRmdir(args []string) {
	if s.fs == nil || len(args) == 0 {
		s.out("usage: rmdir <dir>")
		return
	}
	for _, a := range args {
		p := s.resolve(a)
		if err := s.fs.Delete(p); err != nil {
			s.out("rmdir: " + a + ": " + err.Error())
		}
	}
}

func (s *Shell) cmdCp(args []string) {
	if s.fs == nil || len(args) < 2 {
		s.out("usage: cp <src> <dst>")
		return
	}
	src := s.resolve(args[0])
	dst := s.resolve(args[1])
	buf := make([]byte, 2048)
	var data []byte
	n, err := s.fs.ReadFile(src, 0, buf)
	if err != nil {
		s.out("cp: " + err.Error())
		return
	}
	data = append(data, buf[:n]...)
	off := uint64(n)
	for n == len(buf) {
		n, err = s.fs.ReadFile(src, off, buf)
		if err != nil {
			break
		}
		data = append(data, buf[:n]...)
		off += uint64(n)
	}
	if err := s.fs.Create(dst); err != nil {
		s.out("cp: create " + args[1] + ": " + err.Error())
		return
	}
	if _, err := s.fs.WriteFile(dst, 0, data); err != nil {
		s.out("cp: write " + args[1] + ": " + err.Error())
	}
}

func (s *Shell) cmdMv(args []string) {
	if s.fs == nil || len(args) < 2 {
		s.out("usage: mv <src> <dst>")
		return
	}
	src := s.resolve(args[0])
	dst := s.resolve(args[1])
	buf := make([]byte, 2048)
	var data []byte
	n, err := s.fs.ReadFile(src, 0, buf)
	if err != nil {
		s.out("mv: " + err.Error())
		return
	}
	data = append(data, buf[:n]...)
	off := uint64(n)
	for n == len(buf) {
		n, err = s.fs.ReadFile(src, off, buf)
		if err != nil {
			break
		}
		data = append(data, buf[:n]...)
		off += uint64(n)
	}
	if err := s.fs.Create(dst); err != nil {
		s.out("mv: create " + args[1] + ": " + err.Error())
		return
	}
	if _, err := s.fs.WriteFile(dst, 0, data); err != nil {
		s.out("mv: write " + args[1] + ": " + err.Error())
		return
	}
	if err := s.fs.Delete(src); err != nil {
		s.out("mv: delete " + args[0] + ": " + err.Error())
	}
}

func (s *Shell) cmdGrep(args []string) {
	if len(args) == 0 {
		s.out("usage: grep <pattern> [file]")
		return
	}
	pat := args[0]
	if len(args) < 2 {
		s.out("grep: stdin not supported in v1")
		return
	}
	path := s.resolve(args[1])
	data, err := s.readAll(path)
	if err != nil {
		s.out("grep: " + err.Error())
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for _, ln := range lines {
		if strings.Contains(ln, pat) {
			s.out(ln)
		}
	}
}

func (s *Shell) cmdFind(args []string) {
	if s.fs == nil || len(args) == 0 {
		s.out("usage: find <path>")
		return
	}
	root := s.resolve(args[0])
	if root == "." {
		root = "/"
	}
	root = strings.TrimSuffix(root, "/.")
	root = strings.TrimSuffix(root, "/./")
	s.findWalk(root, root)
}

func (s *Shell) findWalk(base, path string) {
	ents, err := s.fs.List(path)
	if err != nil {
		return
	}
	for _, e := range ents {
		full := path
		if !strings.HasSuffix(full, "/") {
			full += "/"
		}
		full += e.Name
		s.out(full)
		if e.IsDir() {
			s.findWalk(base, full)
		}
	}
}

func (s *Shell) cmdHead(args []string) {
	if len(args) < 2 {
		s.out("usage: head -n <N> <file>")
		return
	}
	n, err := strconv.Atoi(args[1])
	if err != nil {
		s.out("head: bad -n value")
		return
	}
	path := s.resolve(args[2])
	data, err := s.readAll(path)
	if err != nil {
		s.out("head: " + err.Error())
		return
	}
	lines := strings.Split(string(data), "\n")
	for i := 0; i < n && i < len(lines); i++ {
		if lines[i] != "" {
			s.out(lines[i])
		}
	}
}

func (s *Shell) cmdTail(args []string) {
	if len(args) < 2 {
		s.out("usage: tail -n <N> <file>")
		return
	}
	n, err := strconv.Atoi(args[1])
	if err != nil {
		s.out("tail: bad -n value")
		return
	}
	path := s.resolve(args[2])
	data, err := s.readAll(path)
	if err != nil {
		s.out("tail: " + err.Error())
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	start := len(lines) - n
	if start < 0 {
		start = 0
	}
	for i := start; i < len(lines); i++ {
		s.out(lines[i])
	}
}

func (s *Shell) cmdWc(args []string) {
	if len(args) == 0 {
		s.out("usage: wc <file>")
		return
	}
	path := s.resolve(args[0])
	buf, err := s.readAll(path)
	if err != nil {
		s.out("wc: " + err.Error())
		return
	}
	lines := strings.Count(string(buf), "\n")
	words := len(strings.Fields(string(buf)))
	s.out(strconv.Itoa(lines) + " " + strconv.Itoa(words) + " " + strconv.Itoa(len(buf)))
}

func (s *Shell) cmdSort(args []string) {
	if len(args) == 0 {
		s.out("usage: sort <file>")
		return
	}
	path := s.resolve(args[0])
	buf, err := s.readAll(path)
	if err != nil {
		s.out("sort: " + err.Error())
		return
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	sort.Strings(lines)
	for _, ln := range lines {
		s.out(ln)
	}
}

func (s *Shell) cmdUniq(args []string) {
	if len(args) == 0 {
		s.out("usage: uniq <file>")
		return
	}
	path := s.resolve(args[0])
	buf, err := s.readAll(path)
	if err != nil {
		s.out("uniq: " + err.Error())
		return
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	var prev string
	for _, ln := range lines {
		if ln != prev {
			s.out(ln)
		}
		prev = ln
	}
}

func (s *Shell) cmdTr(args []string) {
	if len(args) < 2 {
		s.out("usage: tr <set1> <set2> [file]")
		return
	}
	set1 := args[0]
	set2 := args[1]
	if len(args) > 2 {
		path := s.resolve(args[2])
		buf, err := s.readAll(path)
		if err != nil {
			s.out("tr: " + err.Error())
			return
		}
		text := string(buf)
		mapping := make(map[rune]rune)
		for i, r := range set1 {
			if i < len(set2) {
				mapping[r] = rune(set2[i])
			} else {
				mapping[r] = rune(set2[len(set2)-1])
			}
		}
		out := strings.Map(func(r rune) rune {
			if repl, ok := mapping[r]; ok {
				return repl
			}
			return r
		}, text)
		for _, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if ln != "" {
				s.out(ln)
			}
		}
	} else {
		s.out("tr: stdin not supported in v1")
	}
}

func (s *Shell) cmdCut(args []string) {
	if len(args) < 2 {
		s.out("usage: cut -d<sep> -f<fields> [file]")
		return
	}
	delim := " "
	fields := []int{1}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-d") && len(a) > 2 {
			delim = string(a[2])
		} else if strings.HasPrefix(a, "-f") {
			fpart := a[2:]
			fds := strings.Split(fpart, ",")
			fields = fields[:0]
			for _, fd := range fds {
				n, err := strconv.Atoi(fd)
				if err == nil {
					fields = append(fields, n)
				}
			}
		}
	}
	if len(args) < len(fields) {
		s.out("cut: no file specified")
		return
	}
	// find the file argument (last non-flag arg)
	var path string
	for i := len(args) - 1; i >= 0; i-- {
		if !strings.HasPrefix(args[i], "-") {
			path = args[i]
			break
		}
	}
 	if path == "" {
		s.out("cut: no file specified")
		return
	}
	full := s.resolve(path)
	buf, err := s.readAll(full)
	if err != nil {
		s.out("cut: " + err.Error())
		return
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	for _, ln := range lines {
		parts := strings.Split(ln, delim)
		var selected []string
		for _, f := range fields {
			if f > 0 && f <= len(parts) {
				selected = append(selected, parts[f-1])
			}
		}
		s.out(strings.Join(selected, delim))
	}
}

func (s *Shell) cmdSed(args []string) {
	if len(args) < 2 {
		s.out("usage: sed <script> <file>")
		return
	}
	script := args[0]
	var subs []sedSub
	for _, part := range strings.Split(script, ";") {
		parts := strings.SplitN(part, "/", 4)
		if len(parts) >= 4 && parts[0] == "s" {
			subs = append(subs, sedSub{old: parts[1], repl: parts[2], all: len(parts) > 3 && parts[3] == "g"})
		}
	}
	if len(subs) == 0 {
		s.out("sed: only s/old/new/[] supported in v1")
		return
	}
	path := s.resolve(args[1])
	buf, err := s.readAll(path)
	if err != nil {
		s.out("sed: " + err.Error())
		return
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	for _, ln := range lines {
		out := ln
		for _, sub := range subs {
			if sub.all {
				out = strings.ReplaceAll(out, sub.old, sub.repl)
			} else {
				out = strings.Replace(out, sub.old, sub.repl, 1)
			}
		}
		s.out(out)
	}
}

type sedSub struct {
	old  string
	repl string
	all  bool
}

func (s *Shell) cmdSleep(args []string) {
	if len(args) == 0 {
		s.out("usage: sleep <seconds>")
		return
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		s.out("sleep: bad duration")
		return
	}
	if s.k.HasClock() {
		start := s.k.ClockMs()
		for s.k.ClockMs()-start < uint64(n)*1000 {
			s.k.Yield()
		}
	} else {
		time.Sleep(time.Duration(n) * time.Second)
	}
}

func (s *Shell) cmdTest(args []string) {
	// Minimal: test -n str, test -z str, test -f file, test -d file, test str = str
	s.exitStatus = 0
	if len(args) == 0 {
		s.exitStatus = 1
		return
	}
	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "-n":
			if i+1 >= len(args) || len(args[i+1]) == 0 {
				s.exitStatus = 1
			}
			i += 2
		case "-z":
			if i+1 < len(args) && len(args[i+1]) == 0 {
				s.exitStatus = 0
			} else {
				s.exitStatus = 1
			}
			i += 2
		case "-f":
			if i+1 >= len(args) {
				s.exitStatus = 1
				return
			}
			st, err := s.fs.Stat(s.resolve(args[i+1]))
			if err != nil || st.IsDir() {
				s.exitStatus = 1
			}
			i += 2
		case "-d":
			if i+1 >= len(args) {
				s.exitStatus = 1
				return
			}
			st, err := s.fs.Stat(s.resolve(args[i+1]))
			if err != nil || !st.IsDir() {
				s.exitStatus = 1
			}
			i += 2
		case "=":
			if i < 1 || i+1 >= len(args) {
				s.exitStatus = 1
				return
			}
			left := args[i-1]
			right := args[i+1]
			if left != right {
				s.exitStatus = 1
			}
			i += 2
		default:
			s.exitStatus = 1
			return
		}
	}
}

func (s *Shell) cmdExpr(args []string) {
	// Simple: expr <num> + <num>, expr <num> - <num>, expr <num> = <num>
	if len(args) < 3 {
		s.out("usage: expr <num> +|-|= <num>")
		return
	}
	left, err := strconv.Atoi(args[0])
	if err != nil {
		s.out("expr: not a number")
		return
	}
	op := args[1]
	right, err := strconv.Atoi(args[2])
	if err != nil {
		s.out("expr: not a number")
		return
	}
	switch op {
	case "+":
		s.out(strconv.Itoa(left + right))
	case "-":
		s.out(strconv.Itoa(left - right))
	case "=":
		if left == right {
			s.out("1")
		} else {
			s.out("0")
		}
		s.exitStatus = 0
		if left != right {
			s.exitStatus = 1
		}
	default:
		s.out("expr: unknown op " + op)
	}
}

func (s *Shell) cmdSeq(args []string) {
	if len(args) < 1 {
		s.out("usage: seq <last>")
		return
	}
	if len(args) == 1 {
		last, err := strconv.Atoi(args[0])
		if err != nil {
			s.out("seq: bad number")
			return
		}
		for i := 1; i <= last; i++ {
			s.out(strconv.Itoa(i))
		}
	} else {
		start, err := strconv.Atoi(args[0])
		step := 1
		last := start
		if len(args) == 2 {
			last, err = strconv.Atoi(args[1])
		} else if len(args) > 2 {
			step, err = strconv.Atoi(args[1])
			last, err = strconv.Atoi(args[2])
		}
		if err != nil {
			s.out("seq: bad number")
			return
		}
		if step == 0 {
			return
		}
		if step > 0 {
			for i := start; i <= last; i += step {
				s.out(strconv.Itoa(i))
			}
		} else {
			for i := start; i >= last; i += step {
				s.out(strconv.Itoa(i))
			}
		}
	}
}

func (s *Shell) cmdEnv(args []string) {
	for e := range s.env {
		s.out(e + "=" + s.env[e])
	}
}

func (s *Shell) cmdPrintenv(args []string) {
	if len(args) == 0 {
		s.cmdEnv(nil)
		return
	}
	for e := range s.env {
		if e == args[0] {
			s.out(s.env[e])
			return
		}
	}
	s.exitStatus = 1
}

func (s *Shell) cmdDate(args []string) {
	if s.k.HasClock() {
		ms := s.k.ClockMs()
		t := time.Unix(int64(ms/1000), 0).UTC()
		s.out(t.Format("Mon Jan 2 15:04:05 UTC 2006"))
	} else {
		s.out(time.Now().UTC().Format("Mon Jan 2 15:04:05 UTC 2006"))
	}
}

func (s *Shell) cmdId(args []string) {
	uid := s.uid
	if s.reg != nil {
		if list, err := s.reg.List(); err == nil {
			for _, si := range list {
				if si.Name == lib.NameShell && lib.Alive(si.State) {
					uid = si.UID
				}
			}
		}
	}
	s.out("uid=" + strconv.FormatUint(uint64(uid), 10) + " (" + lib.Username(uid) + ")")
}
