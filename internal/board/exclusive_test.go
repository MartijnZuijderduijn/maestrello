package board

import (
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/settings"
	"maestrello/internal/testenv"
)

// Skipping refinement and automatic model selection exclude each other.
func TestSkipAndAutoModelExclusive(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	root, x := app.guard.Root(), "x"
	on := ptr(true)

	// Both asked for at once: skipping wins.
	c, err := app.CreateCard(cardInput{Description: &x, RootFolder: &root, SkipRefinement: on, AutoModel: on})
	if err != nil || !c.SkipRefinement || c.AutoModel {
		t.Fatalf("both set: err=%v %+v", err, c)
	}
	// Automatic model chosen on a card of a board that skips by default: the explicit choice wins.
	st, _ := app.store.Settings()
	st.DefaultSkipRefinement = true
	app.store.SaveSettings(st)
	if c, err = app.CreateCard(cardInput{Description: &x, RootFolder: &root, AutoModel: on}); err != nil || c.SkipRefinement || !c.AutoModel {
		t.Fatalf("auto model over default skip: err=%v skip=%v auto=%v", err, c.SkipRefinement, c.AutoModel)
	}
	// A card that skips (a finished prompt handed over) never has automatic selection.
	st.DefaultSkipRefinement, st.AutoModel = false, true
	app.store.SaveSettings(st)
	rp := "finished prompt"
	if c, err = app.CreateCard(cardInput{Description: &x, RootFolder: &root, RefinedPrompt: &rp}); err != nil || !c.SkipRefinement || c.AutoModel {
		t.Fatalf("refined prompt: err=%v %+v", err, c)
	}
	// A Ready card cannot change its skip flag, so automatic selection cannot be turned on there.
	if c, err = app.EditCard(c.ID, cardInput{AutoModel: on}); err != nil || !c.SkipRefinement || c.AutoModel {
		t.Fatalf("edit on a skipped card: err=%v %+v", err, c)
	}
	// A chat never refines, so it never has automatic selection either.
	chat := model.KindChat
	if c, err = app.CreateCard(cardInput{Description: &x, RootFolder: &root, Kind: &chat, AutoModel: on}); err != nil || c.AutoModel {
		t.Fatalf("chat: err=%v %+v", err, c)
	}
}

// Rows and settings saved before the rule existed read back as valid.
func TestExclusiveFlagsNormalisedWhenLoaded(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	c := &model.Card{Title: "old", RootFolder: app.guard.Root(), Model: "fake", Effort: "low", PermissionMode: "auto",
		MaxTurns: 1, MaxMinutes: 1, SkipRefinement: true, AutoModel: true}
	if err := app.store.CreateCard(c); err != nil {
		t.Fatal(err)
	}
	if got, err := app.store.GetCard(c.ID); err != nil || !got.SkipRefinement || got.AutoModel {
		t.Errorf("stored card: err=%v %+v", err, got)
	}
	st, _ := app.store.Settings()
	st.DefaultSkipRefinement, st.AutoModel = true, true
	if settings.Validate(st) == nil {
		t.Error("settings with both defaults accepted")
	}
	app.store.SaveSettings(st) // as an older version could have saved it
	if got, err := app.store.Settings(); err != nil || !got.DefaultSkipRefinement || got.AutoModel {
		t.Errorf("stored settings: err=%v skip=%v auto=%v", err, got.DefaultSkipRefinement, got.AutoModel)
	}
}
