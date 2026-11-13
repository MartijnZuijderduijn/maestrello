package board

import (
	"net/http"

	"maestrello/internal/model"
)

// handleToolbox serves the catalog: [{id, label, description, harnesses}].
func (a *App) handleToolbox(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, model.Toolbox)
}
