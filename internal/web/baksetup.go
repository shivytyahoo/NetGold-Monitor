package web

// Automatic SQL engine setup endpoints (v2.2): when a Windows machine
// has no usable SQL Server for a .bak import, NetGold can download,
// verify and install Microsoft's free LocalDB engine itself, then
// restore + import automatically. See internal/sqlengine for the job.

import (
	"net/http"

	"netgold/internal/sqlengine"
)

// setupJob returns the (lazily created) engine-setup job bound to this
// server's store.
func (s *Server) setupJob() *sqlengine.Setup {
	s.bakSetupMu.Lock()
	defer s.bakSetupMu.Unlock()
	if s.bakSetup == nil {
		s.bakSetup = &sqlengine.Setup{Store: s.Store, DataDir: s.Store.Dir}
	}
	return s.bakSetup
}

// legacyBakEngine reports the engine situation for the .bak card: is any
// SQL Server usable already, is LocalDB installed, can setup run here?
func (s *Server) legacyBakEngine(w http.ResponseWriter, r *http.Request) {
	st := sqlengine.CurrentState()
	st.RunningAsService = s.RunningAsService
	writeJSON(w, 200, st)
}

// legacyBakSetup starts the setup-and-import job. The user's click on
// "Set Up Engine and Import" is the download consent, passed as
// accept_download; without it the request is refused.
func (s *Server) legacyBakSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path           string `json:"path"`
		AcceptDownload bool   `json:"accept_download"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !req.AcceptDownload {
		errJSON(w, 400, "Setup downloads the SQL engine installer (SqlLocalDB.msi) from Microsoft — please confirm with accept_download.")
		return
	}
	if err := s.setupJob().Start(req.Path, s.RunningAsService); err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"started": true})
}

// legacyBakSetupStatus returns the pollable job status for the .bak card
// (step, download percentage, stage-labelled error, import report).
func (s *Server) legacyBakSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.setupJob().Status())
}

// legacyBakRepair starts the repair-and-retry job (v3.0): the inbuilt
// residue cleanup + clean reinstall of the LocalDB engine, under one
// administrator (UAC) consent. When the request carries the backup
// path, the restore + import continues automatically afterwards;
// with no path it repairs the engine only (the Import page's manual
// "Repair SQL engine" action).
func (s *Server) legacyBakRepair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.setupJob().StartRepair(req.Path, s.RunningAsService); err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"started": true})
}

// legacyBakRemoveInstance stops and deletes the dedicated LocalDB
// instance. The LocalDB program itself is not uninstalled (that stays
// a Windows Settings action, as the returned note explains).
func (s *Server) legacyBakRemoveInstance(w http.ResponseWriter, r *http.Request) {
	note, err := sqlengine.RemoveInstance()
	if err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"removed": true, "note": note})
}
