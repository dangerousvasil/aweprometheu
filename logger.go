package aweprometheu

import "log/slog"

// logger — логгер библиотеки, выставляется SetLogger. nil означает
// поведение по умолчанию: slog.Default() в момент вызова.
var logger *slog.Logger

// SetLogger задаёт логгер, которым пишет библиотека (stats-сервер,
// пробы, обработка сигналов, no-op вызовы хелперов метрик). По
// умолчанию используется slog.Default(). Nil возвращает значение по
// умолчанию. Вызывайте до старта stats-сервера.
func SetLogger(l *slog.Logger) {
	logger = l
}

// lg возвращает настроенный логгер либо slog.Default().
func lg() *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.Default()
}
