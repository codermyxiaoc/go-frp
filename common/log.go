package common

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

type FileHook struct {
	fileLogger *logrus.Logger
	levels     []logrus.Level
}

func NewFileHook() *FileHook {
	fileLogger := logrus.New()
	fileLogger.SetOutput(&lumberjack.Logger{
		Filename:   SYS_LOG_PATH,
		MaxSize:    1,
		MaxBackups: 2,
		MaxAge:     30,
		Compress:   true,
		LocalTime:  true,
	})
	fileLogger.SetFormatter(&logrus.JSONFormatter{TimestampFormat: "2006-01-02 15:04:05"})

	return &FileHook{
		fileLogger: fileLogger,
		levels:     []logrus.Level{logrus.WarnLevel, logrus.ErrorLevel, logrus.FatalLevel, logrus.PanicLevel},
	}
}

func (hook *FileHook) Fire(entry *logrus.Entry) error {
	hook.fileLogger.WithFields(entry.Data).Log(entry.Level, entry.Message)
	return nil
}

func (hook *FileHook) Levels() []logrus.Level {
	return hook.levels
}

func InitLog() {
	logrus.SetOutput(os.Stdout)
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
		ForceColors:     true,
		CallerPrettyfier: func(f *runtime.Frame) (string, string) {
			filename := filepath.Base(f.File)
			return "", fmt.Sprintf(" %s:%d", filename, f.Line)
		},
	})
	logrus.SetReportCaller(true)
	logrus.SetLevel(logrus.InfoLevel)
	logrus.AddHook(NewFileHook())
}
