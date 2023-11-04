package azfunctions

import (
	"io"

	"github.com/altipla-consulting/env"
	"github.com/altipla-consulting/errors"
	log "github.com/sirupsen/logrus"
)

func init() {
	if env.IsLocal() {
		log.SetFormatter(&log.TextFormatter{
			ForceColors: true,
		})
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetFormatter(new(log.JSONFormatter))
	}
}

type logInterceptor struct {
	logs []string
}

func (logger *logInterceptor) Levels() []log.Level {
	return log.AllLevels
}

func (logger *logInterceptor) Fire(entry *log.Entry) error {
	s, err := entry.String()
	if err != nil {
		return errors.Trace(err)
	}
	logger.logs = append(logger.logs, s)
	return nil
}

func createLogger(level log.Level, funcName string) (*log.Entry, *logInterceptor) {
	logger := log.New()
	interceptor := new(logInterceptor)
	logger.AddHook(interceptor)
	logger.Level = level

	if env.IsLocal() {
		logger.Formatter = &log.TextFormatter{
			ForceColors: true,
		}
	} else {
		logger.Formatter = new(log.JSONFormatter)
		logger.Out = io.Discard
	}

	return logger.WithField("function", funcName), interceptor
}
