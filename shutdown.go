package main

import (
	"net/http"
	"time"
)

// Shutdown stops every live agent session and refinement, and waits up to
// timeout for them to wind down. Open questions stay open: answering one after
// a restart resumes its session.
func (a *App) Shutdown(timeout time.Duration) {
	a.stopOnce.Do(func() { close(a.quit) })
	a.mu.Lock()
	for _, run := range a.runs {
		run.stop("shutdown")
	}
	a.mu.Unlock()

	done := make(chan struct{})
	go func() {
		a.busy.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

func (a *App) handleShutdown(w http.ResponseWriter, r *http.Request) {
	select {
	case a.stopReq <- struct{}{}:
	default:
	}
	writeJSON(w, 202, map[string]string{"status": "stopping"})
}
