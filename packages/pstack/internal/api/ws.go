package api

import (
	"context"
	"io"
	"net/http"
	osexec "os/exec"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/terminal"
)

// terminalRoute is GET /api/deployments/:id/terminal — a shell in a container, over a WebSocket.
// THE CONTAINER NAME IS NOT TRUSTED: the request is matched against the containers this deployment
// actually OWNS, and anything else is a 404. The upgrade decision — auth, which deployment, which
// container, is it running — is made here, because once the socket is up the only way to say "no"
// is a close frame the client has to interpret.
func (s *Server) terminalRoute(w http.ResponseWriter, r *http.Request, dep *registry.Deployment, who *auth.Principal, vars map[string]string) error {
	q := r.URL.RawQuery
	shell, ok := query(q, "shell")
	if !ok {
		shell = "sh"
	}
	if !terminal.IsShell(shell) {
		writeError(w, 400, "shell must be one of: "+strings.Join(terminal.Shells, ", "))
		return nil
	}
	wanted, _ := query(q, "container")
	if wanted == "" {
		writeError(w, 400, "container is required")
		return nil
	}
	st, err := s.resolveDep(dep.ID, vars)
	if err != nil {
		return err
	}
	// Challenge unknown skips a docker call this route has no use for.
	rt := inspect.DeploymentRuntime(inspect.RuntimeArgs{Stack: st.Stack, Runner: s.host, Challenge: inspect.Unknown, Orchestrator: orchestratorOf(st)})
	c := findContainer(rt, wanted)
	if c == nil {
		writeJSON(w, 404, jsonx.O("error", `no container "`+wanted+`" in deployment `+dep.ID, "containers", containerNames(rt)))
		return nil
	}
	if !strings.HasPrefix(c.State, "running") {
		writeError(w, 409, `container "`+c.Name+`" is `+c.State+", not running")
		return nil
	}
	if c.Remote {
		writeJSON(w, 409, jsonx.O("error", `container "`+c.Name+`" runs on node `+nodeOf(c)+"; a terminal can only reach tasks on this node", "node", c.Node))
		return nil
	}
	actor := terminal.ActorOf(*who)
	// The row exists BEFORE the socket does: a session that dies on upgrade still happened.
	sessionID, err := s.terminals.Open(terminal.OpenArgs{Actor: actor, Deployment: dep.ID, Container: c.Name, ContainerID: c.ID, Shell: shell})
	if err != nil {
		return err
	}
	if !isUpgrade(r) {
		_ = s.terminals.Close(sessionID)
		writeError(w, 426, "this endpoint expects a websocket upgrade")
		return nil
	}
	argv := s.opts.TerminalArgv(c.ID, shell)
	// No Origin check — parity with the reference (documented deviation); the session cookie is
	// the credential and this is behind the gate.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		_ = s.terminals.Close(sessionID)
		return nil
	}
	s.streams.Add(1)
	defer s.streams.Done()
	s.runTerminal(conn, argv, shell, c.Name, sessionID)
	return nil
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// runTerminal is the socket's life: the banner as a TEXT frame, shell output as BINARY frames
// (decoding to a string here would corrupt a multi-byte character split across two reads), input
// through crToNl to the pipe, and exactly one close.
func (s *Server) runTerminal(conn *websocket.Conn, argv []string, shell, containerName string, sessionID int64) {
	conn.SetReadLimit(16 << 20) // Bun's default
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wmu sync.Mutex
	write := func(kind websocket.MessageType, b []byte) bool {
		wmu.Lock()
		defer wmu.Unlock()
		wctx, done := context.WithTimeout(ctx, 10*time.Second)
		defer done()
		return conn.Write(wctx, kind, b) == nil
	}
	var closeOnce sync.Once
	closeWith := func(code websocket.StatusCode, reason string) {
		closeOnce.Do(func() {
			wmu.Lock()
			_ = conn.Close(code, reason)
			wmu.Unlock()
			_ = s.terminals.Close(sessionID)
		})
	}
	s.termMu.Lock()
	s.termSeq++
	tid := s.termSeq
	s.terms[tid] = func() { closeWith(websocket.StatusGoingAway, "server stopping") }
	s.termMu.Unlock()
	defer func() {
		s.termMu.Lock()
		delete(s.terms, tid)
		s.termMu.Unlock()
	}()

	cmd := osexec.Command(argv[0], argv[1:]...)
	cmd.Env = envListFrom(s.env)
	stdin, err := cmd.StdinPipe()
	if err == nil {
		var stdout, stderr io.ReadCloser
		stdout, err = cmd.StdoutPipe()
		if err == nil {
			stderr, err = cmd.StderrPipe()
			if err == nil {
				err = cmd.Start()
			}
		}
		if err == nil {
			write(websocket.MessageText, []byte("[pstack] "+shell+" in "+containerName+" — no TTY: no prompt, no job control, no curses UIs.\r\n"))
			var pumps sync.WaitGroup
			pump := func(rd io.Reader) {
				defer pumps.Done()
				buf := make([]byte, 32<<10)
				for {
					n, rerr := rd.Read(buf)
					if n > 0 {
						if !write(websocket.MessageBinary, append([]byte(nil), buf[:n]...)) {
							return
						}
					}
					if rerr != nil {
						return
					}
				}
			}
			pumps.Add(2)
			go pump(stdout)
			go pump(stderr)
			exited := make(chan int, 1)
			go func() {
				pumps.Wait()
				werr := cmd.Wait()
				code := 0
				if ee, ok := werr.(*osexec.ExitError); ok {
					code = ee.ExitCode()
				} else if werr != nil {
					code = -1
				}
				exited <- code
			}()
			// Keystrokes go to the shell verbatim, except CR → NL: there is no line discipline
			// (that is what the missing pty WAS), so this one conversion is the whole fix.
			go func() {
				for {
					_, data, rerr := conn.Read(ctx)
					if rerr != nil {
						cancel()
						return
					}
					if _, werr := stdin.Write(CrToNl(data)); werr != nil {
						return // the shell exited between the keystroke and here
					}
				}
			}()
			select {
			case code := <-exited:
				write(websocket.MessageText, []byte("\r\n[pstack] shell exited ("+jsonx.NumberString(float64(code))+").\r\n"))
				closeWith(websocket.StatusNormalClosure, "shell exited")
			case <-ctx.Done():
				// The client went away (or the server is stopping): kill the shell.
				_ = cmd.Process.Kill()
				<-exited
				closeWith(websocket.StatusNormalClosure, "")
			}
			return
		}
	}
	write(websocket.MessageText, []byte("\r\n[pstack] could not start a shell: "+err.Error()+"\r\n"))
	closeWith(websocket.StatusInternalError, "spawn failed")
}

func envListFrom(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
