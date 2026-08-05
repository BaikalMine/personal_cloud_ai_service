package updateagent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"ai-access-gateway/internal/updates"
)

const maxRequestBytes = 16 << 10

type Controller interface {
	Status(context.Context) (updates.Status, error)
	Check(context.Context, updates.Request) (updates.Status, error)
	Install(context.Context, updates.Request) (updates.Status, error)
}

type Server struct {
	tokenHash  [32]byte
	controller Controller
}

func NewServer(token string, controller Controller) (*Server, error) {
	if len(strings.TrimSpace(token)) < 32 {
		return nil, errors.New("update agent token must contain at least 32 characters")
	}
	if controller == nil {
		return nil, errors.New("controller is required")
	}
	return &Server{tokenHash: sha256.Sum256([]byte(strings.TrimSpace(token))), controller: controller}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.Handle("/v1/status", s.authenticate(http.HandlerFunc(s.handleStatus)))
	mux.Handle("/v1/check", s.authenticate(http.HandlerFunc(s.handleCheck)))
	mux.Handle("/v1/install", s.authenticate(http.HandlerFunc(s.handleInstall)))
	return securityHeaders(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeStatus(w, http.StatusOK, updates.Status{Available: true})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	status, err := s.controller.Status(r.Context())
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, statusWithError(status, err))
		return
	}
	status.Available = true
	writeStatus(w, http.StatusOK, status)
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	s.handleCommand(w, r, s.controller.Check)
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	s.handleCommand(w, r, s.controller.Install)
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request, command func(context.Context, updates.Request) (updates.Status, error)) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request updates.Request
	if err := decoder.Decode(&request); err != nil || !validRequest(request) {
		writeStatus(w, http.StatusBadRequest, updates.Status{Message: "Некорректный список компонентов."})
		return
	}
	log.Printf("update command started components=%s", strings.Join(request.Components, ","))
	status, err := command(r.Context(), request)
	if err != nil {
		log.Printf("update command failed components=%s error=%v", strings.Join(request.Components, ","), err)
		writeStatus(w, http.StatusConflict, statusWithError(status, err))
		return
	}
	status.Available = true
	log.Printf("update command completed components=%s", strings.Join(request.Components, ","))
	writeStatus(w, http.StatusOK, status)
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := strings.TrimSpace(r.Header.Get("Authorization"))
		token, ok := strings.CutPrefix(value, "Bearer ")
		candidate := sha256.Sum256([]byte(token))
		if !ok || subtle.ConstantTimeCompare(candidate[:], s.tokenHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="update-agent"`)
			writeStatus(w, http.StatusUnauthorized, updates.Status{Message: "Требуется авторизация."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validRequest(request updates.Request) bool {
	if len(request.Components) == 0 || len(request.Components) > 3 {
		return false
	}
	seen := make(map[string]struct{}, len(request.Components))
	for _, component := range request.Components {
		if !updates.ValidComponent(component) {
			return false
		}
		if _, exists := seen[component]; exists {
			return false
		}
		seen[component] = struct{}{}
	}
	return true
}

func statusWithError(status updates.Status, err error) updates.Status {
	if status.Message == "" {
		status.Message = err.Error()
	}
	return status
}

func writeStatus(w http.ResponseWriter, statusCode int, status updates.Status) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(status)
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeStatus(w, http.StatusMethodNotAllowed, updates.Status{Message: "Метод не поддерживается."})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
