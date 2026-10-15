package aweprometheu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The package keeps one global stats server and registers its metric families
// in the default Prometheus registry, so the server can only be started once
// per process. The whole suite therefore runs against the single instance
// started by TestMiniAppUnderLoad, in sequential subtests. TestInitCTX reuses
// that instance (via testPort) to check that context-driven worker counters
// reach /metrics; when run standalone (-run TestInitCTX) it skips the scrape.

// The stats server derives metric family names from the app name:
// NewHandler("mini-app", ...) creates mini-app_counter_metrics. promhttp
// serves the legacy text format, where characters invalid in legacy metric
// names (the '-' here) are escaped with underscores, so the exposed family
// is mini_app_counter_metrics. Label values are not escaped.
const testApp = "mini-app"

var (
	counterMetrics         = "mini_app_counter_metrics"
	gaugeMetrics           = "mini_app_gauge_metrics"
	histogramMetrics       = "mini_app_histogram_metrics"
	histogramMetricsCount  = histogramMetrics + "_count"
	histogramMetricsBucket = histogramMetrics + "_bucket"
	summaryMetrics         = "mini_app_summary_metrics"
	summaryMetricsCount    = summaryMetrics + "_count"
)

// testPort запоминается тестом, который поднял stats-сервер, чтобы TestInitCTX
// мог скрейпить тот же инстанс.
var testPort string

func TestMiniAppUnderLoad(t *testing.T) {
	port := freePort(t)
	testPort = port
	livePort := freePort(t)
	readyPort := freePort(t)

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	NewOnPorts("mini-app", "test", port, livePort, readyPort)
	waitHTTP(t, "http://localhost"+port+"/probes/live")
	waitHTTP(t, "http://localhost"+livePort+"/")
	waitHTTP(t, "http://localhost"+readyPort+"/")

	t.Run("startup message lists metrics page URLs", func(t *testing.T) {
		got := logs.String()
		for _, want := range []string{
			"http://localhost" + port + "/metrics",
			statsURL(readyPort, "/"),
			statsURL(livePort, "/"),
		} {
			if !strings.Contains(got, want) {
				t.Errorf("startup log does not mention %q:\n%s", want, got)
			}
		}
	})

	t.Run("probes follow live/ready flags", func(t *testing.T) {
		check := func(want int, targets ...[2]string) {
			t.Helper()
			for _, tg := range targets {
				if code := probe(t, tg[0], tg[1]); code != want {
					t.Errorf("probe %s%s: got %d, want %d", tg[0], tg[1], code, want)
				}
			}
		}

		// До Ok() обе пробы отвечают 503 на любом из своих адресов;
		// отдельный пробовый порт отвечает на любой путь.
		check(http.StatusServiceUnavailable,
			[2]string{port, "/probes/live"},
			[2]string{port, "/probes/ready"},
			[2]string{livePort, "/"},
			[2]string{livePort, "/healthz"},
			[2]string{readyPort, "/"},
		)

		// Ok() — одна команда сразу для live + ready.
		Ok()
		check(http.StatusOK,
			[2]string{port, "/probes/live"},
			[2]string{port, "/probes/ready"},
			[2]string{livePort, "/"},
			[2]string{readyPort, "/"},
		)

		// Раздельные команды действуют только на свою пробу.
		FailLive()
		check(http.StatusServiceUnavailable,
			[2]string{port, "/probes/live"},
			[2]string{livePort, "/anything"},
		)
		check(http.StatusOK,
			[2]string{port, "/probes/ready"},
			[2]string{readyPort, "/"},
		)

		OkLive()
		FailReady()
		check(http.StatusOK,
			[2]string{port, "/probes/live"},
			[2]string{livePort, "/"},
		)
		check(http.StatusServiceUnavailable,
			[2]string{port, "/probes/ready"},
			[2]string{readyPort, "/"},
		)

		OkReady()
		Fail()
		check(http.StatusServiceUnavailable,
			[2]string{port, "/probes/live"},
			[2]string{port, "/probes/ready"},
			[2]string{livePort, "/"},
			[2]string{readyPort, "/"},
		)
		Ok()
	})

	t.Run("mini app under synthetic load", func(t *testing.T) {
		app := startMiniApp(8)
		time.Sleep(300 * time.Millisecond)
		app.Stop()

		// Deterministic samples written after the load has stopped, so their
		// exact values can be compared.
		CounterAdd(42, "check", "post_load")
		Gauge(3.14, "check", "post_load")

		body, contentType := scrape(t, port)
		if !strings.HasPrefix(contentType, "text/plain") {
			t.Errorf("/metrics content type = %q, want text/plain", contentType)
		}
		samples := parseSamples(t, body)

		checkValue(t, samples, counterMetrics, map[string]string{"A": "Version"}, 100)
		checkValue(t, samples, counterMetrics, map[string]string{"A": "check", "B": "post_load"}, 42)
		checkValue(t, samples, gaugeMetrics, map[string]string{"A": "check", "B": "post_load"}, 3.14)

		positive(t, samples, counterMetrics, map[string]string{"app": "mini-app", "seg": "test", "A": "request"}, "handled requests")
		positive(t, samples, counterMetrics, map[string]string{"A": "Error", "B": "timeout"}, "reported errors")
		hasSample(t, samples, gaugeMetrics, map[string]string{"A": "queue"}, "queue gauge")
		positive(t, samples, histogramMetricsCount, map[string]string{"A": "request"}, "histogram observations")
		positive(t, samples, summaryMetricsCount, map[string]string{"A": "request"}, "summary observations")
		hasSample(t, samples, histogramMetricsBucket, map[string]string{"le": "+Inf"}, "histogram +Inf bucket")
		hasSample(t, samples, summaryMetrics, map[string]string{"quantile": "0.9"}, "summary p90 quantile")

		positive(t, samples, "sql_total", map[string]string{"sql_query": testQueries[0], "batch": "false"}, "plain sql requests")
		positive(t, samples, "sql_total", map[string]string{"sql_query": testQueries[1], "batch": "true"}, "batch sql requests")
		positive(t, samples, "sql_duration_seconds_count", map[string]string{"batch": "true"}, "sql durations")
	})

	t.Run("SetLogger routes library logs", func(t *testing.T) {
		var buf bytes.Buffer
		SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
		defer SetLogger(nil)

		// Повторный старт игнорируется и пишет warning — он должен попасть
		// в настроенный логгер, а не в slog.Default().
		New("ignored-app", "ignored", freePort(t))
		if !strings.Contains(buf.String(), "second_attempt_starting_stats") {
			t.Errorf("library warning did not go to the configured logger:\n%s", buf.String())
		}
	})

	t.Run("second Stats/NewOnPorts call is ignored", func(t *testing.T) {
		otherLive, otherReady := freePort(t), freePort(t)
		Stats(new(atomic.Bool), new(atomic.Bool), "other-app", "other", freePort(t))
		NewOnPorts("other-app", "other", freePort(t), otherLive, otherReady)
		if code := probe(t, port, "/metrics"); code != http.StatusOK {
			t.Errorf("/metrics after second start call: got %d, want %d", code, http.StatusOK)
		}
		// Игнорируемый повторный старт не должен поднимать пробовые серверы.
		if !refused(t, otherLive, "/") {
			t.Errorf("ignored NewOnPorts started a live probe server on %s", otherLive)
		}
		if !refused(t, otherReady, "/") {
			t.Errorf("ignored NewOnPorts started a ready probe server on %s", otherReady)
		}
	})
}

// TestInitCTX проверяет обработку сигналов из signal.go: InitCTX возвращает
// корневой контекст, который отменяется (а IsStop выставляется в true) при
// получении SIGHUP/SIGINT/SIGTERM/SIGQUIT. Воркеры, как в демо-приложении
// (internal/cmd), останавливаются по ctx.Done().
func TestInitCTX(t *testing.T) {
	ctx := InitCTX()
	if ctx == nil {
		t.Fatal("InitCTX: возвращён nil-контекст")
	}
	if err := ctx.Err(); err != nil {
		t.Errorf("свежий контекст уже отменён: %v", err)
	}
	if IsStop.Load() {
		t.Error("IsStop = true до получения сигнала")
	}

	// Воркеры образца «контекстного» приложения: крутятся, пока жив контекст,
	// каждую итерацию отмечаясь счётчиком A="signal" (app/seg подставятся
	// из параметров Stats тем же способом, что и в основном тесте).
	var wg sync.WaitGroup
	var iterations atomic.Int64
	for id := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iter := 0; ; iter++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				Counter("signal", "worker", fmt.Sprintf("worker_%d", id))
				iterations.Add(1)
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Ждём, пока воркеры гарантированно сделают хотя бы одну итерацию:
	// без этого на нагруженной машине сигнал может прийти раньше, чем
	// счётчики попадут в реестр, и скрейп ниже не найдёт семплы.
	deadline := time.Now().Add(2 * time.Second)
	for iterations.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if iterations.Load() == 0 {
		t.Fatal("воркеры не сделали ни одной итерации до сигнала")
	}

	// Шлём SIGINT самому себе: InitCTX подписан на него через signal.Notify,
	// поэтому рантайм доставит сигнал в канал, а не завершит тестовый бинарник.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("отправить SIGINT: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("контекст не отменён после сигнала")
	}
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("ctx.Err() = %v, want context.Canceled", err)
	}
	// IsStop — atomic.Bool, читать можно в любой момент; гарантированно
	// true — после <-ctx.Done() выше (Store идёт до отмены контекста).
	if !IsStop.Load() {
		t.Error("IsStop = false после сигнала")
	}

	stopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("воркеры не остановились после отмены контекста")
	}

	// Stats-сервер уже поднят TestMiniAppUnderLoad — проверяем, что счётчики
	// контекстных воркеров реально дошли до /metrics.
	if testPort != "" {
		body, _ := scrape(t, testPort)
		samples := parseSamples(t, body)
		positive(t, samples, counterMetrics, map[string]string{"A": "signal"}, "итерации контекстных воркеров")
	}
}

var testQueries = []string{
	`SELECT id, name FROM users WHERE id = $1`,
	`INSERT INTO events(worker, payload) VALUES ($1, $2)`,
	`UPDATE carts SET updated_at = now() WHERE id = $1`,
}

// miniApp imitates a small service: workers handle "requests" and run "sql",
// reporting every step through the package metric helpers.
type miniApp struct {
	stop chan struct{}
	done sync.WaitGroup
}

func startMiniApp(workers int) *miniApp {
	a := &miniApp{stop: make(chan struct{})}
	for id := range workers {
		a.done.Add(1)
		go a.worker(id)
	}
	return a
}

func (a *miniApp) worker(id int) {
	defer a.done.Done()
	// Воркер останавливается по каналу stop, а не по контексту InitCTX:
	// так длительность нагрузки детерминирована и не зависит от доставки
	// сигнала. Поведение «воркеры слушают ctx.Done()» проверяет TestInitCTX.
	for iter := 0; ; iter++ {
		select {
		case <-a.stop:
			return
		default:
		}

		started := time.Now()
		time.Sleep(time.Duration(rand.IntN(2)) * time.Millisecond)

		// Counter: +1 к mini_app_counter_metrics. Лейблы A..D берутся из
		// аргументов по порядку (незаданные = ""), а app="mini-app" и
		// seg="test" подставляются автоматически из параметров Stats.
		// В PromQL это rate(mini_app_counter_metrics[1m]).
		Counter("request", "handle", fmt.Sprintf("worker_%d", id))

		// Gauge: выставляет абсолютное значение (Set, не Inc/Dec) в
		// mini_app_gauge_metrics — здесь условная длина очереди воркера.
		Gauge(float64(iter%50), "queue", fmt.Sprintf("worker_%d", id))

		// Histogram: кладёт наблюдение длительности итерации в фиксированные
		// бакеты 0.1/0.25/0.6/1.5 с. Пишет mini_app_histogram_metrics_bucket
		// (с le="…"), _sum и _count — среднее и перцентили через
		// histogram_quantile() считаются на стороне Prometheus.
		Histogram(time.Since(started).Seconds(), "request", "duration")

		// Summary: та же длительность, но квантили p50/p90/p99 считаются
		// в самом приложении (objectives 0.5/0.9/0.99): наружу выходят
		// mini_app_summary_metrics{quantile="0.9"} и _sum/_count.
		Summary(time.Since(started).Seconds(), "request", "duration")

		// Error: инкремент того же семейства mini_app_counter_metrics, но
		// лейбл A жёстко задаётся "Error", пользовательские метки идут с B.
		// Каждый 7-й «запрос» завершается ошибкой апстрима.
		if iter%7 == 0 {
			Error("timeout", "upstream")
		}

		query := testQueries[iter%len(testQueries)]
		queryStart := time.Now()
		time.Sleep(time.Duration(rand.IntN(3)) * time.Millisecond)

		// SetSqlMetrics пишет сразу в три места: +1 к
		// sql_total{sql_query=…, batch=…} (свои лейблы, без app/seg),
		// длительность в sql_duration_seconds_{bucket,sum,count} с дефолтными
		// бакетами DefBuckets (5ms…10s) и наблюдение в summary с A="sql".
		// Третий аргумент — выполнялся ли запрос батчем.
		SetSqlMetrics(query, queryStart, query == testQueries[1])
	}
}

func (a *miniApp) Stop() {
	close(a.stop)
	a.done.Wait()
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return ":" + strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func waitHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("stats server did not come up at %s", url)
}

func probe(t *testing.T, port, path string) int {
	t.Helper()
	resp, err := http.Get("http://localhost" + port + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// refused — соединение на адрес должно отклоняться: игнорируемый
// повторный старт библиотеки не должен поднимать новые серверы.
func refused(t *testing.T, port, path string) bool {
	t.Helper()
	resp, err := http.Get("http://localhost" + port + path)
	if err != nil {
		return true
	}
	resp.Body.Close()
	return false
}

func scrape(t *testing.T, port string) (body, contentType string) {
	t.Helper()
	resp, err := http.Get("http://localhost" + port + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics: %v", err)
	}
	return string(b), resp.Header.Get("Content-Type")
}

var labelPattern = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"`)

type sample struct {
	name   string
	labels map[string]string
	value  float64
}

func parseSamples(t *testing.T, body string) []sample {
	t.Helper()
	var samples []sample
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sp := strings.LastIndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(line[sp+1:]), 64)
		if err != nil {
			t.Fatalf("parse metric line %q: %v", line, err)
		}
		head := strings.TrimSpace(line[:sp])
		s := sample{value: value}
		if i := strings.IndexByte(head, '{'); i >= 0 {
			s.name = head[:i]
			s.labels = map[string]string{}
			for _, m := range labelPattern.FindAllStringSubmatch(head, -1) {
				s.labels[m[1]] = m[2]
			}
		} else {
			s.name = head
		}
		samples = append(samples, s)
	}
	return samples
}

// sampleValue sums the values of all samples with the given name whose labels
// contain every wanted pair; a nil map matches only label-less samples.
func sampleValue(samples []sample, name string, labels map[string]string) (float64, bool) {
	var sum float64
	found := false
	for _, s := range samples {
		if s.name != name {
			continue
		}
		if labels == nil && len(s.labels) > 0 {
			continue
		}
		ok := true
		for k, v := range labels {
			if s.labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			sum += s.value
			found = true
		}
	}
	return sum, found
}

func checkValue(t *testing.T, samples []sample, name string, labels map[string]string, want float64) {
	t.Helper()
	got, ok := sampleValue(samples, name, labels)
	if !ok {
		t.Errorf("%s%v: sample not found", name, labels)
		return
	}
	if got != want {
		t.Errorf("%s%v: got %v, want %v", name, labels, got, want)
	}
}

func hasSample(t *testing.T, samples []sample, name string, labels map[string]string, what string) {
	t.Helper()
	if _, ok := sampleValue(samples, name, labels); !ok {
		t.Errorf("%s: no sample %s%v", what, name, labels)
	}
}

func positive(t *testing.T, samples []sample, name string, labels map[string]string, what string) {
	t.Helper()
	v, ok := sampleValue(samples, name, labels)
	if !ok || v <= 0 {
		t.Errorf("%s: no positive value for %s%v (found=%v, sum=%v)", what, name, labels, ok, v)
	}
}

func TestParseSamples(t *testing.T) {
	body := `# HELP sql_total Count of all sql requests
# TYPE sql_total counter
sql_total{sql_query="SELECT id, name FROM users WHERE id = $1",batch="false"} 7
sql_total{sql_query="INSERT INTO events VALUES ($1)",batch="true"} 3

process_start_time_seconds 1.75e+09
`
	samples := parseSamples(t, body)
	checkValue(t, samples, "sql_total", map[string]string{"batch": "false"}, 7)
	checkValue(t, samples, "sql_total", map[string]string{"sql_query": "INSERT INTO events VALUES ($1)"}, 3)
	checkValue(t, samples, "process_start_time_seconds", nil, 1.75e9)
}
