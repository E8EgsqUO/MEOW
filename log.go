package main

// This logging trick is learnt from a post by Rob Pike
// https://groups.google.com/d/msg/golang-nuts/gU7oQGoCkmg/j3nNxuS2O_sJ

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
)

type infoLogging bool
type debugLogging bool
type errorLogging bool
type requestLogging bool
type responseLogging bool

const defaultDebugLogFile = "debug.log"

var (
	info   infoLogging
	debug  debugLogging
	errl   errorLogging
	dbgRq  requestLogging
	dbgRep responseLogging

	logFile io.Writer

	// make sure logger can be called before initLog
	errorLog    = log.New(os.Stdout, "[ERROR] ", log.LstdFlags)
	debugLog    = log.New(os.Stdout, "[DEBUG] ", log.LstdFlags)
	requestLog  = log.New(os.Stdout, "[>>>>>] ", log.LstdFlags)
	responseLog = log.New(os.Stdout, "[<<<<<] ", log.LstdFlags)

	verbose  bool
	colorize bool
)

func init() {
	flag.BoolVar((*bool)(&info), "info", true, "info log")
	flag.BoolVar((*bool)(&debug), "debug", false, "debug log; writes to ./debug.log unless logFile is configured")
	flag.BoolVar((*bool)(&errl), "err", true, "error log")
	flag.BoolVar((*bool)(&dbgRq), "request", false, "request log")
	flag.BoolVar((*bool)(&dbgRep), "reply", false, "reply log")
	flag.BoolVar(&verbose, "v", false, "more info in request/response logging")
	flag.BoolVar(&colorize, "color", false, "colorize log output")
}

func configuredLogFile() string {
	if config.LogFile != "" {
		return expandTilde(config.LogFile)
	}
	if debug {
		return defaultDebugLogFile
	}
	return ""
}

func coloredLogPrefix(code, prefix string) string {
	if !colorize {
		return prefix
	}
	return "\x1b[" + code + "m" + prefix + "\x1b[0m"
}

func initLog() {
	logFile = os.Stdout
	if logPath := configuredLogFile(); logPath != "" {
		if f, err := os.OpenFile(logPath,
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err != nil {
			fmt.Printf("Can't open log file, logging to stdout: %v\n", err)
		} else {
			logFile = f
		}
	}
	log.SetOutput(logFile)
	errorLog = log.New(logFile, coloredLogPrefix("31", "[ERROR] "), log.LstdFlags)
	debugLog = log.New(logFile, coloredLogPrefix("34", "[DEBUG] "), log.LstdFlags)
	requestLog = log.New(logFile, coloredLogPrefix("32", "[>>>>>] "), log.LstdFlags)
	responseLog = log.New(logFile, coloredLogPrefix("33", "[<<<<<] "), log.LstdFlags)
}

func (d infoLogging) Printf(format string, args ...interface{}) {
	if d {
		log.Printf(format, args...)
	}
}

func (d infoLogging) Println(args ...interface{}) {
	if d {
		log.Println(args...)
	}
}

func (d debugLogging) Printf(format string, args ...interface{}) {
	if d {
		debugLog.Printf(format, args...)
	}
}

func (d debugLogging) Println(args ...interface{}) {
	if d {
		debugLog.Println(args...)
	}
}

func (d errorLogging) Printf(format string, args ...interface{}) {
	if d {
		errorLog.Printf(format, args...)
	}
}

func (d errorLogging) Println(args ...interface{}) {
	if d {
		errorLog.Println(args...)
	}
}

func (d requestLogging) Printf(format string, args ...interface{}) {
	if d {
		requestLog.Printf(format, args...)
	}
}

func (d responseLogging) Printf(format string, args ...interface{}) {
	if d {
		responseLog.Printf(format, args...)
	}
}

func Fatal(args ...interface{}) {
	reportCriticalError(fmt.Sprintln(args...))
	fmt.Println(args...)
	os.Exit(1)
}

func Fatalf(format string, args ...interface{}) {
	reportCriticalError(fmt.Sprintf(format, args...))
	fmt.Printf(format, args...)
	os.Exit(1)
}

// criticalf remains visible on the console build and also gives the Windows
// GUI build somewhere useful to report failures that happen before a listener
// is ready (for example, an occupied port or an invalid TLS certificate).
func criticalf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	reportCriticalError(msg)
	fmt.Println(msg)
}
