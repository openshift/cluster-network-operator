package eventrecorder

import (
	"context"
	"log"

	"github.com/openshift/library-go/pkg/operator/events"
)

// LoggingRecorder is a simple noop recorder that implements the library-go
// events.Recorder interface, but just logs to stdout
type LoggingRecorder struct{}

var _ events.Recorder = &LoggingRecorder{}

func (r *LoggingRecorder) Event(reason, message string) {
	log.Println(message)
}
func (r *LoggingRecorder) Eventf(reason, messageFmt string, args ...interface{}) {
	log.Printf(messageFmt, args...)
}
func (r *LoggingRecorder) Warning(reason, message string) {
	log.Println(message)
}

func (r *LoggingRecorder) Warningf(reason, messageFmt string, args ...interface{}) {
	log.Printf(messageFmt, args...)
}

func (r *LoggingRecorder) ForComponent(componentName string) events.Recorder {
	return r
}

func (r *LoggingRecorder) WithComponentSuffix(componentNameSuffix string) events.Recorder {
	return r
}

func (r *LoggingRecorder) ComponentName() string {
	return "not implemented"
}

func (r *LoggingRecorder) Shutdown() {
	//not implemented
}

func (r *LoggingRecorder) WithContext(_ context.Context) events.Recorder {
	return r
}
