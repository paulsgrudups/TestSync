package utils

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// ParseLogLevel turns a configured level name into a [slog.Level]. An empty
// level is INFO, which is what an operator who configured nothing meant.
func ParseLogLevel(name string) (slog.Level, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "":
		return slog.LevelInfo, nil
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	// logrus spelled this WARNING and accepted both; keeping both spellings
	// means an existing configuration file still starts.
	case "WARN", "WARNING":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf(
			"invalid logging.level %q: use DEBUG, INFO, WARN or ERROR", name,
		)
	}
}

// NewLogger builds the process logger described by conf, writing to w.
//
// The default handler is JSON: these logs are read by collectors far more
// often than by people, and one object per entry is what they expect. An
// operator who is tailing the file by eye sets logging.format to "text".
func NewLogger(conf LogConfig, w io.Writer) (*slog.Logger, error) {
	level, err := ParseLogLevel(conf.Level)
	if err != nil {
		return nil, err
	}

	options := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if strings.EqualFold(strings.TrimSpace(conf.Format), LogFormatText) {
		handler = slog.NewTextHandler(w, options)
	} else {
		handler = slog.NewJSONHandler(w, options)
	}

	return slog.New(handler), nil
}

// DiscardLogger returns a logger that writes nothing. Tests and library code
// that has no logger of its own use it rather than a nil pointer, which would
// panic on first use.
func DiscardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
