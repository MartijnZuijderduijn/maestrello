package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"maestrello/internal/board"
)

const defaultAddr = "127.0.0.1:7878"

const usage = `maestrello: a local kanban board where Claude Code agents work your cards

Usage:
  maestrello [start] [flags]     start the board and open it in the browser (default)
  maestrello open [-addr ...]    open a running board in the browser
  maestrello status [-addr ...]  show whether a board is running and where its data lives
  maestrello stop [-addr ...]    stop a running board and its agent sessions
  maestrello mcp [-addr ...]     serve the board's MCP tools over stdio (for Claude Desktop); needs a running board
  maestrello restart [flags]     rebuild from source, stop the running board, start the new build
                                (takes the start flags; pass the ones the board was started with)
  maestrello help                show this help

Run "maestrello start -h" for the start flags.
`

type startOpts struct {
	addr, dataDir, sandboxRoot, claudeBin, opencodeBin, src string
	open                                                    bool
}

// boardOptions is what the board itself needs of the start flags.
func (o *startOpts) boardOptions() board.Options {
	return board.Options{Addr: o.addr, DataDir: o.dataDir, SandboxRoot: o.sandboxRoot, ClaudeBin: o.claudeBin, OpencodeBin: o.opencodeBin}
}

// openBrowser opens url in the user's browser. Replaced in tests.
var openBrowser = func(url string) error {
	name := "xdg-open"
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	return exec.Command(name, url).Start()
}

func Run(args []string) error {
	cmd := "start"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "start":
		o, err := parseStart(args, os.Stderr)
		if err != nil {
			return err
		}
		return start(o)
	case "restart":
		o, err := parseStart(args, os.Stderr)
		if err != nil {
			return err
		}
		return restart(o, args)
	case "mcp":
		fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
		addr := fs.String("addr", defaultAddr, "address of the board")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runBridge(os.Stdin, os.Stdout, "http://"+*addr)
	case "open", "status", "stop":
		fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
		addr := fs.String("addr", defaultAddr, "address of the board")
		if err := fs.Parse(args); err != nil {
			return err
		}
		info, err := boardInfo(*addr)
		if err != nil {
			return fmt.Errorf("no board running on %s (start one with \"maestrello start\")", *addr)
		}
		url := "http://" + *addr
		switch cmd {
		case "status":
			printInfo(os.Stdout, url, info)
			return nil
		case "stop":
			return stopBoard(*addr)
		}
		fmt.Println("opening", url)
		return openBrowser(url)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func parseStart(args []string, out io.Writer) (*startOpts, error) {
	o := &startOpts{}
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.addr, "addr", defaultAddr, "listen address (keep it on localhost)")
	fs.StringVar(&o.dataDir, "data", "", "data directory for the database and workspaces (default ~/.maestrello, or <sandbox-root>/work in sandbox mode)")
	fs.StringVar(&o.sandboxRoot, "sandbox-root", "", "refuse every path outside this directory and stub out PRs (for development and tests)")
	fs.StringVar(&o.claudeBin, "claude", "claude", "path to the claude CLI")
	fs.StringVar(&o.opencodeBin, "opencode", "opencode", "path to the opencode CLI")
	fs.BoolVar(&o.open, "open", true, "open the board in the browser once it is up")
	fs.StringVar(&o.src, "src", "", "source folder to rebuild from on restart (default: where this binary was built)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	// A relative claude path (e.g. testdata/fake-claude.sh) must survive the
	// changed working directories of the sessions it starts.
	for _, bin := range []*string{&o.claudeBin, &o.opencodeBin} {
		if *bin != filepath.Base(*bin) {
			abs, err := filepath.Abs(*bin)
			if err != nil {
				return nil, err
			}
			*bin = abs
		}
	}
	return o, nil
}

// start serves the board, or opens the one already running on the address.
func start(o *startOpts) error {
	if info, err := boardInfo(o.addr); err == nil {
		url := "http://" + o.addr
		guard, dataDir, err := board.Resolve(o.boardOptions())
		if err != nil {
			return err
		}
		if info.DataDir != dataDir || info.SandboxRoot != guard.Root() {
			fmt.Fprintln(os.Stderr, "a different board is already running on", o.addr+":")
			printInfo(os.Stderr, url, info)
			return fmt.Errorf("stop that board first, or start this one on another address with -addr")
		}
		fmt.Println("a board is already running:")
		printInfo(os.Stdout, url, info)
		if o.open {
			return openBrowser(url)
		}
		return nil
	}
	warnIfStale()
	return board.Serve(o.boardOptions(), func(url string) {
		if o.open {
			if err := openBrowser(url); err != nil {
				fmt.Fprintln(os.Stderr, "could not open the browser:", err)
			}
		}
	})
}

type boardInfoResp struct {
	SandboxRoot string `json:"sandbox_root"`
	DataDir     string `json:"data_dir"`
}

// boardInfo asks the board on addr who it is; it fails when nothing (or
// something other than maestrello) answers there.
func boardInfo(addr string) (*boardInfoResp, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/info")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var info boardInfoResp
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	if info.DataDir == "" {
		return nil, errors.New("not a maestrello board")
	}
	return &info, nil
}

func printInfo(w io.Writer, url string, info *boardInfoResp) {
	fmt.Fprintf(w, "  url:  %s\n  data: %s\n", url, info.DataDir)
	if info.SandboxRoot != "" {
		fmt.Fprintf(w, "  sandbox: %s\n", info.SandboxRoot)
	}
}

// stopBoard asks the board on addr to shut down and waits until it is gone.
func stopBoard(addr string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post("http://"+addr+"/api/shutdown", "application/json", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return fmt.Errorf("the board on %s is an older build without stop support; stop it with Ctrl+C in its terminal or kill its process (ss -ltnp | grep %s)", addr, addr[strings.LastIndex(addr, ":")+1:])
	}
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("board refused to stop: status %d", resp.StatusCode)
	}
	fmt.Println("stopping the board on", addr, "(ending its agent sessions)...")
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if _, err := boardInfo(addr); err != nil {
			fmt.Println("stopped")
			return nil
		}
	}
	return fmt.Errorf("board on %s is still running after 30s", addr)
}
