package board

import (
	"strings"
	"testing"
	"time"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

func TestShutdownEndsSessions(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	looping := readyCard(t, app, app.dataDir, "LOOP")
	asking := readyCard(t, app, app.dataDir, "ASK")
	app.Run(looping.ID)
	app.Run(asking.ID)
	waitOpenInput(t, app, asking.ID)
	for app.liveRun(looping.ID) == nil {
	}

	app.Shutdown(10 * time.Second)
	if app.liveRun(looping.ID) != nil || app.liveRun(asking.ID) != nil {
		t.Fatal("sessions still live after shutdown")
	}
	c, _ := app.store.GetCard(looping.ID)
	if c.Status != model.StatusFailed || !strings.Contains(c.FailReason, "board stopped") {
		t.Errorf("looping card: %s %q", c.Status, c.FailReason)
	}
	c, _ = app.store.GetCard(asking.ID)
	if in, _ := app.store.OpenInput(asking.ID); c.Status != model.StatusNeedsInput || in == nil {
		t.Errorf("asking card: %s, open input %v; the question should survive", c.Status, in)
	}
}
