package main

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
)

func main() {
	if err := runCLI(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "maestrello:", err)
		os.Exit(1)
	}
}

// resolveBoard works out the guard and data dir that o describes.
func resolveBoard(o *startOpts) (*Guard, string, error) {
	guard, err := NewGuard(o.sandboxRoot)
	if err != nil {
		return nil, "", err
	}
	dataDir := o.dataDir
	if dataDir == "" {
		if guard.Enabled() {
			dataDir = filepath.Join(guard.root, "work")
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

// serve starts the board and blocks. ready is called once it is listening.
func serve(o *startOpts, ready func(baseURL string)) error {
	guard, dataDir, err := resolveBoard(o)
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
	store, err := OpenStore(filepath.Join(dataDir, dbName))
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", o.addr)
	if err != nil {
		return err
	}
	app := &App{
		store:       store,
		hub:         NewHub(),
		guard:       guard,
		git:         &Git{guard: guard},
		dataDir:     dataDir,
		baseURL:     "http://" + ln.Addr().String(),
		claudeBin:   o.claudeBin,
		opencodeBin: o.opencodeBin,
		runs:        map[int64]*Run{},
		waiters:     map[int64]chan inputReply{},
		quit:        make(chan struct{}),
		stopReq:     make(chan struct{}, 1),
	}
	if err := app.recover(); err != nil {
		return err
	}
	app.loadUsage()
	go app.refreshAccount()      // never blocks startup; the header updates via SSE
	go app.refreshAvailability() // likewise: which harness CLIs work on this machine
	if guard.Enabled() {
		fmt.Printf("sandbox mode: confined to %s\n", guard.root)
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
