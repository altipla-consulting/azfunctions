package azfunctions

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	stdlog "log" // revive:disable-line:imports-blacklist
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/altipla-consulting/env"
	"github.com/altipla-consulting/errors"
	"github.com/altipla-consulting/sentry"
	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
	"golang.org/x/exp/slices"
)

type Server struct {
	router       *mux.Router
	sentryClient *sentry.Client
	logLevel     log.Level
}

type ServerOption func(server *Server)

func WithDebug() ServerOption {
	return func(server *Server) {
		server.logLevel = log.DebugLevel
	}
}

func WithTrace() ServerOption {
	return func(server *Server) {
		server.logLevel = log.TraceLevel
	}
}

func NewServer(opts ...ServerOption) *Server {
	server := &Server{
		router:       mux.NewRouter(),
		sentryClient: sentry.NewClient(os.Getenv("SENTRY_DSN")),
		logLevel:     log.InfoLevel,
	}
	for _, opt := range opts {
		opt(server)
	}
	return server
}

func (server *Server) port() string {
	if port := os.Getenv("FUNCTIONS_CUSTOMHANDLER_PORT"); port != "" {
		return port
	}
	return "8080"
}

type HTTPHandler func(w http.ResponseWriter, r *http.Request) error

func (server *Server) HTTPGet(funcName string, handler HTTPHandler) {
	server.router.HandleFunc("/"+funcName, server.decorateHTTP(funcName, []string{http.MethodGet, http.MethodHead}, handler)).Methods(http.MethodPost)
}

func (server *Server) HTTPPost(funcName string, handler HTTPHandler) {
	server.router.HandleFunc("/"+funcName, server.decorateHTTP(funcName, []string{http.MethodPost}, handler)).Methods(http.MethodPost)
}

type invokeInput struct {
	Data     map[string]json.RawMessage
	Metadata map[string]any
}

type invokeOutput struct {
	Outputs     map[string]any
	Logs        []string
	ReturnValue any
}

type invokeHTTPRequest struct {
	URL     string      `json:"Url"`
	Method  string      `json:"Method"`
	Headers http.Header `json:"Headers"`
	Body    string      `json:"Body"`
}

type invokeHTTPResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       string      `json:"Body,omitempty"`
}

func (server *Server) decorateHTTP(funcName string, methods []string, handler HTTPHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer server.sentryClient.ReportPanicsRequest(r)

		hw := httptest.NewRecorder()
		logger, interceptor := createLogger(server.logLevel, funcName)

		in := new(invokeInput)
		if err := json.NewDecoder(r.Body).Decode(in); err != nil {
			http.Error(hw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			logger.WithFields(errors.LogFields(err)).Error("Cannot decode JSON request")
			emitResponse(w, interceptor, hw)
			return
		}
		if in.Data["req"] == nil {
			http.Error(hw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			logger.Error("Missing req parameter")
			emitResponse(w, interceptor, hw)
			return
		}

		logger.WithField("req", string(in.Data["req"])).Trace("Internal request received")

		inreq := new(invokeHTTPRequest)
		if err := json.Unmarshal(in.Data["req"], inreq); err != nil {
			http.Error(hw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			logger.WithFields(errors.LogFields(err)).Error("Cannot decode HTTP request")
			emitResponse(w, interceptor, hw)
			return
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, requestKey, r)
		ctx = context.WithValue(ctx, loggerKey, logger)
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()

		hr, err := http.NewRequestWithContext(ctx, inreq.Method, inreq.URL, strings.NewReader(inreq.Body))
		if err != nil {
			http.Error(hw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			logger.WithFields(errors.LogFields(err)).Error("Cannot create internal HTTP request")
			emitResponse(w, interceptor, hw)
			return
		}
		hr.Header = inreq.Headers

		if !slices.Contains(methods, hr.Method) {
			http.Error(hw, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			emitResponse(w, interceptor, hw)
			return
		}

		if err := handler(hw, hr); err != nil {
			var herr httpError
			if errors.As(err, &herr) {
				switch herr.StatusCode {
				case http.StatusNotFound, http.StatusUnauthorized, http.StatusBadRequest:
					logger.
						WithFields(errors.LogFields(err)).
						WithField("status", http.StatusText(herr.StatusCode)).
						WithField("reason", herr.Message).
						Error("Handler failed")
					renderError(hw, hr, herr.StatusCode)
					emitResponse(w, interceptor, hw)
					return
				}
			}

			if ctx.Err() == context.Canceled {
				renderError(hw, hr, http.StatusRequestTimeout)
				emitResponse(w, interceptor, hw)
				return
			}

			logger.WithFields(errors.LogFields(err)).Error("Handler failed")
			server.sentryClient.ReportRequest(r, err)

			if ctx.Err() == context.DeadlineExceeded {
				renderError(hw, hr, http.StatusRequestTimeout)
				emitResponse(w, interceptor, hw)
				return
			}

			if env.IsLocal() {
				hw.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintln(hw, errors.Stack(err))
			} else {
				renderError(hw, hr, http.StatusInternalServerError)
			}
			emitResponse(w, interceptor, hw)
			return
		}

		emitResponse(w, interceptor, hw)
	}
}

func renderError(w http.ResponseWriter, r *http.Request, status int) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(status)

	tmpl, err := template.New("error").Parse(errorTemplate)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		LoggerFromRequest(r).WithField("error", err.Error()).Error("Cannot parse template")
		return
	}
	if err := tmpl.Execute(w, status); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		LoggerFromRequest(r).WithField("error", err.Error()).Error("Cannot execute template")
		return
	}
}

func emitResponse(w http.ResponseWriter, interceptor *logInterceptor, handlerw *httptest.ResponseRecorder) {
	result := handlerw.Result()
	body, _ := io.ReadAll(result.Body)

	out := &invokeOutput{
		Logs: interceptor.logs,
		ReturnValue: map[string]any{
			"res": &invokeHTTPResponse{
				StatusCode: result.StatusCode,
				Headers:    result.Header,
				Body:       string(body),
			},
		},
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		http.Error(w, fmt.Sprintf("cannot encode json response: %s", err), http.StatusInternalServerError)
	}
}

func (server *Server) Serve() {
	if !env.IsLocal() {
		version, err := os.ReadFile("version.txt")
		if err != nil && !os.IsNotExist(err) {
			log.Fatal(err)
		} else if err == nil {
			_ = os.Setenv("VERSION", strings.TrimSpace(string(version)))
		}
	}

	if os.Getenv("SENTRY_DSN") != "" {
		log.WithField("dsn", os.Getenv("SENTRY_DSN")).Info("Sentry enabled")
	}

	lis, err := net.Listen("tcp", ":"+server.port())
	if err != nil {
		log.Fatal(err)
	}
	w := log.WithFields(log.Fields{
		"stdlib": "http",
		"port":   server.port(),
	}).Writer()
	defer w.Close()
	web := &http.Server{
		Addr:     ":" + server.port(),
		Handler:  server.router,
		ErrorLog: stdlog.New(w, "", 0),
	}
	go func() {
		if err := web.Serve(lis); err != nil && !isClosingError(err) {
			log.Fatalf("failed to serve: %s", err)
		}
	}()

	log.
		WithFields(log.Fields{
			"listen.0": server.port(),
			"version":  env.Version(),
			"name":     env.ServiceName(),
		}).Info("Instance initialized successfully!")

	signalctx, done := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer done()
	<-signalctx.Done()

	log.Info("Shutting down")

	shutdownctx, done := context.WithTimeout(context.Background(), 25*time.Second)
	defer done()
	_ = web.Shutdown(shutdownctx)
	_ = web.Close()
}

func isClosingError(err error) bool {
	return errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "use of closed network connection")
}
