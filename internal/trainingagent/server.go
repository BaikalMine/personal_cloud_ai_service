package trainingagent

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"ai-access-gateway/internal/loratraining"
)

const maxSpecBytes = 64 << 10

type Server struct {
	tokenHash  [32]byte
	controller *Controller
}

func NewServer(token string, controller *Controller) (*Server, error) {
	if len(strings.TrimSpace(token)) < 32 {
		return nil, errors.New("LoRA training agent token must contain at least 32 characters")
	}
	if controller == nil {
		return nil, errors.New("controller is required")
	}
	return &Server{tokenHash: sha256.Sum256([]byte(strings.TrimSpace(token))), controller: controller}, nil
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.handleHealth)
	mux.Handle("/v1/profiles", server.authenticate(http.HandlerFunc(server.handleProfiles)))
	mux.Handle("/v1/jobs", server.authenticate(http.HandlerFunc(server.handleJobs)))
	mux.Handle("/v1/jobs/", server.authenticate(http.HandlerFunc(server.handleJob)))
	mux.Handle("/v1/gateway-jobs", server.authenticate(http.HandlerFunc(server.handleGatewayJob)))
	mux.Handle("/v1/gateway-jobs/fence", server.authenticate(http.HandlerFunc(server.handleSubmissionFence)))
	return agentSecurityHeaders(mux)
}

func (server *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true})
}

func (server *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	profiles := server.controller.Profiles()
	writeJSON(w, http.StatusOK, loratraining.ProfilesResponse{Available: true, Profiles: profiles})
}

func (server *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, server.controller.MaxDatasetBytes()+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "Ожидалась multipart-форма с датасетом.")
		return
	}
	var spec loratraining.JobSpec
	specRead := false
	datasetRead := false
	var status loratraining.JobStatus
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			writeError(w, http.StatusBadRequest, "Не удалось прочитать датасет.")
			return
		}
		switch part.FormName() {
		case "spec":
			if specRead || datasetRead {
				part.Close()
				writeError(w, http.StatusBadRequest, "Описание задания должно идти перед датасетом.")
				return
			}
			decoder := json.NewDecoder(io.LimitReader(part, maxSpecBytes))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&spec); err != nil {
				part.Close()
				writeError(w, http.StatusBadRequest, "Некорректное описание задания.")
				return
			}
			specRead = true
		case "dataset":
			if !specRead || datasetRead {
				part.Close()
				writeError(w, http.StatusBadRequest, "Некорректный порядок частей датасета.")
				return
			}
			status, err = server.controller.Submit(r.Context(), spec, part)
			if err != nil {
				part.Close()
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			datasetRead = true
		default:
			_, _ = io.Copy(io.Discard, io.LimitReader(part, maxSpecBytes))
		}
		part.Close()
	}
	if !specRead || !datasetRead {
		writeError(w, http.StatusBadRequest, "Описание задания или ZIP-датасет отсутствуют.")
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

func (server *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	remainder := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/jobs/"), "/")
	parts := strings.Split(remainder, "/")
	if len(parts) == 0 || parts[0] == "" || len(parts[0]) > 96 {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			status, err := server.controller.Status(id)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, http.StatusOK, status)
			return
		case http.MethodDelete:
			status, err := server.controller.Delete(id)
			if errors.Is(err, os.ErrNotExist) {
				writeCodedError(w, http.StatusNotFound, "job_not_found", "Задание не найдено.")
				return
			}
			if errors.Is(err, ErrJobNotTerminal) {
				writeError(w, http.StatusConflict, "Сначала отмените активное обучение.")
				return
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "Не удалось удалить файлы задания.")
				return
			}
			writeJSON(w, http.StatusOK, status)
			return
		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeError(w, http.StatusMethodNotAllowed, "Метод не поддерживается.")
			return
		}
	}
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		status, err := server.controller.Cancel(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	if len(parts) == 2 && parts[1] == "artifact" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		server.handleArtifact(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	http.NotFound(w, r)
}

func (server *Server) handleGatewayJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	ids := r.URL.Query()["gateway_job_id"]
	if len(ids) != 1 || strings.TrimSpace(ids[0]) == "" || len(ids[0]) > 96 {
		writeError(w, http.StatusBadRequest, "Некорректный идентификатор Gateway.")
		return
	}
	status, err := server.controller.StatusByGatewayID(ids[0])
	if errors.Is(err, os.ErrNotExist) {
		writeCodedError(w, http.StatusNotFound, "job_not_found", "Задание не найдено.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось проверить задание.")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (server *Server) handleSubmissionFence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	ids := r.URL.Query()["gateway_job_id"]
	if len(ids) != 1 || strings.TrimSpace(ids[0]) == "" || len(ids[0]) > 96 {
		writeError(w, http.StatusBadRequest, "Некорректный идентификатор Gateway.")
		return
	}
	result, err := server.controller.FenceGatewaySubmission(ids[0])
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Не удалось подтвердить остановку передачи.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (server *Server) handleArtifact(w http.ResponseWriter, r *http.Request, id string) {
	filename, name, size, err := server.controller.Artifact(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	file, err := os.Open(filename)
	if err != nil {
		writeError(w, http.StatusNotFound, "Файл LoRA не найден.")
		return
	}
	defer file.Close()
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("X-Artifact-Name", name)
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == http.MethodGet {
		_, _ = io.Copy(w, file)
	}
}

func (server *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := strings.TrimSpace(r.Header.Get("Authorization"))
		token, ok := strings.CutPrefix(value, "Bearer ")
		candidate := sha256.Sum256([]byte(token))
		if !ok || subtle.ConstantTimeCompare(candidate[:], server.tokenHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="lora-training-agent"`)
			writeError(w, http.StatusUnauthorized, "Требуется авторизация.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func agentSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeError(w, http.StatusMethodNotAllowed, "Метод не поддерживается.")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}

func writeCodedError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
