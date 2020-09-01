package main

import (
	"log"

	"github.com/go-logr/logr"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
)

func initControllerRuntimeLogging() {
	crlog.SetLogger(&crLogger{})
}

type crLogger struct {
	logInfo bool
}

func (l *crLogger) Info(msg string, keysAndValues ...interface{}) {
	if l.logInfo {
		log.Printf("%s", msg)
	}
}

func (l *crLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	log.Printf("ERROR %v - %s", err, msg)
}

func (l *crLogger) Enabled() bool {
	return true
}

func (l *crLogger) V(level int) logr.InfoLogger {
	return l
}

func (l *crLogger) WithValues(keysAndValues ...interface{}) logr.Logger {
	return l
}

func (l *crLogger) WithName(name string) logr.Logger {
	return &crLogger{logInfo: name == "leader"}
}
