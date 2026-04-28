package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Logger emits structured key=value logs.
type Logger struct {
	mu     sync.Mutex
	out    io.Writer
	fields map[string]string
}

// NewLogger creates a logger writing to the given writer.
func NewLogger(w io.Writer) *Logger {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{out: w, fields: make(map[string]string)}
}

// With returns a new Logger with additional permanent fields.
func (l *Logger) With(kv map[string]string) *Logger {
	l.mu.Lock()
	defer l.mu.Unlock()
	newFields := make(map[string]string, len(l.fields)+len(kv))
	for k, v := range l.fields {
		newFields[k] = v
	}
	for k, v := range kv {
		newFields[k] = v
	}
	return &Logger{out: l.out, fields: newFields}
}

func (l *Logger) log(level string, msg string, kv map[string]string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	parts := []string{
		fmt.Sprintf("time=%s", time.Now().UTC().Format(time.RFC3339)),
		fmt.Sprintf("level=%s", level),
		fmt.Sprintf("msg=%q", msg),
	}
	for k, v := range l.fields {
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	for k, v := range kv {
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	fmt.Fprintln(l.out, strings.Join(parts, " "))
}

func (l *Logger) Info(msg string, kv ...map[string]string) {
	m := mergeMaps(kv...)
	l.log("info", msg, m)
}

func (l *Logger) Warn(msg string, kv ...map[string]string) {
	m := mergeMaps(kv...)
	l.log("warn", msg, m)
}

func (l *Logger) Error(msg string, kv ...map[string]string) {
	m := mergeMaps(kv...)
	l.log("error", msg, m)
}

func mergeMaps(maps ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
