# aweprometheu

Минималистичная обёртка над [prometheus/client_golang](https://github.com/prometheus/client_golang) для Go-сервисов: один вызов поднимает HTTP-сервер со страницей `/metrics` и пробами живости/готовности, а пакетные хелперы пишут во все семейства метрик позиционными лейблами.

## Возможности

- один вызов `New` — подняты `/metrics`, `/probes/live`, `/probes/ready` (URL печатаются в лог);
- хелперы `Counter`, `CounterAdd`, `Error`, `Gauge`, `Histogram`, `Summary` — без ручного построения лейблов: `app`/`seg` подставляются сами, остальные метки передаются по порядку;
- отдельные SQL-метрики: `sql_total` и `sql_duration_seconds` с лейблами `sql_query`, `batch` — одной функцией `SetSqlMetrics`;
- `Ok()`/`Fail()` — переключение проб readiness/liveness (до `Ok()` пробы отвечают 503);
- `InitCTX()` — корневой контекст, отменяемый по SIGHUP/SIGINT/SIGTERM/SIGQUIT, плюс флаг `IsStop`;
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
	aweprometheu.Ok()

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
| `/probes/live` | liveness-проба: 200 после `Ok()`, 503 после `Fail()` |
| `/probes/ready` | readiness-проба: тот же флаг |

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

- `New(app, segment, port string)` — поднимает stats-сервер с внутренним флагом живости. Второй вызов (или `Stats` после `New`) игнорируется с warning.
- `Stats(live *atomic.Bool, app, segment, port string)` — то же, но флаг живости внешний: его можно переключать напрямую через `Store`, минуя `Ok`/`Fail`.
- `Ok()` / `Fail()` — выставить/сбросить флаг живости. Вызывать только после старта сервера — до него глобальный инстанс ещё nil и вызов паникует.

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
- `IsStop` читайте только после `<-ctx.Done()` — запись флага упорядочена с закрытием канала Done.

## Демо

```bash
go run ./internal/cmd
```

Поднимает мини-приложение на `http://localhost:9099` (порт меняется через `DEMO_PORT`): 4 воркера синтетической нагрузки пишут во все семейства метрик, включая ошибки и «SQL-запросы»; Ctrl+C — штатная остановка по контексту от `InitCTX`.

## Тесты

```bash
go test ./...
```

Тесты работают с единственным глобальным инстансом stats-сервера (второй в процессе не поднять), а `TestInitCTX` отправляет настоящую SIGINT тестовому бинарнику — поэтому гоняйте набор целиком, а не отдельные тесты.

## Лицензия

MIT — см. [LICENSE](LICENSE).
