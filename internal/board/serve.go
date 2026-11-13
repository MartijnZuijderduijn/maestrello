package board

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"maestrello/internal/gitops"
	"maestrello/internal/sandbox"
	"maestrello/internal/store"
)

// Options says where a board listens, keeps its data and which sandbox and agent binaries it uses.
type Options struct {
	Addr, DataDir, SandboxRoot, ClaudeBin, OpencodeBin, MistralBin string
}

// Resolve works out the guard and data dir that o describes.
func Resolve(o Options) (*sandbox.Guard, string, error) {
	guard, err := sandbox.NewGuard(o.SandboxRoot)
	if err != nil {
		return nil, "", err
	}
	dataDir := o.DataDir
	if dataDir == "" {
		if guard.Enabled() {
			dataDir = filepath.Join(guard.Root(), "work")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, "", err
			}
			dataDir = filepath.Join(home, ".maestrello")
			// Keep using the data dir of the board's former name (claudeban).
			if _, err := os.Stat(dataDir); err != nil {
				old := filepath.Join(home, ".claudeban")
				if _, err := os.Stat(old); err == nil {
					dataDir = old
				}
			}
		}
	}
	dataDir, err = filepath.Abs(dataDir)
	return guard, dataDir, err
}

// Serve starts the board and blocks. ready is called once it is listening.
func Serve(o Options, ready func(baseURL string)) error {
	guard, dataDir, err := Resolve(o)
	if err != nil {
		return err
	}
	if err := guard.Check(dataDir); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "work"), 0o755); err != nil {
		return err
	}

	dbName := "maestrello.db"
	if _, err := os.Stat(filepath.Join(dataDir, "claudeban.db")); err == nil {
		dbName = "claudeban.db" // database of the former name
	}
	st, err := store.OpenStore(filepath.Join(dataDir, dbName))
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", o.Addr)
	if err != nil {
		return err
	}
	app := &App{
		store:       st,
		hub:         NewHub(),
		guard:       guard,
		git:         gitops.New(guard),
		dataDir:     dataDir,
		baseURL:     "http://" + ln.Addr().String(),
		claudeBin:   o.ClaudeBin,
		opencodeBin: o.OpencodeBin,
		mistralBin:  o.MistralBin,
		runs:        map[int64]*Run{},
		waiters:     map[int64]chan inputReply{},
		quit:        make(chan struct{}),
		stopReq:     make(chan struct{}, 1),
	}
	if err := app.recover(); err != nil {
		return err
	}
	app.loadUsage()
	go app.refreshAccounts()     // never blocks startup; the header updates via SSE
	go app.refreshAvailability() // likewise: which harness CLIs work on this machine
	if guard.Enabled() {
		fmt.Printf("sandbox mode: confined to %s\n", guard.Root())
	}
	fmt.Printf("maestrello on %s (data in %s)\n", app.baseURL, dataDir)
	if ready != nil {
		ready(app.baseURL)
	}
	srv := &http.Server{Handler: app.routes()}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	select {
	case err := <-errc:
		return err
	case <-sig:
	case <-app.stopReq:
	}
	fmt.Println("stopping: ending agent sessions...")
	app.Shutdown(15 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	fmt.Println("stopped")
	return nil
}
