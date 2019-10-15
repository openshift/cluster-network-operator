package pki

import (
	"log"

	"github.com/openshift/library-go/pkg/operator/events"
)

// loggingRecorder is a simple noop recorder that implements the library-go
// events.Recorder interface, but just logs to stdout
type loggingRecorder struct{}

var _ events.Recorder = &loggingRecorder{}

func (r *loggingRecorder) Event(reason, message string) {
	log.Println(message)
}
func (r *loggingRecorder) Eventf(reason, messageFmt string, args ...interface{}) {
	log.Printf(messageFmt, args...)
}
func (r *loggingRecorder) Warning(reason, message string) {
	log.Println(message)
}

func (r *loggingRecorder) Warningf(reason, messageFmt string, args ...interface{}) {
	log.Printf(messageFmt, args...)
}

func (r *loggingRecorder) ForComponent(componentName string) events.Recorder {
	return r
}

func (r *loggingRecorder) WithComponentSuffix(componentNameSuffix string) events.Recorder {
	return r
}

func (r *loggingRecorder) ComponentName() string {
	return "not implemented"
}
