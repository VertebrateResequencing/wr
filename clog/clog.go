/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

// package clog is used to do contextual logging with a global logger.
package clog

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"

	log "github.com/inconshreveable/log15/v3"
	"github.com/sb10/l15h/v2"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	defaultFileRotationMaxSizeMB  = 500
	defaultFileRotationMaxBackups = 3
	defaultFileRotationMaxAgeDays = 28
	defaultFileRotationCompress   = true
	logFilePerm                   = 0o600
)

// osExit is used to disable real os.Exit for testing purposes when calling Fatal.
var osExit = os.Exit //nolint:gochecknoglobals // Fatal tests swap this process-exit hook.

var fileRotationConfigMu sync.RWMutex //nolint:gochecknoglobals // File handlers use process-wide rotation config.

var fileRotationConfig = DefaultFileRotationConfig() //nolint:gochecknoglobals // Updated during command initialization.

var errNoFileHandlerLevels = errors.New("at least one file handler level is required")

// FileRotationConfig controls how file-backed clog handlers rotate log files.
type FileRotationConfig struct {
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	Compress   bool
}

// DefaultFileRotationConfig returns the default log rotation settings.
func DefaultFileRotationConfig() FileRotationConfig {
	return FileRotationConfig{
		MaxSizeMB:  defaultFileRotationMaxSizeMB,
		MaxBackups: defaultFileRotationMaxBackups,
		MaxAgeDays: defaultFileRotationMaxAgeDays,
		Compress:   defaultFileRotationCompress,
	}
}

func currentFileRotationConfig() FileRotationConfig {
	fileRotationConfigMu.RLock()
	defer fileRotationConfigMu.RUnlock()

	return fileRotationConfig
}

// ConfigureFileRotation sets the rotation settings used by new file handlers.
func ConfigureFileRotation(config FileRotationConfig) {
	if config.MaxSizeMB <= 0 {
		config.MaxSizeMB = defaultFileRotationMaxSizeMB
	}

	fileRotationConfigMu.Lock()
	defer fileRotationConfigMu.Unlock()

	fileRotationConfig = config
}

func createRotatingFileHandler(path string) (log.Handler, error) {
	if err := validateFileHandlerPath(path); err != nil {
		return nil, err
	}

	config := currentFileRotationConfig()
	writer := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    config.MaxSizeMB,
		MaxBackups: config.MaxBackups,
		MaxAge:     config.MaxAgeDays,
		Compress:   config.Compress,
	}

	return log.StreamHandler(writer, log.LogfmtFormat()), nil
}

func validateFileHandlerPath(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, logFilePerm)
	if err != nil {
		return err
	}

	return file.Close()
}

// CreateFileHandlersAtLevels returns log15 file handlers for one shared file.
func CreateFileHandlersAtLevels(path string, levels ...string) ([]log.Handler, error) {
	if len(levels) == 0 {
		return nil, errNoFileHandlerLevels
	}

	fh, err := createRotatingFileHandler(path)
	if err != nil {
		return nil, err
	}

	handlers := make([]log.Handler, 0, len(levels))
	for _, lvl := range levels {
		handlers = append(handlers, createFilteredInfoHandler(fh, lvlFromString(lvl)))
	}

	return handlers, nil
}

// init sets our default logging syle.
func init() { //nolint:gochecknoinits // The package intentionally sets a default root logger.
	ToDefault()
}

// ToDefault sets the global logger to log to STDERR at the "warn" level.
func ToDefault() {
	toOutputAtLevel(log.StderrHandler, log.LvlWarn)
}

// ToDefaultAtLevel sets the global logger to log to STDERR at the given level.
func ToDefaultAtLevel(lvl string) {
	toStderrAtLevel(lvlFromString(lvl))
}

// toStderrAtLevel sets the handler of the global logger at the given level to
// log to STDERR.
func toStderrAtLevel(lvl log.Lvl) {
	h := log.StreamHandler(os.Stderr, log.TerminalFormat())
	setRootHandler(log.LvlFilterHandler(lvl, h))
}

// ToHandlerAtLevel sets the default logger to a given custom handler at the
// given level.
// Eg. to log to syslog
// ...
// handler, _ := log15.SyslogHandler(syslog.LOG_USER,
// "wrrunner", log15.LogfmtFormat())
// clog.ToHandlerAtLevel(handler, "info")
// ...
func ToHandlerAtLevel(outputHandler log.Handler, lvl string) {
	toOutputAtLevel(outputHandler, lvlFromString(lvl))
}

// GetHandler returns the global logger handler used for all logging.
func GetHandler() log.Handler {
	return log.Root().GetHandler()
}

// toOutputAtLevel sets the handler of the global logger to filter on the given
// level, add caller info, and output to the given handler.
func toOutputAtLevel(outputHandler log.Handler, lvl log.Lvl) {
	h := createFilteredInfoHandler(outputHandler, lvl)
	setRootHandler(h)
}

// CallerInfoHandler returns a Handler that, at the Debug, Warn and Error
// levels, adds the file and line number of the calling function to the context
// with key "caller". At the Crit levels it instead adds a stack trace to the
// context with key "stack". The stack trace is formatted as a space separated
// list of call sites inside matching []'s. The most recent call site is listed
// first.
func CallerInfoHandler(h log.Handler) log.Handler {
	return l15h.CallerInfoHandler(h)
}

// createFilteredInfoHandler wraps the given output handler in handlers that add
// caller info and filters on the given level.
func createFilteredInfoHandler(outputHandler log.Handler, lvl log.Lvl) log.Handler {
	return CallerInfoHandler(
		log.LvlFilterHandler(
			lvl,
			outputHandler,
		),
	)
}

// setRootHandler sets the given handler as the root handler that all logging
// will use.
func setRootHandler(h log.Handler) {
	log.Root().SetHandler(h)
}

// ToBufferAtLevel sets the global logger to log to the returned
// bytes.Buffer at the given level.
func ToBufferAtLevel(lvl string) *bytes.Buffer {
	buff := new(bytes.Buffer)
	toOutputAtLevel(log.StreamHandler(buff, log.LogfmtFormat()), lvlFromString(lvl))

	return buff
}

// ContextWithFileHandler returns a context that will log to the given file at
// the given level.
func ContextWithFileHandler(ctx context.Context, path, lvl string) (context.Context, error) {
	fh, err := CreateFileHandlerAtLevel(path, lvl)
	if err != nil {
		return nil, err
	}

	return ContextWithLogHandler(ctx, fh), nil
}

// CreateFileHandlerAtLevel returns a log15 file handler at the given level.
func CreateFileHandlerAtLevel(path, lvl string) (log.Handler, error) {
	handlers, err := CreateFileHandlersAtLevels(path, lvl)
	if err != nil {
		return nil, err
	}

	return handlers[0], nil
}

// AddHandler adds the given log15 handler to global logger.
func AddHandler(handler log.Handler) {
	l15h.AddHandler(log.Root(), handler)
}

// ToFileAtLevel sets the global logger to log to a file at the given path
// and at the given level.
func ToFileAtLevel(path, lvl string) error {
	fh, err := CreateFileHandlerAtLevel(path, lvl)
	if err != nil {
		return err
	}

	setRootHandler(fh)

	return nil
}

// lvlFromString returns a log.Lvl for the given string. Valid lvls are
// "debug"|"dbug", "info", "warn", "error"|"eror", "crit". Invalid lvls return
// LvlDebug.
func lvlFromString(lvl string) log.Lvl {
	logLevel, err := log.LvlFromString(lvl)
	if err != nil {
		return log.LvlDebug
	}

	return logLevel
}

// logger returns the global logger with as much context as possible.
func logger(ctx context.Context) log.Logger {
	logger := log.Root()
	if ctx != nil {
		logger = addStringKeyToLogger(ctx, logger, retrySetKey, "retryset")
		logger = addStringKeyToLogger(ctx, logger, retryActivityKey, "retryactivity")
		logger = addIntKeyToLogger(ctx, logger, retryNumKey, "retrynum")
		logger = addStringKeyToLogger(ctx, logger, contextJobKey, "jobkey")
		logger = addStringKeyToLogger(ctx, logger, contextServerID, "serverid")
		logger = addStringKeyToLogger(ctx, logger, contextSchedulerType, "schedulertype")
		logger = addStringKeyToLogger(ctx, logger, contextCloudType, "cloudtype")
		logger = addStringKeyToLogger(ctx, logger, contextCallValue, "callvalue")
		logger = addStringKeyToLogger(ctx, logger, contextServerFlavor, "serverflavor")
		logger = addHandlerToLogger(ctx, logger)
	}

	return logger
}

// addStringKeyToLogger checks if the given string key is set in the logger and
// returns a new logger with that context under the logger key if so.
func addStringKeyToLogger(ctx context.Context, logger log.Logger, key correlationIDType, loggerKey string) log.Logger {
	if val, ok := ctx.Value(key).(string); ok {
		logger = logger.New(loggerKey, val)
	}

	return logger
}

// addIntKeyToLogger checks if the given int key is set in the logger and
// returns a new logger with that context under the logger key if so.
func addIntKeyToLogger(ctx context.Context, logger log.Logger, key correlationIDType, loggerKey string) log.Logger {
	if val, ok := ctx.Value(key).(int); ok {
		logger = logger.New(loggerKey, val)
	}

	return logger
}

// addHandlerToLogger checks if a handler has been set in the context and
// sets the logger's handler to it.
func addHandlerToLogger(ctx context.Context, logger log.Logger) log.Logger {
	if val, ok := ctx.Value(contextLogHandler).(log.Handler); ok {
		logger = logger.New()
		logger.SetHandler(val)
	}

	return logger
}

// Debug logs the given message with context and args to the global logger at
// the debug level. Caller info is included.
func Debug(ctx context.Context, msg string, args ...any) {
	logger(ctx).Debug(msg, args...)
}

// Info logs the given message with context and args to the global logger at
// the info level.
func Info(ctx context.Context, msg string, args ...any) {
	logger(ctx).Info(msg, args...)
}

// Warn logs the given message with context and args to the global logger at
// the warn level. Caller info is included.
func Warn(ctx context.Context, msg string, args ...any) {
	logger(ctx).Warn(msg, args...)
}

// Error logs the given message with context and args to the global logger at
// the error level. Caller info is included.
func Error(ctx context.Context, msg string, args ...any) {
	logger(ctx).Error(msg, args...)
}

// Crit logs the given message with context and args to the global logger at
// the error level. A stack trace is included.
func Crit(ctx context.Context, msg string, args ...any) {
	logger(ctx).Crit(msg, args...)
}

// Fatal logs the given message with context and args to the global logger at
// the error level before exiting. 'fatal' is set true in stack trace.
//
// If the WR_FATAL_EXIT_TEST environment variable is set to 1, we don't
// actually exit.
func Fatal(ctx context.Context, msg string, args ...any) {
	args = append(args, "fatal", true)
	logger(ctx).Crit(msg, args...)

	if os.Getenv("WR_FATAL_EXIT_TEST") == "1" {
		defer func() { osExit = os.Exit }()

		osExit = func(int) {}
	}

	osExit(1)
}
