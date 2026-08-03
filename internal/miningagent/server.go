package miningagent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"ai-access-gateway/internal/mining"
)

const maxRequestBytes = 16 << 10

var processNamePattern = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9_.-]{0,126}\.exe$`)

type Controller interface {
	State(context.Context, string) (mining.State, error)
	Script(context.Context, string) (mining.Script, error)
	Start(context.Context, mining.Request) (mining.State, error)
	Stop(context.Context, mining.Request) (mining.State, error)
}

type Server struct {
	tokenHash  [32]byte
	controller Controller
}

func NewServer(token string, controller Controller) (*Server, error) {
	if len(strings.TrimSpace(token)) < 32 {
		return nil, errors.New("agent token must contain at least 32 characters")
	}
	if controller == nil {
		return nil, errors.New("controller is required")
	}
	return &Server{tokenHash: sha256.Sum256([]byte(strings.TrimSpace(token))), controller: controller}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.Handle("/v1/state", s.authenticate(http.HandlerFunc(s.handleState)))
	mux.Handle("/v1/script", s.authenticate(http.HandlerFunc(s.handleScript)))
	mux.Handle("/v1/start", s.authenticate(http.HandlerFunc(s.handleStart)))
	mux.Handle("/v1/stop", s.authenticate(http.HandlerFunc(s.handleStop)))
	return securityHeaders(mux)
}

func (s *Server) handleScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	scriptPath := strings.TrimSpace(r.URL.Query().Get("script_path"))
	if scriptPath == "" || len(scriptPath) > 1024 {
		writeScript(w, http.StatusBadRequest, mining.Script{Message: "Некорректный путь к скрипту."})
		return
	}
	script, err := s.controller.Script(r.Context(), scriptPath)
	if err != nil {
		if script.Message == "" {
			script.Message = err.Error()
		}
		writeScript(w, http.StatusConflict, script)
		return
	}
	writeScript(w, http.StatusOK, script)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	processName := strings.TrimSpace(r.URL.Query().Get("process_name"))
	if !validProcessName(processName) {
		writeState(w, http.StatusBadRequest, mining.State{Message: "Некорректное имя процесса."})
		return
	}
	state, err := s.controller.State(r.Context(), processName)
	if err != nil {
		writeState(w, http.StatusServiceUnavailable, stateWithError(state, err))
		return
	}
	state.Available = true
	writeState(w, http.StatusOK, state)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	s.handleCommand(w, r, s.controller.Start)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.handleCommand(w, r, s.controller.Stop)
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request, command func(context.Context, mining.Request) (mining.State, error)) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request mining.Request
	if err := decoder.Decode(&request); err != nil {
		writeState(w, http.StatusBadRequest, mining.State{Message: "Некорректное тело запроса."})
		return
	}
	request.ScriptPath = strings.TrimSpace(request.ScriptPath)
	request.ProcessName = strings.TrimSpace(request.ProcessName)
	if len(request.ScriptPath) == 0 || len(request.ScriptPath) > 1024 || !validProcessName(request.ProcessName) {
		writeState(w, http.StatusBadRequest, mining.State{Message: "Некорректные параметры майнера."})
		return
	}
	state, err := command(r.Context(), request)
	if err != nil {
		writeState(w, http.StatusConflict, stateWithError(state, err))
		return
	}
	state.Available = true
	writeState(w, http.StatusOK, state)
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := strings.TrimSpace(r.Header.Get("Authorization"))
		token, ok := strings.CutPrefix(value, "Bearer ")
		candidate := sha256.Sum256([]byte(token))
		if !ok || subtle.ConstantTimeCompare(candidate[:], s.tokenHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mining-agent"`)
			writeState(w, http.StatusUnauthorized, mining.State{Message: "Требуется авторизация."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validProcessName(name string) bool {
	return processNamePattern.MatchString(name) && !strings.ContainsAny(name, `/\`)
}

func stateWithError(state mining.State, err error) mining.State {
	if state.Message == "" {
		state.Message = err.Error()
	}
	return state
}

func writeState(w http.ResponseWriter, status int, state mining.State) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(state)
}

func writeScript(w http.ResponseWriter, status int, script mining.Script) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(script)
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeState(w, http.StatusMethodNotAllowed, mining.State{Message: "Метод не поддерживается."})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
