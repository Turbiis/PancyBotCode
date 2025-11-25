// Package logger provides a structured logging system with multiple outputs.
// It supports console logging, file logging, and Discord webhook notifications.
package logger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Level represents the severity level of a log message.
type Level int

const (
	// CriticalLevel is for critical errors that require immediate attention.
	CriticalLevel Level = iota
	// ErrorLevel is for errors that should be investigated.
	ErrorLevel
	// WarnLevel is for warnings about potential issues.
	WarnLevel
	// SuccessLevel is for successful operations.
	SuccessLevel
	// InfoLevel is for informational messages.
	InfoLevel
	// DebugLevel is for debug information.
	DebugLevel
	// SystemLevel is for system-level messages.
	SystemLevel
)

// String returns the string representation of the log level.
func (l Level) String() string {
	switch l {
	case CriticalLevel:
		return "CRITICAL"
	case ErrorLevel:
		return "ERROR"
	case WarnLevel:
		return "WARN"
	case SuccessLevel:
		return "SUCCESS"
	case InfoLevel:
		return "INFO"
	case DebugLevel:
		return "DEBUG"
	case SystemLevel:
		return "SYSTEM"
	default:
		return "UNKNOWN"
	}
}

// Color returns the ANSI color code for the log level.
func (l Level) Color() string {
	switch l {
	case CriticalLevel:
		return "\033[1;31m" // Bold Red
	case ErrorLevel:
		return "\033[31m" // Red
	case WarnLevel:
		return "\033[33m" // Yellow
	case SuccessLevel:
		return "\033[32m" // Green
	case InfoLevel:
		return "\033[36m" // Cyan
	case DebugLevel:
		return "\033[35m" // Magenta
	case SystemLevel:
		return "\033[34m" // Blue
	default:
		return "\033[0m" // Reset
	}
}

// DiscordColor returns the Discord embed color for the log level.
func (l Level) DiscordColor() int {
	switch l {
	case CriticalLevel, ErrorLevel:
		return 0xFF0000 // Red
	case WarnLevel:
		return 0xFFFF00 // Yellow
	case SuccessLevel:
		return 0x00FF00 // Green
	case InfoLevel:
		return 0x0000FF // Blue
	case DebugLevel:
		return 0x800080 // Purple
	case SystemLevel:
		return 0x808080 // Grey
	default:
		return 0x000000 // Default
	}
}

// Config contains the logger configuration.
type Config struct {
	// LogLevel is the minimum level to log (messages below this level are ignored).
	LogLevel Level
	// LogDir is the directory for log files.
	LogDir string
	// ErrorWebhook is the Discord webhook URL for error notifications.
	ErrorWebhook string
	// LogsWebhook is the Discord webhook URL for general log notifications.
	LogsWebhook string
	// Version is the application version for log footers.
	Version string
	// MaxFileSize is the maximum log file size in bytes before rotation.
	MaxFileSize int64
	// MaxFiles is the maximum number of rotated log files to keep.
	MaxFiles int
}

// DefaultConfig returns a default logger configuration.
func DefaultConfig() Config {
	return Config{
		LogLevel:    SystemLevel,
		LogDir:      "logs",
		MaxFileSize: 500 * 1024 * 1024, // 500MB
		MaxFiles:    5,
		Version:     "0.0.0",
	}
}

// Logger is the main logging instance.
type Logger struct {
	config        Config
	mu            sync.Mutex
	errorFile     *os.File
	combinedFile  *os.File
	webhookClient *http.Client
}

// New creates a new Logger instance with the given configuration.
func New(config Config) (*Logger, error) {
	logger := &Logger{
		config:        config,
		webhookClient: &http.Client{Timeout: 10 * time.Second},
	}

	// Create log directory if it doesn't exist
	if config.LogDir != "" {
		if err := os.MkdirAll(config.LogDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create log directory: %w", err)
		}

		// Open log files
		errorPath := filepath.Join(config.LogDir, "error.log")
		errorFile, err := os.OpenFile(errorPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open error log file: %w", err)
		}
		logger.errorFile = errorFile

		combinedPath := filepath.Join(config.LogDir, "combined.log")
		combinedFile, err := os.OpenFile(combinedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			errorFile.Close()
			return nil, fmt.Errorf("failed to open combined log file: %w", err)
		}
		logger.combinedFile = combinedFile
	}

	return logger, nil
}

// log is the internal logging function.
func (l *Logger) log(level Level, message string, prefix string) {
	if level > l.config.LogLevel {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")

	// Console output with colors
	reset := "\033[0m"
	fmt.Printf("[%s] [%s%s%s] [%s]: %s\n",
		timestamp,
		level.Color(),
		level.String(),
		reset,
		prefix,
		message,
	)

	// File output (without colors)
	fileMessage := fmt.Sprintf("[%s] [%s] [%s]: %s\n",
		timestamp,
		level.String(),
		prefix,
		message,
	)

	l.mu.Lock()
	defer l.mu.Unlock()

	// Write to combined log
	if l.combinedFile != nil {
		l.combinedFile.WriteString(fileMessage)
		l.rotateFileIfNeeded(l.combinedFile, "combined.log")
	}

	// Write to error log (only for error and critical levels)
	if (level == ErrorLevel || level == CriticalLevel) && l.errorFile != nil {
		l.errorFile.WriteString(fileMessage)
		l.rotateFileIfNeeded(l.errorFile, "error.log")
	}

	// Send to Discord webhooks
	go l.sendToWebhook(level, message, prefix)
}

// rotateFileIfNeeded checks file size and rotates if necessary.
func (l *Logger) rotateFileIfNeeded(file *os.File, filename string) {
	if l.config.MaxFileSize <= 0 {
		return
	}

	info, err := file.Stat()
	if err != nil {
		return
	}

	if info.Size() < l.config.MaxFileSize {
		return
	}

	// Close current file
	file.Close()

	// Rotate files
	basePath := filepath.Join(l.config.LogDir, filename)
	for i := l.config.MaxFiles - 1; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", basePath, i)
		newPath := fmt.Sprintf("%s.%d", basePath, i+1)
		os.Rename(oldPath, newPath)
	}
	os.Rename(basePath, basePath+".1")

	// Reopen the file
	newFile, err := os.OpenFile(basePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}

	if strings.Contains(filename, "error") {
		l.errorFile = newFile
	} else {
		l.combinedFile = newFile
	}
}

// DiscordEmbed represents a Discord embed structure.
type DiscordEmbed struct {
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Color       int               `json:"color"`
	Footer      map[string]string `json:"footer,omitempty"`
	Timestamp   string            `json:"timestamp"`
}

// DiscordMessage represents a Discord webhook message.
type DiscordMessage struct {
	Embeds []DiscordEmbed `json:"embeds"`
}

// sendToWebhook sends log messages to Discord webhooks.
func (l *Logger) sendToWebhook(level Level, message string, prefix string) {
	var webhookURL string

	// Determine which webhook to use
	if level == ErrorLevel || level == CriticalLevel {
		webhookURL = l.config.ErrorWebhook
	} else {
		webhookURL = l.config.LogsWebhook
	}

	if webhookURL == "" {
		return
	}

	embed := DiscordEmbed{
		Title:       fmt.Sprintf("[%s] %s", level.String(), prefix),
		Description: fmt.Sprintf("```%s```", message),
		Color:       level.DiscordColor(),
		Footer: map[string]string{
			"text": fmt.Sprintf("💫 Developed by PancyStudio | PancyBot %s", l.config.Version),
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	discordMsg := DiscordMessage{Embeds: []DiscordEmbed{embed}}
	data, err := json.Marshal(discordMsg)
	if err != nil {
		return
	}

	req, err := http.NewRequest("POST", webhookURL, bytes.NewBuffer(data))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	l.webhookClient.Do(req)
}

// Critical logs a critical message.
func (l *Logger) Critical(message string, prefix string) {
	l.log(CriticalLevel, message, prefix)
}

// Error logs an error message.
func (l *Logger) Error(err error, prefix string) {
	if err == nil {
		return
	}
	// Get stack trace
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	stack := string(buf[:n])
	l.log(ErrorLevel, fmt.Sprintf("%s\n%s", err.Error(), stack), prefix)
}

// ErrorMsg logs an error message string.
func (l *Logger) ErrorMsg(message string, prefix string) {
	l.log(ErrorLevel, message, prefix)
}

// Warn logs a warning message.
func (l *Logger) Warn(message string, prefix string) {
	l.log(WarnLevel, message, prefix)
}

// Success logs a success message.
func (l *Logger) Success(message string, prefix string) {
	l.log(SuccessLevel, message, prefix)
}

// Info logs an informational message.
func (l *Logger) Info(message string, prefix string) {
	l.log(InfoLevel, message, prefix)
}

// Debug logs a debug message.
func (l *Logger) Debug(message string, prefix string) {
	l.log(DebugLevel, message, prefix)
}

// System logs a system message.
func (l *Logger) System(message string, prefix string) {
	l.log(SystemLevel, message, prefix)
}

// Close closes all log files.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var errs []error
	if l.errorFile != nil {
		if err := l.errorFile.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if l.combinedFile != nil {
		if err := l.combinedFile.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing log files: %v", errs)
	}
	return nil
}

// SetOutput sets a custom writer for log output (useful for testing).
func (l *Logger) SetOutput(w io.Writer) {
	// This is a placeholder for custom output - would need more implementation
}

// Global logger instance
var defaultLogger *Logger
var once sync.Once

// Init initializes the global logger with the given configuration.
func Init(config Config) error {
	var err error
	once.Do(func() {
		defaultLogger, err = New(config)
	})
	return err
}

// Default returns the global logger instance.
func Default() *Logger {
	if defaultLogger == nil {
		defaultLogger, _ = New(DefaultConfig())
	}
	return defaultLogger
}

// Convenience functions that use the default logger

// Critical logs a critical message using the default logger.
func Critical(message string, prefix string) {
	Default().Critical(message, prefix)
}

// Error logs an error using the default logger.
func Error(err error, prefix string) {
	Default().Error(err, prefix)
}

// ErrorMsg logs an error message using the default logger.
func ErrorMsg(message string, prefix string) {
	Default().ErrorMsg(message, prefix)
}

// Warn logs a warning using the default logger.
func Warn(message string, prefix string) {
	Default().Warn(message, prefix)
}

// Success logs a success message using the default logger.
func Success(message string, prefix string) {
	Default().Success(message, prefix)
}

// Info logs an info message using the default logger.
func Info(message string, prefix string) {
	Default().Info(message, prefix)
}

// Debug logs a debug message using the default logger.
func Debug(message string, prefix string) {
	Default().Debug(message, prefix)
}

// System logs a system message using the default logger.
func System(message string, prefix string) {
	Default().System(message, prefix)
}
