package miningagent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/mining"
)

// Only use for precondition failures that leave the controller unchanged.
var errCommandRejected = errors.New("mining command rejected before execution")

func NewFencedServer(token string, controller Controller) (*Server, error) {
	s, err := NewServer(token, controller)
	if err == nil {
		s.requireFencing = true
	}
	return s, err
}

func commandSession() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value[:])
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(mining.CommandSession{Protocol: 2, Session: s.session, Required: s.requireFencing})
}

func (s *Server) handleFenced(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var command mining.FencedCommand
	var extra any
	if decoder.Decode(&command) != nil || decoder.Decode(&extra) != io.EOF || command.Revision <= 0 || command.Session != s.session || !s.requireFencing {
		http.Error(w, "invalid command session", http.StatusConflict)
		return
	}
	command.Miner.ScriptPath = strings.TrimSpace(command.Miner.ScriptPath)
	command.Miner.ProcessName = strings.TrimSpace(command.Miner.ProcessName)
	if len(command.Miner.ScriptPath) == 0 || len(command.Miner.ScriptPath) > 1024 || !validProcessName(command.Miner.ProcessName) {
		http.Error(w, "invalid miner", 400)
		return
	}
	if command.Action != "start" && command.Action != "stop" && command.Action != "barrier" && command.Action != "update" {
		http.Error(w, "invalid action", 400)
		return
	}
	if command.Action == "update" {
		u := command.Update
		if u == nil || u.ScriptPath != command.Miner.ScriptPath || u.ProcessName != command.Miner.ProcessName || len(u.MinerName) > 80 || len(u.ArchiveURL) == 0 || len(u.ArchiveURL) > 2048 || !validSHA256(u.ArchiveSHA256) {
			http.Error(w, "invalid update", 400)
			return
		}
	} else if command.Update != nil {
		http.Error(w, "unexpected update", 400)
		return
	}
	select {
	case s.commandGate <- struct{}{}:
		defer func() { <-s.commandGate }()
	case <-r.Context().Done():
		return
	}
	if command.Revision <= s.commandRevision || r.Context().Err() != nil {
		http.Error(w, "superseded command", http.StatusConflict)
		return
	}
	// Accept the newer intent before side effects, including controller errors.
	// A barrier must wait for the preceding controller operation, not its HTTP reply.
	s.commandRevision = command.Revision
	if s.commandUnconfirmed && command.Action != "stop" {
		http.Error(w, "previous miner operation requires confirmed stop", http.StatusConflict)
		return
	}
	if s.commandUnconfirmed && (!strings.EqualFold(command.Miner.ScriptPath, s.unconfirmedMiner.ScriptPath) || !strings.EqualFold(command.Miner.ProcessName, s.unconfirmedMiner.ProcessName)) {
		http.Error(w, "stop must reconcile the unconfirmed miner profile", http.StatusConflict)
		return
	}
	result := mining.CommandResult{Session: s.session, Revision: command.Revision}
	var err error
	switch command.Action {
	case "start":
		result.State, err = s.controller.Start(r.Context(), command.Miner)
	case "stop":
		result.State, err = s.controller.Stop(r.Context(), command.Miner)
	case "barrier":
		result.State, err = s.controller.State(r.Context(), command.Miner.ProcessName)
	case "update":
		var updated mining.UpdateResult
		updated, err = s.controller.Update(r.Context(), *command.Update)
		result.Update = &updated
	}
	status := http.StatusOK
	if command.Action != "barrier" && !errors.Is(err, errCommandRejected) {
		s.commandUnconfirmed = err != nil
		if err != nil {
			s.unconfirmedMiner = command.Miner
		}
	}
	if err != nil {
		status = http.StatusConflict
		result.State = stateWithError(result.State, err)
	} else {
		result.State.Available = true
		result.State.CollectedAt = time.Now().UTC()
		if result.Update != nil {
			result.Update.Success = true
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(result)
}
