package logs

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dimonomid/clock"
)

// LogLevel defines supported log levels.
type LogLevel int

// Exported constants for log levels.
const (
	Debug LogLevel = iota
	Info
	Warning
	Error
)

// ParseLogLevel parses a human-readable log level name.
func ParseLogLevel(value string) (LogLevel, error) {
	switch value {
	case "debug":
		return Debug, nil
	case "info":
		return Info, nil
	case "warn", "warning":
		return Warning, nil
	case "error":
		return Error, nil
	default:
		return 0, fmt.Errorf("invalid log level %q; want debug, info, warning, or error", value)
	}
}

// Logger writes leveled messages to its configured sinks.
type Logger struct {
	params LoggerParams

	namespace string
	ctx       map[string]string
}

type LoggerParams struct {
	Sinks []LoggerSinkParams
	Clock clock.Clock
}

type LoggerSinkParams struct {
	// Filepath is where to store the logs. "" or "-" means stdout.
	// The "%t" in Filepath gets replaced with the time formatted with
	// FilepathTimefmt below.
	Filepath        string
	FilepathTimefmt string

	// MinLevel is the minimum log level for this sink.
	MinLevel LogLevel
}

// NewLogger creates a Logger with the specified sinks.
func NewLogger(params LoggerParams) *Logger {
	return &Logger{params: params}
}

func (l *Logger) WithNamespaceAppended(namespace string) *Logger {
	newNamespace := l.namespace
	if newNamespace != "" {
		newNamespace += "/"
	}

	newNamespace += namespace

	return &Logger{
		params:    l.params,
		ctx:       l.ctx,
		namespace: newNamespace,
	}
}

func (l *Logger) WithContext(key, value string) *Logger {
	newCtx := make(map[string]string, len(l.ctx)+1)
	for k, v := range l.ctx {
		newCtx[k] = v
	}
	newCtx[key] = value

	return &Logger{
		params:    l.params,
		namespace: l.namespace,
		ctx:       newCtx,
	}
}

// Log writes a formatted message to every sink whose minimum level permits it.
func (l *Logger) Log(level LogLevel, format string, args ...interface{}) {
	now := l.params.Clock.Now().UTC()
	timestamp := now.Format("2006-01-02 15:04:05.000")
	message := fmt.Sprintf(format, args...)
	levelStr := levelToString(level)

	namespaceStr := ""
	if l.namespace != "" {
		namespaceStr = fmt.Sprintf("[%s] ", l.namespace)
	}

	contextStr := ""
	if len(l.ctx) > 0 {
		var sb strings.Builder
		sb.WriteString(" (")

		keys := make([]string, 0, len(l.ctx))
		for k := range l.ctx {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		for i, k := range keys {
			if i != 0 {
				sb.WriteString(", ")
			}

			sb.WriteString(k)
			sb.WriteString(":")
			sb.WriteString(l.ctx[k])
		}

		sb.WriteString(")")

		contextStr = sb.String()
	}

	for _, sink := range l.params.Sinks {
		if level < sink.MinLevel {
			continue
		}

		var w io.Writer

		switch sink.Filepath {
		case "", "-":
			w = os.Stdout
		default:
			t := now.Format(sink.FilepathTimefmt)
			filepath := strings.Replace(sink.Filepath, "%t", t, -1)

			var err error
			w, err = os.OpenFile(filepath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
			if err != nil {
				message = fmt.Sprintf("FAILED TO PRINT LOGS TO FILE %s: %s %s", filepath, err, message)
				w = os.Stdout
				continue
			}
		}

		fmt.Fprintf(w, "%s [%s] %s%s%s\n", timestamp, levelStr, namespaceStr, message, contextStr)
	}
}

// levelToString maps LogLevel to its compact output representation.
func levelToString(level LogLevel) string {
	switch level {
	case Debug:
		return "D"
	case Info:
		return "I"
	case Warning:
		return "W"
	case Error:
		return "E"
	default:
		return fmt.Sprintf("invalid:%v", level)
	}
}
