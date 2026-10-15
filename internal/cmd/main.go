// Command demo — runnable-пример мини-приложения для библиотеки aweprometheu.
//
// Что делает:
//   - поднимает сервер статистики: /metrics и пробы /probes/ready,
//     /probes/live на основном порту, плюс те же пробы на отдельных
//     портах live/ready, отвечающие 200/503 на любой путь; все URL
//     печатаются в лог при старте;
//   - логи библиотеки настраиваются отдельно (SetLogger, по умолчанию
//     slog.Default());
//   - InitCTX перехватывает SIGHUP/SIGINT/SIGTERM/SIGQUIT и отменяет
//     корневой контекст — по нему останавливаются воркеры нагрузки;
//   - гонит синтетическую нагрузку: каждое «обращение к сервису» и каждый
//     «SQL-запрос» отмечаются во всех семействах метрик библиотеки.
//
// Запуск и просмотр метрик:
//
//	go run ./internal/cmd
//
// затем открыть http://localhost:9099/metrics (порт меняется через
// DEMO_PORT, порты проб — через DEMO_LIVE_PORT/DEMO_READY_PORT,
// по умолчанию 9100/9101).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	aweprometheu "github.com/dangerousvasil/aweprometheu"
)

func main() {
	// Логи библиотеки — отдельная настройка: по умолчанию она пишет через
	// slog.Default(); SetLogger подменяет логгер (например, на JSON-хендлер
	// или slog.DiscardHandler, чтобы совсем выключить). Вызов должен идти
	// до старта stats-сервера, nil возвращает slog.Default().
	aweprometheu.SetLogger(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	port := envOr("DEMO_PORT", "9099")
	livePort := envOr("DEMO_LIVE_PORT", "9100")
	readyPort := envOr("DEMO_READY_PORT", "9101")

	// Старт сервера статистики: регистрирует все семейства метрик в
	// default-реестре Prometheus и поднимает HTTP-ручки /metrics,
	// /probes/ready и /probes/live (URL печатаются в лог). Имена семейств
	// строятся из имени приложения, недопустимые символы экранируются:
	// demo-app -> demo_app_counter_metrics и т.д. Пробы дополнительно
	// слушают на отдельных портах (live 9100 / ready 9101) и отвечают
	// 200/503 на любой путь — путь задаётся конфигурацией мониторинга.
	aweprometheu.NewOnPorts("demo-app", "demo", ":"+port, ":"+livePort, ":"+readyPort)

	// До этого вызова обе пробы отвечали 503; Ok() — по-прежнему одна
	// команда сразу для live + ready. Раздельные команды OkLive/FailLive
	// и OkReady/FailReady (их демонстрирует воркер 0 в syntheticLoad)
	// меняют только свою пробу.
	aweprometheu.Ok()

	// Корневой контекст процесса: InitCTX подписывается на SIGHUP, SIGINT,
	// SIGTERM и SIGQUIT. По сигналу выставляется IsStop = true и отменяется
	// возвращённый контекст — все, кто ждёт ctx.Done(), завершают работу.
	ctx := aweprometheu.InitCTX()

	go syntheticLoad(ctx, 4)

	slog.Info("demo_started", "pid", os.Getpid(), "hint", "press Ctrl+C to stop")

	// main ждёт только отмены контекста: собственный signal.Notify не нужен,
	// всю работу с сигналами взял на себя InitCTX.
	<-ctx.Done()

	// Последняя запись перед выходом: счётчик A="lifecycle" оставляет в
	// /metrics след того, что завершились штатно, а не упали.
	aweprometheu.Counter("lifecycle", "shutdown")
	slog.Info("demo_stopped", "is_stop", aweprometheu.IsStop.Load())
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var demoQueries = []string{
	`SELECT id, name FROM users WHERE id = $1`,
	`INSERT INTO events(worker, payload) VALUES ($1, $2)`,
	`UPDATE carts SET updated_at = now() WHERE id = $1`,
}

// syntheticLoad — «бизнес-логика» демо: workers воркеров в цикле имитируют
// обработку запроса и SQL-обращение, отмечая каждый шаг в метриках, пока
// InitCTX не отменит контекст по сигналу.
func syntheticLoad(ctx context.Context, workers int) {
	var wg sync.WaitGroup
	for id := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iter := 0; ; iter++ {
				// Точка остановки: воркер не начинает новую итерацию,
				// если контекст уже отменён сигналом.
				select {
				case <-ctx.Done():
					return
				default:
				}

				// Имитация полезной работы «запроса».
				started := time.Now()
				time.Sleep(time.Duration(rand.IntN(5)) * time.Millisecond)

				// Counter: +1 к demo_app_counter_metrics. Лейблы A..D —
				// позиционные аргументы (незаданные = ""), а app="demo-app"
				// и seg="demo" подставляются автоматически из аргументов New.
				// В PromQL темп запросов: rate(demo_app_counter_metrics[1m]).
				aweprometheu.Counter("request", "handle", fmt.Sprintf("worker_%d", id))

				// Gauge: выставляет абсолютное значение (Set, не Inc/Dec)
				// в demo_app_gauge_metrics — здесь условная длина очереди
				// воркера, чтобы в графике была живая «пила» 0…49.
				aweprometheu.Gauge(float64(iter%50), "queue", fmt.Sprintf("worker_%d", id))

				// Histogram: кладёт наблюдение длительности обработки в
				// фиксированные бакеты 0.1/0.25/0.6/1.5 с. Наружу пишет
				// demo_app_histogram_metrics_bucket (с le="…"), _sum и
				// _count — среднее и перцентили считаются на стороне
				// Prometheus через histogram_quantile().
				aweprometheu.Histogram(time.Since(started).Seconds(), "request", "duration")

				// Summary: та же длительность, но квантили p50/p90/p99
				// считаются в самом приложении (objectives 0.5/0.9/0.99):
				// наружу выходят demo_app_summary_metrics{quantile="0.9"}
				// и _sum/_count. Дешевле для Prometheus, но перцентили
				// нельзя агрегировать по лейблам, в отличие от histogram.
				aweprometheu.Summary(time.Since(started).Seconds(), "request", "duration")

				// Error: инкремент того же семейства demo_app_counter_metrics,
				// но лейбл A жёстко задаётся "Error", пользовательские метки
				// идут начиная с B. Каждый 7-й «запрос» падает по таймауту
				// апстрима: A="Error", B="timeout", C="upstream".
				if iter%7 == 0 {
					aweprometheu.Error("timeout", "upstream")
				}

				// Раздельное управление пробами: у воркера 0 каждый 50-й
				// «запрос» на 300 мс снимает готовность. В это время
				// ready-порт и /probes/ready отвечают 503, а live-проба
				// остаётся 200 — команды не влияют друг на друга.
				if id == 0 && iter%50 == 49 {
					aweprometheu.FailReady()
					time.Sleep(300 * time.Millisecond)
					aweprometheu.OkReady()
				}

				// Имитация SQL-обращения: воркер по очереди «выполняет»
				// SELECT / INSERT / UPDATE из demoQueries.
				query := demoQueries[iter%len(demoQueries)]
				queryStart := time.Now()
				time.Sleep(time.Duration(rand.IntN(3)) * time.Millisecond)

				// SetSqlMetrics пишет сразу в три места: +1 к
				// sql_total{sql_query=…, batch=…} (у SQL-метрик свои лейблы,
				// без app/seg), длительность в sql_duration_seconds_{
				// bucket,sum,count} с дефолтными бакетами DefBuckets
				// (5ms…10s) и наблюдение в summary с A="sql". Третий
				// аргумент — выполнялся ли запрос батчем: INSERT у нас
				// «пакетный», остальные — обычные.
				aweprometheu.SetSqlMetrics(query, queryStart, query == demoQueries[1])
			}
		}()
	}
	wg.Wait()
}
