# aweprometheu

Минималистичная обёртка над [prometheus/client_golang](https://github.com/prometheus/client_golang) для Go-сервисов: один вызов поднимает HTTP-сервер со страницей `/metrics` и пробами живости/готовности, а пакетные хелперы пишут во все семейства метрик позиционными лейблами.

## Возможности

- один вызов `New` — подняты `/metrics`, `/probes/live`, `/probes/ready` (URL печатаются в лог);
- хелперы `Counter`, `CounterAdd`, `Error`, `Gauge`, `Histogram`, `Summary` — без ручного построения лейблов: `app`/`seg` подставляются сами, остальные метки передаются по порядку;
- отдельные SQL-метрики: `sql_total` и `sql_duration_seconds` с лейблами `sql_query`, `batch` — одной функцией `SetSqlMetrics`;
- `Ok()`/`Fail()` — переключение обеих проб (live + ready) одной командой, до `Ok()` пробы отвечают 503;
- раздельные команды `OkLive()`/`FailLive()` и `OkReady()`/`FailReady()` — независимое управление пробами;
- опционально пробы live/ready поднимаются на отдельных портах (`NewOnPorts`) — каждый порт отвечает 200/503 на любой путь;
- `InitCTX()` — корневой контекст, отменяемый по SIGHUP/SIGINT/SIGTERM/SIGQUIT, плюс атомарный флаг `IsStop`;
- логирование — отдельная настройка: библиотека везде использует stdlib `log/slog`, по умолчанию `slog.Default()`, подмена — `SetLogger`;
- версия сборки экспортируется метрикой: из переменной окружения `CI_COMMIT_SHORT_SHA` либо из пакетной переменной `Version`.

## Установка

```bash
go get github.com/dangerousvasil/aweprometheu
```

Требуется Go 1.27+.

## Быстрый старт

```go
package main

import (
	"time"

	aweprometheu "github.com/dangerousvasil/aweprometheu"
)

func main() {
	// Поднимает stats-сервер на :9099: /metrics, /probes/ready, /probes/live.
	// До Ok() пробы отвечают 503. Повторный вызов игнорируется (warning в лог).
	aweprometheu.New("my-app", "prod", ":9099")
	// Вариант с отдельными портами проб (live и ready по 200 независимо):
	// aweprometheu.NewOnPorts("my-app", "prod", ":9099", ":9100", ":9101")
	aweprometheu.Ok() // одна команда сразу для live + ready

	// Корневой контекст: отменяется по SIGHUP/SIGINT/SIGTERM/SIGQUIT,
	// при этом выставляется IsStop = true.
	ctx := aweprometheu.InitCTX()

	started := time.Now()
	// ... полезная работа ...

	aweprometheu.Counter("request", "handle")           // +1 к my_app_counter_metrics
	aweprometheu.Error("timeout", "upstream")           // ошибка: A="Error", метки с B
	aweprometheu.Gauge(7, "queue")                      // my_app_gauge_metrics = 7
	aweprometheu.Histogram(0.25, "request", "duration") // бакеты 0.1/0.25/0.6/1.5 с
	aweprometheu.Summary(0.25, "request", "duration")   // квантили p50/p90/p99

	queryStart := time.Now()
	// ... SQL-запрос ...
	aweprometheu.SetSqlMetrics("SELECT 1", queryStart, false) // sql_total + sql_duration_seconds

	<-ctx.Done() // ждём сигнала; после этого IsStop == true
}
```

## Эндпоинты

| Путь | Назначение |
|---|---|
| `/metrics` | экспорт метрик в текстовом формате Prometheus |
| `/probes/live` | liveness-проба: 200 после `Ok()`/`OkLive()`, 503 после `Fail()`/`FailLive()` |
| `/probes/ready` | readiness-проба: 200 после `Ok()`/`OkReady()`, 503 после `Fail()`/`FailReady()` |

При старте через `NewOnPorts` каждая проба дополнительно слушает свой порт (`livePort`, `readyPort`) и отвечает 200/503 на любой путь этого порта — конкретный путь задаётся на стороне мониторинга (например, в k8s-пробе httpGet).

## Метрики

Имена семейств строятся из имени приложения: `New("demo-app", ...)` создаёт `demo_app_counter_metrics` — дефис экранируется в подчёркивание (значения лейблов не экранируются).

| Семейство | Тип | Лейблы | Хелперы |
|---|---|---|---|
| `<app>_counter_metrics` | counter | `app`, `seg`, `A`, `B`, `C`, `D` | `Counter`, `CounterAdd`, `Error` |
| `<app>_gauge_metrics` | gauge | `app`, `seg`, `A`–`D` | `Gauge` |
| `<app>_histogram_metrics` | histogram (бакеты 0.1/0.25/0.6/1.5 с) | `app`, `seg`, `A`–`D` | `Histogram` |
| `<app>_summary_metrics` | summary (p50/p90/p99) | `app`, `seg`, `A`–`D` | `Summary`, `SetSqlMetrics` |
| `sql_total` | counter | `sql_query`, `batch` | `SetSqlMetrics` |
| `sql_duration_seconds` | histogram (бакеты по умолчанию DefBuckets) | `sql_query`, `batch` | `SetSqlMetrics` |

Примеры PromQL:

```promql
rate(demo_app_counter_metrics[1m])
histogram_quantile(0.9, rate(demo_app_histogram_metrics_bucket[5m]))
rate(sql_total{batch="true"}[5m])
```

### Версия

При старте сервера однократно пишется `<app>_counter_metrics{A="Version"}` со значением 100: значением метки становится `CI_COMMIT_SHORT_SHA` из окружения, а если её нет — пакетная переменная `Version` (по умолчанию `"handmade"`).

## API

### Запуск

- `New(app, segment, port string)` — поднимает stats-сервер: `/metrics`, `/probes/live`, `/probes/ready` на одном порту, флаги проб внутренние. Второй вызов любого стартера игнорируется с warning.
- `NewOnPorts(app, segment, port, livePort, readyPort string)` — как `New`, но пробы дополнительно слушают на отдельных портах `livePort` и `readyPort`.
- `Stats(live, ready *atomic.Bool, app, segment, port string)` — то же, что `New`, но флаги проб внешние: их можно переключать напрямую через `Store`, минуя команды ниже.
- `Ok()` / `Fail()` — выставить/сбросить сразу обе пробы (live + ready).
- `OkLive()` / `FailLive()`, `OkReady()` / `FailReady()` — управление только своей пробой.

Все команды проб вызывайте после старта сервера — до него глобальный инстанс ещё nil и вызов паникует.

### Логи

Библиотека пишет через стандартный `log/slog`. По умолчанию — `slog.Default()`; `SetLogger` перенаправляет все сообщения библиотеки (stats-сервер, пробы, сигналы, no-op вызовы хелперов) в свой логгер:

```go
aweprometheu.SetLogger(slog.New(slog.NewJSONHandler(os.Stdout, nil))) // JSON
aweprometheu.SetLogger(slog.New(slog.DiscardHandler))                 // выключить логи библиотеки
```

`SetLogger(nil)` возвращает `slog.Default()`. Вызывайте до старта stats-сервера.

### Запись метрик

Пользовательские метки передаются позиционно в лейблы `A`, `B`, `C`, `D` (незаданные — пустые строки), `app` и `seg` подставляются автоматически. До старта сервера все хелперы безопасны: ничего не пишут, только `slog.Debug`.

- `Counter(labels ...string)` — +1 к `<app>_counter_metrics`, до 4 меток;
- `CounterAdd(inc float64, labels ...string)` — прибавить `inc`;
- `Error(labels ...string)` — +1, лейбл `A` жёстко `"Error"`, пользовательские метки идут с `B` (до 3);
- `Gauge(val float64, labels ...string)` — установить абсолютное значение (Set);
- `Histogram(val float64, labels ...string)` — добавить наблюдение длительности;
- `Summary(val float64, labels ...string)` — добавить наблюдение (квантили считаются в приложении);
- `SetSqlMetrics(sql string, startTime time.Time, batch bool)` — сразу три записи: +1 к `sql_total{sql_query, batch}`, длительность в `sql_duration_seconds` и наблюдение в `<app>_summary_metrics` с `A="sql"`.

### Сигналы и graceful shutdown

- `InitCTX() context.Context` — возвращает корневой контекст и подписывается на SIGHUP, SIGINT, SIGTERM, SIGQUIT. По сигналу выставляется `IsStop = true` и отменяется контекст — все, кто ждёт `ctx.Done()`, завершают работу. Собственный `signal.Notify` в `main` не нужен.
- `IsStop` — `atomic.Bool`: читать можно в любой момент (`IsStop.Load()`), гарантированно `true` — после `<-ctx.Done()`.

## Демо

```bash
go run ./internal/cmd
```

Поднимает мини-приложение: метрики на `http://localhost:9099` (`DEMO_PORT`), пробы дополнительно на отдельных портах `:9100` live (`DEMO_LIVE_PORT`) и `:9101` ready (`DEMO_READY_PORT`). 4 воркера синтетической нагрузки пишут во все семейства метрик, включая ошибки и «SQL-запросы»; воркер 0 периодически снимает и возвращает готовность (`FailReady`/`OkReady`) — при этом ready отвечает 503, а live остаётся 200. Ctrl+C — штатная остановка по контексту от `InitCTX`.

## Тесты

```bash
go test ./...
```

Тесты работают с единственным глобальным инстансом stats-сервера (второй в процессе не поднять), а `TestInitCTX` отправляет настоящую SIGINT тестовому бинарнику — поэтому гоняйте набор целиком, а не отдельные тесты.

## Лицензия

MIT — см. [LICENSE](LICENSE).
