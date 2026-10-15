package aweprometheu

import (
	"net/http"
	"strings"
	"sync/atomic"

	"golang.org/x/exp/slog"
)

var instance *statServer

type statServer struct {
	*http.ServeMux
	live *atomic.Bool
}

func Stats(live *atomic.Bool, app, segment, port string) {
	if instance != nil {
		slog.Warn("second_attempt_starting_stats")
		return
	}
	instance = newStats(live)
	instance.Run(app, segment, port)
}
func New(app, segment, port string) {
	if instance != nil {
		slog.Warn("second_attempt_starting_stats")
		return
	}
	live := new(atomic.Bool)
	instance = newStats(live)
	instance.Run(app, segment, port)
}

func newStats(live *atomic.Bool) *statServer {
	return &statServer{
		ServeMux: http.NewServeMux(),
		live:     live,
	}
}

func (s *statServer) Ok() {
	s.live.Store(true)
}
func (s *statServer) Fail() {
	s.live.Store(false)
}

func Ok() {
	instance.Ok()
}
func Fail() {
	instance.Fail()
}

func (s *statServer) Run(app, segment, port string) {
	addr := port
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}

	s.Handle("/metrics", NewHandler(app, app, segment))
	s.HandleFunc("/probes/ready", statusBoolCheckHandler(s.live))
	s.HandleFunc("/probes/live", statusBoolCheckHandler(s.live))
	go func() {
		if err := http.ListenAndServe(addr, s); err != nil {
			slog.Error("prometheus.ListenAndServe", "err", err)
		}
	}()
	slog.Info("stats_started",
		"metrics", statsURL(addr, "/metrics"),
		"ready", statsURL(addr, "/probes/ready"),
		"live", statsURL(addr, "/probes/live"),
	)
}

func statsURL(addr, path string) string {
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	return "http://" + addr + path
}

func statusBoolCheckHandler(ok *atomic.Bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ok.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		}
	}
}
