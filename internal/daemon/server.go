package daemon

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type Status struct {
	Version      string    `json:"version"`
	PID          int       `json:"pid"`
	StartedAt    time.Time `json:"started_at"`
	ProxyAddress string    `json:"proxy_address"`
	// Why the proxy does not listen. The daemon still runs the processes then.
	ProxyError string `json:"proxy_error,omitempty"`
	// A daemon that shuts down still answers, but it refuses every start.
	ShuttingDown bool `json:"shutting_down,omitzero"`
}

type processList struct {
	Processes []Process `json:"processes"`
}

type routeList struct {
	Routes []Route `json:"routes"`
}

// shutdownReply names the daemon that stops, so that the caller can tell it from the next one on the same socket.
type shutdownReply struct {
	PID int `json:"pid"`
}

type errorBody struct {
	Error string `json:"error"`
}

// Server is the API of the daemon. It is HTTP so that curl --unix-socket can debug it.
type Server struct {
	Supervisor *Supervisor
	Routes     *RouteTable
	Status     Status
	// Called after a shutdown call stopped every process.
	OnShutdown func()
	Logger     *slog.Logger
}

func (server *Server) Handler() http.Handler {
	logger := cmp.Or(server.Logger, slog.New(slog.DiscardHandler))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(writer http.ResponseWriter, request *http.Request) {
		status := server.Status
		status.ShuttingDown = server.Supervisor.IsShuttingDown()
		writeJSON(writer, http.StatusOK, status)
	})
	mux.HandleFunc("GET /v1/processes", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, processList{Processes: server.Supervisor.List()})
	})
	mux.HandleFunc("PUT /v1/processes/{name}", func(writer http.ResponseWriter, request *http.Request) {
		if err := validateName(request.PathValue("name")); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		var spec Spec
		if err := decodeBody(request, &spec); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		if err := spec.Validate(); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		process, err := server.Supervisor.Put(request.PathValue("name"), spec)
		writeProcess(writer, process, err)
	})
	// A body is the specification to restart with. A caller without one restarts the stored specification.
	mux.HandleFunc("POST /v1/processes/{name}/restart", func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		if len(body) == 0 {
			process, err := server.Supervisor.Restart(request.PathValue("name"))
			writeProcess(writer, process, err)
			return
		}
		var spec Spec
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&spec); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		if err := spec.Validate(); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		process, err := server.Supervisor.RestartWith(request.PathValue("name"), spec)
		writeProcess(writer, process, err)
	})
	mux.HandleFunc("DELETE /v1/processes/{name}", func(writer http.ResponseWriter, request *http.Request) {
		if err := server.Supervisor.Delete(request.PathValue("name")); err != nil {
			writeError(writer, statusOf(err), err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /v1/routes", func(writer http.ResponseWriter, request *http.Request) {
		var body routeList
		if err := decodeBody(request, &body); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		if err := server.Routes.Replace(body.Routes); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/shutdown", func(writer http.ResponseWriter, request *http.Request) {
		server.Supervisor.Shutdown()
		writeJSON(writer, http.StatusOK, shutdownReply{PID: server.Status.PID})
		if server.OnShutdown != nil {
			server.OnShutdown()
		}
	})
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			// A status poll every few hundred milliseconds would bury the calls that change something.
			logger.Info("api call", "method", request.Method, "path", request.URL.Path)
		}
		mux.ServeHTTP(writer, request)
	})
}

func decodeBody(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func statusOf(err error) int {
	switch {
	case errors.Is(err, ErrUnknownProcess):
		return http.StatusNotFound
	case errors.Is(err, ErrShuttingDown):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func writeProcess(writer http.ResponseWriter, process Process, err error) {
	if err != nil {
		writeError(writer, statusOf(err), err)
		return
	}
	writeJSON(writer, http.StatusOK, process)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, errorBody{Error: err.Error()})
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}
