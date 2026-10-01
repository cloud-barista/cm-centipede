package logger

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/cloud-barista/cm-centipede/pkg/config"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

// consoleLevelLabels replaces zerolog's three-letter console labels
// ("INF", "WRN") with bracketed full names ("[ INFO ]", "[ WARN ]").
// ConsoleWriter reads the label from this map and colours it as usual, so the
// per-level colouring is preserved. Only console output is affected; the log
// file stays JSON with the standard "level" field.
var consoleLevelLabels = map[zerolog.Level]string{
	zerolog.TraceLevel: "[ TRACE ]",
	zerolog.DebugLevel: "[ DEBUG ]",
	zerolog.InfoLevel:  "[ INFO ]",
	zerolog.WarnLevel:  "[ WARN ]",
	zerolog.ErrorLevel: "[ ERROR ]",
	zerolog.FatalLevel: "[ FATAL ]",
	zerolog.PanicLevel: "[ PANIC ]",
}

// Init initialises the global zerolog logger based on the loaded configuration.
func Init() {
	cfg := config.Conf

	level := parseLevel(cfg.Loglevel)
	zerolog.SetGlobalLevel(level)
	zerolog.TimeFieldFormat = time.RFC3339
	zerolog.FormattedLevels = consoleLevelLabels

	var writers []io.Writer

	switch strings.ToLower(cfg.Logwriter) {
	case "file":
		writers = append(writers, fileWriter(cfg.Logfile))
	case "stdout":
		writers = append(writers, consoleWriter())
	default: // "both" or any unrecognised value
		writers = append(writers, fileWriter(cfg.Logfile), consoleWriter())
	}

	multi := zerolog.MultiLevelWriter(writers...)
	log.Logger = zerolog.New(multi).With().Timestamp().Caller().Logger()
}

func fileWriter(cfg config.LogfileConfig) io.Writer {
	return &lumberjack.Logger{
		Filename:   cfg.Path,
		MaxSize:    cfg.MaxSize,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAge,
		Compress:   true,
	}
}

func consoleWriter() io.Writer {
	return zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
}

func parseLevel(s string) zerolog.Level {
	switch strings.ToLower(s) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "info":
		return zerolog.InfoLevel
	case "warn":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "fatal":
		return zerolog.FatalLevel
	default:
		return zerolog.InfoLevel
	}
}
