package aweprometheu

import (
	"net/http"
	"strings"
	"sync/atomic"
)

var instance *statServer

type statServer struct {
	*http.ServeMux
	live  *atomic.Bool
	ready *atomic.Bool
}

// New поднимает stats-сервер: /metrics, /probes/live и /probes/ready
// на одном порту, с внутренними флагами проб.
func New(app, segment, port string) {
	if instance != nil {
		lg().Warn("second_attempt_starting_stats")
		return
	}
	instance = newStats(new(atomic.Bool), new(atomic.Bool))
	instance.Run(app, segment, port, "", "")
}

// NewOnPorts — как New, но пробы дополнительно слушают на отдельных
// портах: live на livePort, ready на readyPort. Пробовый порт отвечает
// 200/503 на любой путь, поэтому путь настраивается на стороне
// мониторинга.
func NewOnPorts(app, segment, port, livePort, readyPort string) {
	if instance != nil {
		lg().Warn("second_attempt_starting_stats")
		return
	}
	instance = newStats(new(atomic.Bool), new(atomic.Bool))
	instance.Run(app, segment, port, livePort, readyPort)
}

// Stats поднимает stats-сервер на внешних флагах проб: их можно
// переключать напрямую через Store, минуя команды Ok/Fail.
func Stats(live, ready *atomic.Bool, app, segment, port string) {
	if instance != nil {
		lg().Warn("second_attempt_starting_stats")
		return
	}
	instance = newStats(live, ready)
	instance.Run(app, segment, port, "", "")
}

func newStats(live, ready *atomic.Bool) *statServer {
	return &statServer{
		ServeMux: http.NewServeMux(),
		live:     live,
		ready:    ready,
	}
}

func (s *statServer) Ok() {
	s.live.Store(true)
	s.ready.Store(true)
}
func (s *statServer) Fail() {
	s.live.Store(false)
	s.ready.Store(false)
}
func (s *statServer) OkLive()    { s.live.Store(true) }
func (s *statServer) FailLive()  { s.live.Store(false) }
func (s *statServer) OkReady()   { s.ready.Store(true) }
func (s *statServer) FailReady() { s.ready.Store(false) }

// Ok выставляет сразу обе пробы (live + ready).
func Ok() { instance.Ok() }

// Fail сбрасывает сразу обе пробы (live + ready).
func Fail() { instance.Fail() }

// OkLive выставляет только live-пробу.
func OkLive() { instance.OkLive() }

// FailLive сбрасывает только live-пробу.
func FailLive() { instance.FailLive() }

// OkReady выставляет только ready-пробу.
func OkReady() { instance.OkReady() }

// FailReady сбрасывает только ready-пробу.
func FailReady() { instance.FailReady() }

func (s *statServer) Run(app, segment, port, livePort, readyPort string) {
	addr := normalizePort(port)

	s.Handle("/metrics", NewHandler(app, app, segment))
	s.HandleFunc("/probes/ready", statusBoolCheckHandler(s.ready))
	s.HandleFunc("/probes/live", statusBoolCheckHandler(s.live))

	// Пробы остаются и на общем порту; отдельные порты — дополнительные,
	// в лог печатается адрес каждой пробы.
	liveURL := statsURL(addr, "/probes/live")
	readyURL := statsURL(addr, "/probes/ready")
	if livePort != "" {
		startProbeServer("live", livePort, s.live)
		liveURL = statsURL(normalizePort(livePort), "/")
	}
	if readyPort != "" {
		startProbeServer("ready", readyPort, s.ready)
		readyURL = statsURL(normalizePort(readyPort), "/")
	}

	go func() {
		if err := http.ListenAndServe(addr, s); err != nil {
			lg().Error("prometheus.ListenAndServe", "err", err)
		}
	}()
	lg().Info("stats_started",
		"metrics", statsURL(addr, "/metrics"),
		"ready", readyURL,
		"live", liveURL,
	)
}

func normalizePort(port string) string {
	if strings.Contains(port, ":") {
		return port
	}
	return ":" + port
}

// startProbeServer поднимает отдельный сервер пробы: порт отвечает
// 200 при выставленном флаге и 503 иначе, независимо от пути.
func startProbeServer(name, port string, ok *atomic.Bool) {
	addr := normalizePort(port)
	go func() {
		if err := http.ListenAndServe(addr, statusBoolCheckHandler(ok)); err != nil {
			lg().Error("probe.ListenAndServe", "probe", name, "addr", addr, "err", err)
		}
	}()
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
