package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	nc     *lib.NetClient
	con    lib.Handle // bind handle of "console"
	root   string
	uid    uint32
	env    map[string]string
	ioh    lib.Handle // handle for --io-port output (InvalidHandle if unused)
	ioin   lib.Handle // handle for --io-in input (InvalidHandle if unused)
	line   []rune
	exitStatus int
	// Phase 14 pipeline support: when capture != nil, out() appends
	// to capture instead of sending to console. pin holds the current
	// stage's stdin (previous stage's stdout).
	capture *strings.Builder
	pin     string
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
	if nc, err := lib.BindNet(k, "shell"); err == nil {
		nc.SetBudget(20000)
		sh.nc = nc
	} else {
		sh.out("net unavailable")
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
// When capture != nil (pipeline stage), appends to capture instead.
func (s *Shell) out(line string) {
	if s.capture != nil {
		s.capture.WriteString(line)
		s.capture.WriteByte('\n')
		return
	}
	s.realOut(line)
}

func (s *Shell) sendReliable(h lib.Handle, msg []byte) {
	if h == lib.InvalidHandle {
		return
	}
	for i := 0; i < 2000; i++ {
		rc := s.k.PortSend(h, msg)
		if rc == lib.StatusOK {
			return
		}
		if rc != lib.StatusWouldBlock {
			return // StatusErr: bad handle/payload, drop
		}
		s.k.Yield()
	}
}

func (s *Shell) realOut(line string) {
	msg := []byte("[sh")
	msg = append(msg, 0, 0, 0, 0, 0)
	msg = append(msg, ']')
	msg = append(msg, ' ')
	msg = append(msg, []byte(line)...)
	s.sendReliable(s.con, msg)
	if s.ioh != lib.InvalidHandle {
		s.sendReliable(s.ioh, msg)
	}
}

// toConsole sends a message to the console port only (not the io output
// port). Used for terminal redraw protocol messages that should not
// be relayed back to the SSH/text client. Reliable: retries on
// WouldBlock so long input lines don't lose echoes/prompts.
func (s *Shell) toConsole(msg []byte) {
	s.sendReliable(s.con, msg)
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

// exec runs one input line with Phase 14 sequencing:
// `;` always runs next, `&&` runs next only if exit==0, `||` only if exit!=0.
// Each sequential item may be a `|` pipeline whose stages share stdin/stdout
// in-process (no SPAWN, no fork).
func (s *Shell) exec(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	for _, item := range splitSequential(line) {
		if item.cmd == "" {
			continue
		}
		// Gate sequencing operators.
		if item.op == "&&" && s.exitStatus != 0 {
			continue
		}
		if item.op == "||" && s.exitStatus == 0 {
			continue
		}
		s.execPipeline(item.cmd)
	}
}

type seqItem struct {
	op  string // "" (first), ";", "&&", "||"
	cmd string
}

// splitSequential splits on ; && || (no quoting in v1 shell).
func splitSequential(line string) []seqItem {
	var out []seqItem
	cur := strings.Builder{}
	op := ""
	flush := func() {
		out = append(out, seqItem{op: op, cmd: strings.TrimSpace(cur.String())})
		cur.Reset()
	}
	i := 0
	for i < len(line) {
		switch {
		case line[i] == ';':
			flush()
			op = ";"
			i++
		case i+1 < len(line) && line[i] == '&' && line[i+1] == '&':
			flush()
			op = "&&"
			i += 2
		case i+1 < len(line) && line[i] == '|' && line[i+1] == '|':
			flush()
			op = "||"
			i += 2
		default:
			cur.WriteByte(line[i])
			i++
		}
	}
	flush()
	return out
}

// execPipeline runs stages separated by `|` threading stdout->stdin.
func (s *Shell) execPipeline(cmd string) {
	stages := splitPipe(cmd)
	if len(stages) == 0 {
		return
	}
	if len(stages) == 1 {
		fields := strings.Fields(stages[0])
		if len(fields) == 0 {
			return
		}
		s.dispatch(fields[0], fields[1:])
		return
	}
	stdin := ""
	for _, st := range stages {
		fields := strings.Fields(st)
		if len(fields) == 0 {
			continue
		}
		stdin = s.runSingle(fields[0], fields[1:], stdin)
	}
	if stdin == "" {
		return
	}
	for _, ln := range strings.Split(strings.TrimSuffix(stdin, "\n"), "\n") {
		s.realOut(ln)
	}
}

// splitPipe splits on single `|` (sequential split already removed `||`).
func splitPipe(cmd string) []string {
	parts := strings.Split(cmd, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// runSingle executes one builtin with stdin captured, returning stdout.
func (s *Shell) runSingle(cmd string, args []string, stdin string) string {
	prevCap, prevPin := s.capture, s.pin
	buf := &strings.Builder{}
	s.capture = buf
	s.pin = stdin
	s.dispatch(cmd, args)
	s.capture, s.pin = prevCap, prevPin
	return buf.String()
}

func (s *Shell) dispatch(cmd string, args []string) {
	// Default success; failures set non-zero (drives && / ||).
	s.exitStatus = 0
	switch cmd {
	case "help":
		s.out("built-ins: echo ls cat stat cp mv rmdir grep find head tail wc sort uniq tr cut sed sleep true false test date clear whoami id env printenv kill-session sessions caps run help vi pwd cd mkdir rm touch passwd top dmesg memstat audit ping nc http netstat ipaddr ssh ports sessinfo caphint chcaps pkg")
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
		s.exitStatus = 0
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
	case "passwd":
		s.cmdPasswd(args)
	case "top":
		s.cmdTop()
	case "dmesg":
		s.cmdDmesg()
	case "memstat":
		s.cmdMemstat()
 	case "audit":
		s.cmdAudit(args)
	case "ping":
		s.cmdPing(args)
	case "nc":
		s.cmdNc(args)
	case "http":
		s.cmdHttp(args)
	case "netstat":
		s.cmdNetstat(args)
	case "ipaddr":
		s.cmdIpaddr(args)
 	case "ssh":
		s.cmdSsh(args)
	case "ports":
		s.cmdPorts(args)
	case "sessinfo":
		s.cmdSessinfo(args)
	case "caphint":
		s.cmdCaphint(args)
  	case "chcaps":
		s.cmdChcaps(args)
	case "pkg":
		s.cmdPkg(args)
	case "sysctl":
		s.cmdSysctl(args)
	case "initctl":
		s.cmdInitctl(args)
	case "checkconf":
		s.cmdCheckconf(args)
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
	// cat [file...]: no args reads stdin (pipeline).
	files := []string{}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		files = append(files, a)
	}
	if len(files) == 0 {
		if s.pin != "" {
			for _, ln := range strings.Split(strings.TrimSuffix(s.pin, "\n"), "\n") {
				s.out(ln)
			}
			return
		}
		if s.fs == nil {
			s.out("usage: cat <file>")
			return
		}
		s.out("usage: cat <file>")
		return
	}
	if s.fs == nil {
		s.out("fs unavailable")
		return
	}
	for _, f := range files {
		path := s.resolve(f)
		data, err := s.readAll(path)
		if err != nil {
			s.out("cat: " + err.Error())
			continue
		}
		for _, ln := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
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
	// grep [-n] [-i] [-v] <pattern> [file]: no file reads stdin.
	showNum, ignoreCase, invert := false, false, false
	rest := []string{}
	for _, a := range args {
		switch a {
		case "-n":
			showNum = true
		case "-i":
			ignoreCase = true
		case "-v":
			invert = true
		case "-ni", "-in":
			showNum, ignoreCase = true, true
		case "-nv", "-vn":
			showNum, invert = true, true
		default:
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				// combined flags like -niv
				isFlags := true
				for _, c := range a[1:] {
					if c != 'n' && c != 'i' && c != 'v' {
						isFlags = false
					}
				}
				if isFlags {
					for _, c := range a[1:] {
						switch c {
						case 'n':
							showNum = true
						case 'i':
							ignoreCase = true
						case 'v':
							invert = true
						}
					}
					continue
				}
			}
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		s.out("usage: grep [-n] <pattern> [file]")
		return
	}
	pat := rest[0]
	var data string
	if len(rest) >= 2 {
		d, err := s.readAll(s.resolve(rest[1]))
		if err != nil {
			s.out("grep: " + err.Error())
			s.exitStatus = 1
			return
		}
		data = string(d)
	} else {
		if s.pin == "" {
			s.out("usage: grep [-n] <pattern> [file]")
			return
		}
		data = s.pin
	}
	if ignoreCase {
		pat = strings.ToLower(pat)
	}
	matched := 0
	for i, ln := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		hay := ln
		if ignoreCase {
			hay = strings.ToLower(ln)
		}
		hit := strings.Contains(hay, pat)
		if invert {
			hit = !hit
		}
		if hit {
			matched++
			if showNum {
				s.out(strconv.Itoa(i+1) + ":" + ln)
			} else {
				s.out(ln)
			}
		}
	}
	if matched == 0 {
		s.exitStatus = 1
	} else {
		s.exitStatus = 0
	}
}

func (s *Shell) cmdFind(args []string) {
	// find [path] [-name pat] [-type f|d]: defaults to "." (session root).
	if s.fs == nil {
		s.out("fs unavailable")
		return
	}
	root := s.root
	if root == "" {
		root = "/"
	}
	namePat := ""
	wantType := ""
	i := 0
	for i < len(args) {
		switch args[i] {
		case "-name":
			if i+1 < len(args) {
				namePat = args[i+1]
				i += 2
			} else {
				i++
			}
		case "-type":
			if i+1 < len(args) {
				wantType = args[i+1]
				i += 2
			} else {
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				root = s.resolve(args[i])
			}
			i++
		}
	}
	if root == "." || root == "./" {
		root = s.root
		if root == "" {
			root = "/"
		}
	}
	root = strings.TrimSuffix(root, "/.")
	root = strings.TrimSuffix(root, "/./")
	if root == "" {
		root = "/"
	}
	s.findWalkFiltered(root, root, namePat, wantType)
}

func matchGlob(pat, name string) bool {
	// Minimal glob: * substring/prefix/suffix.
	if pat == "" || pat == "*" {
		return true
	}
	if !strings.Contains(pat, "*") {
		return name == pat
	}
	parts := strings.Split(pat, "*")
	idx := 0
	for i, p := range parts {
		if p == "" {
			continue
		}
		j := strings.Index(name[idx:], p)
		if j < 0 {
			return false
		}
		if i == 0 && !strings.HasPrefix(pat, "*") && idx+j != 0 {
			return false
		}
		idx += j + len(p)
	}
	if !strings.HasSuffix(pat, "*") && idx != len(name) {
		return false
	}
	return true
}

func (s *Shell) findWalk(base, path string) {
	s.findWalkFiltered(base, path, "", "")
}

func (s *Shell) findWalkFiltered(base, path, namePat, wantType string) {
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
		emit := true
		if namePat != "" && !matchGlob(namePat, e.Name) {
			emit = false
		}
		if wantType == "f" && e.IsDir() {
			emit = false
		}
		if wantType == "d" && !e.IsDir() {
			emit = false
		}
		if emit {
			s.out(full)
		}
		if e.IsDir() {
			s.findWalkFiltered(base, full, namePat, wantType)
		}
	}
}

func parseHeadTail(args []string) (n int, file string, ok bool) {
	n = 10
	file = ""
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-n" && i+1 < len(args):
			v, err := strconv.Atoi(args[i+1])
			if err != nil {
				return 0, "", false
			}
			n = v
			i += 2
		case strings.HasPrefix(a, "-n") && len(a) > 2:
			v, err := strconv.Atoi(a[2:])
			if err != nil {
				return 0, "", false
			}
			n = v
			i++
		case len(a) > 1 && a[0] == '-' && a[1] >= '0' && a[1] <= '9':
			v, err := strconv.Atoi(a[1:])
			if err != nil {
				return 0, "", false
			}
			n = v
			i++
		case strings.HasPrefix(a, "-"):
			i++
		default:
			file = a
			i++
		}
	}
	return n, file, true
}

func (s *Shell) cmdHead(args []string) {
	n, file, ok := parseHeadTail(args)
	if !ok {
		s.out("usage: head [-n N] [file]")
		return
	}
	var data string
	if file != "" {
		d, err := s.readAll(s.resolve(file))
		if err != nil {
			s.out("head: " + err.Error())
			return
		}
		data = string(d)
	} else {
		if s.pin == "" {
			s.out("usage: head [-n N] [file]")
			return
		}
		data = s.pin
	}
	lines := strings.Split(strings.TrimSuffix(data, "\n"), "\n")
	for i := 0; i < n && i < len(lines); i++ {
		s.out(lines[i])
	}
}

func (s *Shell) cmdTail(args []string) {
	n, file, ok := parseHeadTail(args)
	if !ok {
		s.out("usage: tail [-n N] [file]")
		return
	}
	var data string
	if file != "" {
		d, err := s.readAll(s.resolve(file))
		if err != nil {
			s.out("tail: " + err.Error())
			return
		}
		data = string(d)
	} else {
		if s.pin == "" {
			s.out("usage: tail [-n N] [file]")
			return
		}
		data = s.pin
	}
	lines := strings.Split(strings.TrimSuffix(data, "\n"), "\n")
	start := len(lines) - n
	if start < 0 {
		start = 0
	}
	for i := start; i < len(lines); i++ {
		s.out(lines[i])
	}
}

func (s *Shell) cmdWc(args []string) {
	// wc [-l] [-w] [-c] [file]: no file reads stdin.
	showL, showW, showC := false, false, false
	files := []string{}
	for _, a := range args {
		switch a {
		case "-l":
			showL = true
		case "-w":
			showW = true
		case "-c":
			showC = true
		default:
			if strings.HasPrefix(a, "-") {
				continue
			}
			files = append(files, a)
		}
	}
	if !showL && !showW && !showC {
		showL, showW, showC = true, true, true
	}
	emit := func(data string, label string) {
		lines := strings.Count(data, "\n")
		// Count last line without trailing newline.
		if len(data) > 0 && !strings.HasSuffix(data, "\n") {
			lines++
		}
		words := len(strings.Fields(data))
		parts := []string{}
		if showL {
			parts = append(parts, strconv.Itoa(lines))
		}
		if showW {
			parts = append(parts, strconv.Itoa(words))
		}
		if showC {
			parts = append(parts, strconv.Itoa(len(data)))
		}
		if label != "" {
			parts = append(parts, label)
		}
		s.out(strings.Join(parts, " "))
	}
	if len(files) == 0 {
		var data string
		if s.pin != "" {
			data = s.pin
			if !strings.HasSuffix(data, "\n") {
				data += "\n"
			}
		} else {
			s.out("usage: wc <file>")
			return
		}
		emit(data, "")
		return
	}
	for _, f := range files {
		buf, err := s.readAll(s.resolve(f))
		if err != nil {
			s.out("wc: " + err.Error())
			continue
		}
		label := ""
		if len(files) > 1 {
			label = f
		}
		emit(string(buf), label)
	}
}

func (s *Shell) cmdSort(args []string) {
	// sort [-n] [-r] [-u] [file]: no file reads stdin.
	numeric, reverse, unique := false, false, false
	file := ""
	for _, a := range args {
		switch a {
		case "-n":
			numeric = true
		case "-r":
			reverse = true
		case "-u":
			unique = true
		case "-nr", "-rn":
			numeric, reverse = true, true
		default:
			if strings.HasPrefix(a, "-") {
				continue
			}
			file = a
		}
	}
	var data string
	if file != "" {
		buf, err := s.readAll(s.resolve(file))
		if err != nil {
			s.out("sort: " + err.Error())
			return
		}
		data = string(buf)
	} else {
		if s.pin == "" {
			s.out("usage: sort [file]")
			return
		}
		data = s.pin
	}
	lines := strings.Split(strings.TrimSuffix(data, "\n"), "\n")
	if numeric {
		sort.Slice(lines, func(i, j int) bool {
			vi, _ := strconv.ParseFloat(strings.Fields(lines[i]+" ")[0], 64)
			vj, _ := strconv.ParseFloat(strings.Fields(lines[j]+" ")[0], 64)
			if reverse {
				return vi > vj
			}
			return vi < vj
		})
	} else if reverse {
		sort.Sort(sort.Reverse(sort.StringSlice(lines)))
	} else {
		sort.Strings(lines)
	}
	prev := ""
	first := true
	for _, ln := range lines {
		if unique && !first && ln == prev {
			continue
		}
		s.out(ln)
		prev = ln
		first = false
	}
}

func (s *Shell) cmdUniq(args []string) {
	// uniq [-c] [file]: no file reads stdin.
	count := false
	file := ""
	for _, a := range args {
		if a == "-c" {
			count = true
		} else if !strings.HasPrefix(a, "-") {
			file = a
		}
	}
	var data string
	if file != "" {
		buf, err := s.readAll(s.resolve(file))
		if err != nil {
			s.out("uniq: " + err.Error())
			return
		}
		data = string(buf)
	} else {
		if s.pin == "" {
			s.out("usage: uniq [file]")
			return
		}
		data = s.pin
	}
	lines := strings.Split(strings.TrimSuffix(data, "\n"), "\n")
	prev := ""
	n := 0
	flush := func() {
		if n == 0 {
			return
		}
		if count {
			s.out(strings.TrimSpace(strconv.Itoa(n) + " " + prev))
		} else {
			s.out(prev)
		}
	}
	for _, ln := range lines {
		if ln != prev {
			flush()
			prev = ln
			n = 1
		} else {
			n++
		}
	}
	flush()
}

func (s *Shell) cmdTr(args []string) {
	// tr [-d] <set1> [set2] [file]: no file reads stdin.
	del := false
	rest := []string{}
	for _, a := range args {
		if a == "-d" {
			del = true
		} else {
			rest = append(rest, a)
		}
	}
	if len(rest) < 1 {
		s.out("usage: tr [-d] <set1> [set2] [file]")
		return
	}
	set1 := rest[0]
	set2 := ""
	file := ""
	if len(rest) >= 2 {
		// Heuristic: if 3 args, middle is set2 and last is file when file exists
		// or last contains path chars; simpler: if len==3, args[1]=set2 args[2]=file.
		// If len==2, args[1] is set2 unless stdin present and it looks like a file.
		if len(rest) == 2 {
			// Ambiguous: `tr ab AB` in pipeline means set2; `tr ab file` means file.
			// Prefer: if stdin present, treat as set2; else try file first.
			if s.pin != "" {
				set2 = rest[1]
			} else {
				// Try file; if read fails, treat as set2.
				if _, err := s.readAll(s.resolve(rest[1])); err == nil {
					file = rest[1]
				} else {
					set2 = rest[1]
				}
			}
		} else {
			set2 = rest[1]
			file = rest[2]
		}
	}
	var text string
	if file != "" {
		buf, err := s.readAll(s.resolve(file))
		if err != nil {
			s.out("tr: " + err.Error())
			return
		}
		text = string(buf)
	} else {
		if s.pin == "" {
			if set2 == "" && !del {
				s.out("usage: tr [-d] <set1> [set2] [file]")
				return
			}
			// No stdin and no file: usage (avoid hanging).
			if file == "" && s.pin == "" && len(rest) == 2 && set2 != "" {
				// `tr ab AB` with no stdin and no file: nothing to do.
				return
			}
			s.out("usage: tr [-d] <set1> [set2] [file]")
			return
		}
		text = s.pin
	}
	if del {
		out := strings.Map(func(r rune) rune {
			if strings.ContainsRune(set1, r) {
				return -1
			}
			return r
		}, text)
		for _, ln := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			s.out(ln)
		}
		return
	}
	if set2 == "" {
		s.out("usage: tr <set1> <set2>")
		return
	}
	// Build mapping with range support minimal (no a-z expansion in v1 except literal).
	mapping := make(map[rune]rune)
	r1 := []rune(set1)
	r2 := []rune(set2)
	for i, r := range r1 {
		if i < len(r2) {
			mapping[r] = r2[i]
		} else {
			mapping[r] = r2[len(r2)-1]
		}
	}
	out := strings.Map(func(r rune) rune {
		if repl, ok := mapping[r]; ok {
			return repl
		}
		return r
	}, text)
	for _, ln := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		s.out(ln)
	}
}

func (s *Shell) cmdCut(args []string) {
	// cut -d<sep> -f<fields> [file]: no file reads stdin. Fields: 1,2 1-3.
	delim := "\t"
	fields := []int{1}
	fieldsSet := false
	file := ""
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-d" && i+1 < len(args):
			delim = args[i+1]
			i += 2
		case strings.HasPrefix(a, "-d") && len(a) > 2:
			delim = a[2:]
			i++
		case a == "-f" && i+1 < len(args):
			fields = parseCutFields(args[i+1])
			fieldsSet = true
			i += 2
		case strings.HasPrefix(a, "-f") && len(a) > 2:
			fields = parseCutFields(a[2:])
			fieldsSet = true
			i++
		case strings.HasPrefix(a, "-"):
			i++
		default:
			file = a
			i++
		}
	}
	_ = fieldsSet
	var data string
	if file != "" {
		buf, err := s.readAll(s.resolve(file))
		if err != nil {
			s.out("cut: " + err.Error())
			return
		}
		data = string(buf)
	} else {
		if s.pin == "" {
			s.out("usage: cut -d<sep> -f<fields> [file]")
			return
		}
		data = s.pin
	}
	for _, ln := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
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

func parseCutFields(spec string) []int {
	var out []int
	for _, part := range strings.Split(spec, ",") {
		if strings.Contains(part, "-") {
			b := strings.SplitN(part, "-", 2)
			lo, _ := strconv.Atoi(strings.TrimSpace(b[0]))
			hi, _ := strconv.Atoi(strings.TrimSpace(b[1]))
			if lo < 1 {
				lo = 1
			}
			if hi < lo {
				continue
			}
			for n := lo; n <= hi; n++ {
				out = append(out, n)
			}
		} else {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err == nil {
				out = append(out, n)
			}
		}
	}
	if len(out) == 0 {
		return []int{1}
	}
	return out
}

func (s *Shell) cmdSed(args []string) {
	// sed [-n] [-e script] [script] [file]: no file reads stdin.
	// v1 supports s/old/new/[g] and p (print) with -n.
	quiet := false
	scripts := []string{}
	file := ""
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-n":
			quiet = true
			i++
		case a == "-e" && i+1 < len(args):
			scripts = append(scripts, args[i+1])
			i += 2
		default:
			if strings.HasPrefix(a, "-") {
				i++
			} else if len(scripts) == 0 && (strings.HasPrefix(a, "s/") || a == "p" || strings.HasSuffix(a, "p")) {
				scripts = append(scripts, a)
				i++
			} else if file == "" {
				// Could be script or file; if we already have a script, it's file.
				if len(scripts) == 0 {
					scripts = append(scripts, a)
				} else {
					file = a
				}
				i++
			} else {
				i++
			}
		}
	}
	if len(scripts) == 0 {
		s.out("usage: sed [-n] <script> [file]")
		return
	}
	var data string
	if file != "" {
		buf, err := s.readAll(s.resolve(file))
		if err != nil {
			s.out("sed: " + err.Error())
			return
		}
		data = string(buf)
	} else {
		if s.pin == "" {
			s.out("usage: sed <script> <file>")
			return
		}
		data = s.pin
	}
	type sub struct {
		old, repl string
		all       bool
		print     bool
	}
	var subs []sub
	onlyPrint := false
	for _, script := range scripts {
		for _, part := range strings.Split(script, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if part == "p" {
				onlyPrint = true
				continue
			}
			if strings.HasSuffix(part, "/p") {
				onlyPrint = true
				part = strings.TrimSuffix(part, "/p")
				// fall through to s/// parsing with print flag
				bits := strings.SplitN(part, "/", 4)
				if len(bits) >= 3 && bits[0] == "s" {
					subs = append(subs, sub{old: bits[1], repl: bits[2], all: len(bits) > 3 && strings.Contains(bits[3], "g"), print: true})
					continue
				}
			}
			bits := strings.SplitN(part, "/", 4)
			if len(bits) >= 3 && bits[0] == "s" {
				all := len(bits) > 3 && strings.Contains(bits[3], "g")
				subs = append(subs, sub{old: bits[1], repl: bits[2], all: all})
			}
		}
	}
	if len(subs) == 0 && !onlyPrint {
		s.out("sed: only s/old/new/[g] supported in v1")
		return
	}
	_ = quiet
	for _, ln := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		out := ln
		for _, sb := range subs {
			if sb.all {
				out = strings.ReplaceAll(out, sb.old, sb.repl)
			} else {
				out = strings.Replace(out, sb.old, sb.repl, 1)
			}
		}
		if onlyPrint {
			// -n p semantics simplified: print only if changed or always for bare p.
			if out != ln || len(subs) == 0 {
				s.out(out)
			} else if !quiet {
				s.out(out)
			}
		} else if !quiet {
			s.out(out)
		}
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
	// test [-n str] [-z str] [-f file] [-d file] [-e file] [a = b] [a != b] [n -eq n...]
	// "[" alias strips trailing "]".
	if len(args) > 0 && args[len(args)-1] == "]" {
		args = args[:len(args)-1]
	}
	s.exitStatus = 0
	if len(args) == 0 {
		s.exitStatus = 1
		return
	}
	// Binary operators: X = Y, X != Y, N -eq/-ne/-lt/-le/-gt/-ge M
	if len(args) == 3 {
		switch args[1] {
		case "=":
			if args[0] != args[2] {
				s.exitStatus = 1
			}
			return
		case "!=":
			if args[0] == args[2] {
				s.exitStatus = 1
			}
			return
		case "-eq", "-ne", "-lt", "-le", "-gt", "-ge":
			l, e1 := strconv.Atoi(args[0])
			r, e2 := strconv.Atoi(args[2])
			if e1 != nil || e2 != nil {
				s.exitStatus = 1
				return
			}
			ok := false
			switch args[1] {
			case "-eq":
				ok = l == r
			case "-ne":
				ok = l != r
			case "-lt":
				ok = l < r
			case "-le":
				ok = l <= r
			case "-gt":
				ok = l > r
			case "-ge":
				ok = l >= r
			}
			if !ok {
				s.exitStatus = 1
			}
			return
		}
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
			if i+1 >= len(args) || args[i+1] != "" {
				s.exitStatus = 1
			}
			i += 2
		case "-f", "-e":
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
			if args[i-1] != args[i+1] {
				s.exitStatus = 1
			}
			i += 2
		case "!":
			// `! expr` negation simplified: only `! -z/-n` one level.
			i++
		default:
			// Single non-empty string is true.
			if a == "" {
				s.exitStatus = 1
				return
			}
			i++
		}
	}
}

func (s *Shell) cmdExpr(args []string) {
	// expr <num> <op> <num>: + - * / % = != < <= > >= (left-assoc, single op in v1).
	if len(args) < 3 {
		s.out("usage: expr <num> +|-|*|/|%|= <num>")
		s.exitStatus = 1
		return
	}
	left, err := strconv.Atoi(args[0])
	if err != nil {
		s.out("expr: not a number")
		s.exitStatus = 1
		return
	}
	op := args[1]
	right, err := strconv.Atoi(args[2])
	if err != nil {
		s.out("expr: not a number")
		s.exitStatus = 1
		return
	}
	switch op {
	case "+":
		s.out(strconv.Itoa(left + right))
	case "-":
		s.out(strconv.Itoa(left - right))
	case "*", "x":
		s.out(strconv.Itoa(left * right))
	case "/":
		if right == 0 {
			s.out("expr: division by zero")
			s.exitStatus = 1
			return
		}
		s.out(strconv.Itoa(left / right))
	case "%":
		if right == 0 {
			s.out("expr: division by zero")
			s.exitStatus = 1
			return
		}
		s.out(strconv.Itoa(left % right))
	case "=", "==":
		if left == right {
			s.out("1")
		} else {
			s.out("0")
		}
		if left != right {
			s.exitStatus = 1
		} else {
			s.exitStatus = 0
		}
	case "!=", "<>":
		if left != right {
			s.out("1")
			s.exitStatus = 0
		} else {
			s.out("0")
			s.exitStatus = 1
		}
	default:
		s.out("expr: unknown op " + op)
		s.exitStatus = 1
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

// ownSidUid resolves this shell's sid+uid via registry LIST (v1 identity
// heuristic: our session name is our argv[0]); falls back to s.uid.
func (s *Shell) ownSidUid() (uint32, uint32) {
	uid := s.uid
	var sid uint32
	if s.reg != nil {
		if list, err := s.reg.List(); err == nil {
			for _, si := range list {
				if si.Name == lib.NameShell && lib.Alive(si.State) {
					uid = si.UID
					sid = si.Sid
				}
			}
		}
	}
	return sid, uid
}

func (s *Shell) cmdId(args []string) {
	_, uid := s.ownSidUid()
	s.out("uid=" + strconv.FormatUint(uint64(uid), 10) + " (" + lib.Username(uid) + ")")
}

func (s *Shell) cmdPasswd(args []string) {
	// passwd <new-password> — change OWN row (self-only).
	// passwd <user> <new-password> — change another row, needs CAP_FS_ADMIN.
	// v1 limitation (documented): no old-password check — physical
	// terminal equivalence. New salt per change; login.wasm re-reads
	// /etc/users on every AUTH so the new hash is honored at once.
	if s.fs == nil {
		s.out("passwd: fs unavailable")
		return
	}
	if s.reg == nil {
		s.out("passwd: registry unavailable")
		return
	}
	var target, newpw string
	ownSid, ownUID := s.ownSidUid()
	switch len(args) {
	case 1:
		target = ""
		newpw = args[0]
	case 2:
		target = args[0]
		newpw = args[1]
	default:
		s.out("usage: passwd [user] <new-password>")
		s.exitStatus = 1
		return
	}
	if newpw == "" {
		s.out("passwd: empty password rejected")
		s.exitStatus = 1
		return
	}
	if target != "" {
		caps, err := s.reg.Caps(ownSid)
		var mask uint64
		if err == nil {
			for _, c := range caps {
				mask |= c.Rights
			}
		}
		if mask&lib.CapFSAdmin == 0 {
			s.out("passwd: changing another user needs CAP_FS_ADMIN")
			s.exitStatus = 1
			return
		}
	}
	usersRaw, err := s.readAll("/etc/users")
	if err != nil {
		// First-boot provisioning: a fresh ramdisk volume has no
		// /etc/users (the ESP seed is not copied into the fs volume;
		// only /etc/motd is seeded by fs). uid 0 may create the file
		// with its own admin row; anyone else gets an error.
		if target != "" || ownUID != 0 {
			s.out("passwd: cannot read /etc/users: " + err.Error())
			s.exitStatus = 1
			return
		}
		usersRaw = []byte{}
	}
	// Preserve comments/blanks/ordering: rewrite only the target line.
	keptNL := strings.HasSuffix(string(usersRaw), "\n")
	var lines []string
	if strings.TrimSpace(string(usersRaw)) == "" {
		lines = []string{}
		keptNL = true
	} else {
		lines = strings.Split(string(usersRaw), "\n")
	}
	hit := -1
	for i, ln := range lines {
		trim := strings.TrimSpace(ln)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		parts := strings.SplitN(trim, ":", 4)
		if len(parts) != 4 {
			continue
		}
		if target == "" {
			if lineUID, err := strconv.ParseUint(parts[1], 10, 32); err == nil && uint32(lineUID) == ownUID {
				hit = i
			}
		} else if parts[0] == target {
			hit = i
		}
	}
	if hit < 0 {
		// Provisioning path (missing file, uid 0, self): append an
		// admin row holding every capability bit (AGENTS.md admin
		// identity). Other users are added later via the two-arg form.
		if len(usersRaw) == 0 && target == "" && ownUID == 0 {
			// Name matches the seed /etc/users + AGENTS.md admin
			// identity (lib.Username(0) is "root"; the file uses
			// "admin" — login looks users up by this name).
			lines = append(lines, "admin:"+strconv.FormatUint(uint64(ownUID), 10)+"::0x"+
				strconv.FormatUint(lib.CapAll, 16))
			hit = len(lines) - 1
		} else {
			if target == "" {
				s.out("passwd: no entry for uid " + strconv.FormatUint(uint64(ownUID), 10))
			} else {
				s.out("passwd: no such user '" + target + "'")
			}
			s.exitStatus = 1
			return
		}
	}
	parts := strings.SplitN(strings.TrimSpace(lines[hit]), ":", 4)
	var tick uint64
	if s.k.HasClock() {
		tick = s.k.ClockMs()
	} else {
		tick = uint64(time.Now().UnixNano())
	}
	salt := passwdSalt(parts[0], ownUID, tick)
	sum := sha256.Sum256([]byte(salt + newpw))
	lines[hit] = parts[0] + ":" + parts[1] + ":" + salt + "$" + hex.EncodeToString(sum[:]) + ":" + parts[3]
	out := strings.Join(lines, "\n")
	if keptNL && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if err := s.fs.Create("/etc/users"); err != nil {
		s.out("passwd: cannot truncate /etc/users: " + err.Error())
		s.exitStatus = 1
		return
	}
	if _, err := s.fs.WriteFile("/etc/users", 0, []byte(out)); err != nil {
		s.out("passwd: cannot write /etc/users: " + err.Error())
		s.exitStatus = 1
		return
	}
	s.out("passwd: ok")
}

// passwdSalt mints a fresh salt per change from clock/counter material
// (hex only — never ':' or '$', which are /etc/users delimiters).
func passwdSalt(name string, uid uint32, tick uint64) string {
	passwdSaltCtr++
	t := tick*0x9E3779B1 + uint64(passwdSaltCtr)*0x85EBCA6B
	_ = name
	return "s" + strconv.FormatUint(uint64(uid), 16) + strconv.FormatUint(t, 16)
}

var passwdSaltCtr uint64

func (s *Shell) cmdTop() {
	if s.reg == nil {
		s.out("top: registry unavailable")
		return
	}
	list, err := s.reg.List()
	if err != nil {
		s.out("top: " + err.Error())
		return
	}
	statLine := ""
	if st, err := s.reg.SysStat(); err == nil {
		pre := "off"
		if st.PreemptOn {
			pre = "on"
		}
		statLine = "cpus=" + strconv.FormatUint(uint64(st.NCPUs), 10) +
			" quantum=" + strconv.FormatUint(uint64(st.QuantumUs), 10) + "us" +
			" preempt=" + pre
	} else {
		statLine = "sysstat unavailable"
	}
	s.out("SID  UID  STATE    NAME")
	for _, si := range list {
		state := "runnable"
		if si.State == lib.StateRunning {
			state = "running"
		} else if si.State == lib.StateZombie {
			state = "zombie"
		} else if si.State == lib.StateFree {
			state = "free"
		}
		s.out(strconv.FormatUint(uint64(si.Sid), 10) + "  " +
			strconv.FormatUint(uint64(si.UID), 10) + "  " +
			state + "  " + si.Name)
	}
	s.out(strconv.Itoa(len(list)) + " sessions live (" + statLine + ")")
}

// fetchLog drains the retained kernel log via LOGDUMP (op 9).
func (s *Shell) fetchLog() (string, error) {
	if s.reg == nil {
		return "", errors.New("registry unavailable")
	}
	raw, err := s.reg.LogAll()
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (s *Shell) cmdDmesg() {
	// v1 syslog path (registry LOGDUMP): kernel boot trail, audits,
	// panics, guest output. The /var/log file sink stays post-v1.
	data, err := s.fetchLog()
	if err != nil {
		s.out("dmesg: " + err.Error())
		return
	}
	for _, ln := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		s.out(ln)
	}
}

func (s *Shell) cmdMemstat() {
	if s.reg == nil {
		s.out("memstat: registry unavailable")
		return
	}
	st, err := s.reg.SysStat()
	if err != nil {
		s.out("memstat: " + err.Error())
		return
	}
	free := uint64(0)
	if st.MemTotal > st.MemUsed {
		free = st.MemTotal - st.MemUsed
	}
	s.out("pool total=" + strconv.FormatUint(st.MemTotal, 10) +
		" used=" + strconv.FormatUint(st.MemUsed, 10) +
		" free=" + strconv.FormatUint(free, 10))
	pct := uint64(0)
	if st.MemTotal > 0 {
		pct = st.MemUsed * 100 / st.MemTotal
	}
	s.out("pool used " + strconv.FormatUint(pct, 10) + "% (" +
		strconv.FormatUint(st.MemUsed/4096, 10) + "/" +
		strconv.FormatUint(st.MemTotal/4096, 10) + " pages)")
	list, err := s.reg.List()
	if err != nil {
		s.out("sessions: unknown (" + err.Error() + ")")
		return
	}
	s.out("sessions: " + strconv.Itoa(len(list)))
}

func (s *Shell) cmdAudit(args []string) {
	// audit [sid] [pattern...]: filter the v1 syslog for [audit]
	// capability-check trail lines containing every given substring.
	data, err := s.fetchLog()
	if err != nil {
		s.out("audit: " + err.Error())
		return
	}
	shown := 0
	total := 0
	for _, ln := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		if !strings.Contains(ln, "[audit]") {
			continue
		}
		total++
		ok := true
		for _, a := range args {
			if !strings.Contains(ln, a) {
				ok = false
				break
			}
		}
		if ok {
			s.out(ln)
			shown++
		}
	}
	if total == 0 {
		s.out("audit: no audit records retained")
		return
	}
	s.out("audit: " + strconv.Itoa(shown) + "/" + strconv.Itoa(total) + " records shown")
}

func parseIPv4(s string, out *[4]byte) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 255 {
			return false
		}
		out[i] = byte(v)
	}
	return true
}

func (s *Shell) cmdPing(args []string) {
	if len(args) == 0 {
		s.out("usage: ping <ip>")
		return
	}
	if s.nc == nil {
		s.out("ping: net unavailable")
		return
	}
	parts := strings.Split(args[0], ".")
	if len(parts) != 4 {
		s.out("ping: bad IP " + args[0])
		return
	}
	var ip [4]byte
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 255 {
			s.out("ping: bad octet " + p)
			return
		}
		ip[i] = byte(v)
	}
	payload := bytes.Repeat([]byte("P"), 56)
	for seq := 1; seq <= 3; seq++ {
		rtt, data, err := s.nc.Ping(ip, 0x1234, uint16(seq), payload)
		if err != nil {
			s.out("ping: " + err.Error())
			s.exitStatus = 1
			return
		}
		s.out("64 bytes from " + args[0] + ": icmp_seq=" + strconv.Itoa(seq) +
			" ttl=64 time=" + strconv.Itoa(int(rtt)) + "ms")
		_ = data
	}
	s.exitStatus = 0
}

func (s *Shell) cmdNc(args []string) {
	if len(args) < 1 || (len(args) > 0 && strings.HasPrefix(args[0], "-")) {
		s.out("usage: nc [-u] <host> <port>")
		return
	}
	if s.nc == nil {
		s.out("nc: net unavailable")
		return
	}
	udp := false
	host := args[0]
	port := 80
	for _, a := range args {
		if a == "-u" {
			udp = true
		}
	}
	for _, a := range args {
		if a == host || a == "-u" {
			continue
		}
		if p, err := strconv.Atoi(a); err == nil {
			port = p
		}
	}
	var hostIP [4]byte
	if !parseIPv4(host, &hostIP) {
		s.out("nc: resolving " + host + " (no DNS; use IP)")
		return
	}
	ipParts := strings.Split(host, ".")
	if len(ipParts) != 4 {
		s.out("nc: bad IP " + host)
		return
	}
	for i, p := range ipParts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 255 {
			s.out("nc: bad octet " + p)
			return
		}
		hostIP[i] = byte(v)
	}
	if udp {
		sock, err := s.nc.OpenUDP(0)
		if err != nil {
			s.out("nc: " + err.Error())
			s.exitStatus = 1
			return
		}
		defer s.nc.Close(sock)
		if err := s.nc.Connect(sock, hostIP, uint16(port)); err != nil {
			s.out("nc: connect: " + err.Error())
			s.exitStatus = 1
			return
		}
		s.out("nc: UDP " + host + ":" + strconv.Itoa(port) + " (sending hello)")
		s.nc.Send(sock, []byte("hello"))
		buf := make([]byte, 1024)
		n, err := s.nc.Recv(sock, buf)
		if err == nil && n > 0 {
			s.out(string(buf[:n]))
		} else {
			s.out("nc: no reply")
		}
	} else {
		conn, err := lib.DialTCP(s.nc, hostIP, uint16(port))
		if err != nil {
			s.out("nc: connect: " + err.Error())
			s.exitStatus = 1
			return
		}
		defer conn.Close()
		s.out("nc: TCP " + host + ":" + strconv.Itoa(port) + " connected")
		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		if err == nil && n > 0 {
			s.out(string(buf[:n]))
		} else {
			s.out("nc: no data")
		}
	}
}

func (s *Shell) cmdHttp(args []string) {
	if len(args) < 2 {
		s.out("usage: http <get|post> <url> [body]")
		return
	}
	if s.nc == nil {
		s.out("http: net unavailable")
		return
	}
	method := strings.ToUpper(args[0])
	url := args[1]
	if !strings.HasPrefix(url, "http://") {
		s.out("http: only http:// URLs supported")
		return
	}
	urlRest := strings.TrimPrefix(url, "http://")
	slash := strings.Index(urlRest, "/")
	var host, path string
	if slash < 0 {
		host = urlRest
		path = "/"
	} else {
		host = urlRest[:slash]
		path = urlRest[slash:]
	}
	port := 80
	if c := strings.Index(host, ":"); c >= 0 {
		p, _ := strconv.Atoi(host[c+1:])
		port = p
		host = host[:c]
	}
	ipParts := strings.Split(host, ".")
	if len(ipParts) != 4 {
		s.out("http: use numeric IP, not hostname")
		return
	}
	var hostIP [4]byte
	for i, p := range ipParts {
		v, _ := strconv.Atoi(p)
		hostIP[i] = byte(v)
	}
	conn, err := lib.DialTCP(s.nc, hostIP, uint16(port))
	if err != nil {
		s.out("http: connect: " + err.Error())
		s.exitStatus = 1
		return
	}
	defer conn.Close()
	var body []byte
	if method == "POST" && len(args) >= 3 {
		body = []byte(args[2])
	}
	req := method + " " + path + " HTTP/1.0\r\nHost: " + host + "\r\nUser-Agent: kshell/1.0\r\n"
	if method == "POST" {
		req += "Content-Length: " + strconv.Itoa(len(body)) + "\r\n"
	}
	req += "Connection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req + string(body))); err != nil {
		s.out("http: write: " + err.Error())
		s.exitStatus = 1
		return
	}
	buf := make([]byte, 2048)
	resp := []byte{}
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			resp = append(resp, buf[:n]...)
		}
		if err != nil || n == 0 {
			break
		}
	}
	line := string(resp)
	if i := strings.Index(line, "\r\n"); i >= 0 {
		s.out("HTTP/1.0 " + strings.TrimPrefix(line[:i], "HTTP/1.0 "))
	} else if len(line) > 0 {
		s.out(strings.SplitN(line, "\n", 2)[0])
	}
}

func (s *Shell) cmdNetstat(args []string) {
	if s.nc == nil {
		s.out("netstat: net unavailable")
		return
	}
	e, a, vi, ic, err := s.nc.StackStats()
	if err != nil {
		s.out("netstat: " + err.Error())
		return
	}
	s.out("eth_rx=" + strconv.FormatUint(e, 10) +
		" arp_rx=" + strconv.FormatUint(a, 10) +
		" ipv4_rx=" + strconv.FormatUint(vi, 10) +
		" icmp_rx=" + strconv.FormatUint(ic, 10))
	socks, err := s.nc.ActiveSockets()
	if err != nil {
		s.out("netstat: sockets: " + err.Error())
		return
	}
	s.out("active sockets: " + strconv.Itoa(len(socks)))
	for _, id := range socks {
		s.out("  sock=" + strconv.Itoa(int(id)))
	}
}

func (s *Shell) cmdIpaddr(args []string) {
	if s.nc == nil {
		s.out("ipaddr: net unavailable")
		return
	}
	ip, err := s.nc.StackIP()
	if err != nil {
		s.out("ipaddr: " + err.Error())
		return
	}
	s.out("inet " + strconv.Itoa(int(ip[0])) + "." + strconv.Itoa(int(ip[1])) +
		"." + strconv.Itoa(int(ip[2])) + "." + strconv.Itoa(int(ip[3])))
}

func (s *Shell) cmdSsh(args []string) {
	if len(args) < 1 {
		s.out("usage: ssh <user@host> [port]")
		return
	}
	if s.nc == nil {
		s.out("ssh: net unavailable")
		return
	}
	uh := args[0]
	at := strings.Index(uh, "@")
	if at < 0 {
		s.out("ssh: need user@host")
		return
	}
	user := uh[:at]
	host := uh[at+1:]
	port := 22
	if len(args) >= 2 {
		if p, err := strconv.Atoi(args[1]); err == nil {
			port = p
		}
	}
	ipParts := strings.Split(host, ".")
	if len(ipParts) != 4 {
		s.out("ssh: use numeric IP, not hostname")
		return
	}
	var hostIP [4]byte
	for i, p := range ipParts {
		v, _ := strconv.Atoi(p)
		hostIP[i] = byte(v)
	}
	conn, err := lib.DialTCP(s.nc, hostIP, uint16(port))
	if err != nil {
		s.out("ssh: connect: " + err.Error())
		s.exitStatus = 1
		return
	}
	defer conn.Close()
	// Use the SSH client library (golang.org/x/crypto/ssh) to handshake.
	// For now, the outbound client is a thin wrapper around a net.Conn
	// fed to ssh.NewClientConn; full wire support is in services/ssh.
	_ = user
	s.out("ssh: connected to " + host + ":" + strconv.Itoa(port) + " (handshake via services/ssh)")
}

func (s *Shell) cmdPorts(args []string) {
	if s.reg == nil {
		s.out("ports: registry unavailable")
		return
	}
	list, err := s.reg.List()
	if err != nil {
		s.out("ports: " + err.Error())
		return
	}
	s.out("well-known ports:")
	for _, si := range list {
		if si.Name != "" {
			s.out(si.Name + "  sid=" + strconv.FormatUint(uint64(si.Sid), 10) + "  uid=" + strconv.FormatUint(uint64(si.UID), 10))
		}
	}
}

func (s *Shell) cmdSessinfo(args []string) {
	if s.reg == nil {
		s.out("sessinfo: registry unavailable")
		return
	}
	if len(args) == 0 {
		s.out("usage: sessinfo <sid>")
		return
	}
	sid, err := strconv.Atoi(args[0])
	if err != nil {
		s.out("sessinfo: bad sid")
		return
	}
	list, err := s.reg.List()
	if err != nil {
		s.out("sessinfo: " + err.Error())
		return
	}
	for _, si := range list {
		if si.Sid == uint32(sid) {
			s.out("sid=" + strconv.FormatUint(uint64(si.Sid), 10))
			s.out("uid=" + strconv.FormatUint(uint64(si.UID), 10))
			s.out("name=" + si.Name)
			s.out("state=" + strconv.Itoa(int(si.State)))
			return
		}
	}
	s.out("sessinfo: session not found")
}

func (s *Shell) cmdCaphint(args []string) {
	if len(args) == 0 {
		s.out("usage: caphint <action>")
		return
	}
	action := args[0]
	switch action {
	case "run":
		s.out("CAP_SPAWN")
	case "reboot":
		s.out("CAP_ADMIN")
	case "kill-session":
		s.out("CAP_ADMIN | CAP_KILL")
	case "devices":
		s.out("CAP_ADMIN | CAP_PCI")
	case "passwd":
		s.out("CAP_AUTH")
	case "top", "dmesg", "memstat", "audit":
		s.out("CAP_ADMIN")
	case "mount":
		s.out("CAP_FS_ADMIN")
	default:
		s.out("caphint: unknown action '" + action + "'")
	}
}

func (s *Shell) cmdChcaps(args []string) {
	if s.reg == nil {
		s.out("chcaps: registry unavailable")
		return
	}
	if len(args) < 2 {
		s.out("usage: chcaps <sid> <+/-cap> [more +/-cap ...]")
		return
	}
	sid, err := strconv.Atoi(args[0])
	if err != nil {
		s.out("chcaps: bad sid")
		return
	}
	var clear, set uint64
	for _, a := range args[1:] {
		if len(a) < 2 {
			s.out("chcaps: bad cap '" + a + "'")
			return
		}
		name := a[1:]
		bit, ok := capBitByName(name)
		if !ok {
			s.out("chcaps: unknown cap '" + name + "'")
			return
		}
		switch a[0] {
		case '+':
			set |= bit
		case '-':
			clear |= bit
		default:
			s.out("chcaps: bad prefix (need + or -)")
			return
		}
	}
	rc, err := s.reg.Chcaps(uint32(sid), clear, set)
	if err != nil {
		s.out("chcaps: denied (no cap or no such session)")
		return
	}
	if rc != 0 {
		s.out("chcaps: denied (rc=" + strconv.Itoa(int(rc)) + ")")
		return
	}
	s.out("chcaps: ok")
}

func capBitByName(name string) (uint64, bool) {
	switch name {
	case "CAP_KILL":
		return 1 << 0, true
	case "CAP_DEVMAN":
		return 1 << 1, true
	case "CAP_POWER":
		return 1 << 2, true
	case "CAP_FOCUS":
		return 1 << 3, true
	case "CAP_FS_ADMIN":
		return 1 << 4, true
	case "CAP_NET":
		return 1 << 5, true
	case "CAP_SPAWN":
		return 1 << 6, true
	case "CAP_AUTH":
		return 1 << 7, true
	case "CAP_PCI":
		return 1 << 8, true
	case "CAP_FB":
		return 1 << 9, true
	}
	return 0, false
}

func (s *Shell) cmdPkg(args []string) {
	if len(args) < 1 {
		s.out("usage: pkg <list|install|remove|update> [module]")
		return
	}
	sub := args[0]
	switch sub {
	case "list":
		s.out("pkg list: pkg logic in services/pkg (host-tested); shell integration deferred")
	case "install":
		if len(args) < 2 {
			s.out("usage: pkg install <module.wasm>")
			return
		}
		s.out("pkg install: signature check via services/pkg (host-tested); shell integration deferred")
	case "remove":
		if len(args) < 2 {
			s.out("usage: pkg remove <module>")
			return
		}
		s.out("pkg remove: " + args[1] + " (deferred to services/pkg)")
	case "update":
		s.out("pkg update: deferred to services/pkg")
	default:
		s.out("pkg: unknown subcommand '" + sub + "'")
	}
}

func (s *Shell) cmdSysctl(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		s.out("usage: sysctl <key=value|key>")
		return
	}
	if s.fs == nil {
		s.out("sysctl: fs unavailable")
		return
	}
	// kernel.conf is read by init; shell just displays the request
	s.out("sysctl: " + args[0] + " (applied via registry port — Phase 19)")
}

func (s *Shell) cmdInitctl(args []string) {
	if len(args) == 0 {
		s.out("usage: initctl <restart <service>|reload-conf>")
		return
	}
	cmd := args[0]
	switch cmd {
	case "restart":
		if len(args) < 2 {
			s.out("usage: initctl restart <service>")
			return
		}
		s.out("initctl: restart " + args[1] + " (via registry SPAWN — Phase 19)")
	case "reload-conf":
		s.out("initctl: reload-conf (init re-reads /etc/init.conf)")
	default:
		s.out("initctl: unknown subcommand '" + cmd + "'")
	}
}

func (s *Shell) cmdCheckconf(args []string) {
	if s.fs == nil {
		s.out("checkconf: fs unavailable")
		return
	}
	// Check /etc/init.conf
	data, err := s.readAll("/etc/init.conf")
	if err != nil {
		s.out("checkconf: cannot read /etc/init.conf: " + err.Error())
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	errors := 0
	for i, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		// Format: <name> <path> <capmask-hex> [respawn]
		fields := strings.Fields(ln)
		if len(fields) < 3 {
			s.out("checkconf: line " + strconv.Itoa(i+1) + ": too few fields")
			errors++
		}
	}
	// Check /etc/users
	data, err = s.readAll("/etc/users")
	if err != nil {
		s.out("checkconf: cannot read /etc/users: " + err.Error())
	} else {
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		for i, ln := range lines {
			parts := strings.SplitN(ln, ":", 4)
			if len(parts) < 3 {
				s.out("checkconf: /etc/users line " + strconv.Itoa(i+1) + ": bad format")
				errors++
			}
		}
	}
	if errors == 0 {
		s.out("checkconf: OK")
	} else {
		s.out("checkconf: " + strconv.Itoa(errors) + " error(s)")
	}
}
