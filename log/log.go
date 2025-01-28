package log

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeethsuresh/newtond/transport"
)

//TODO split up this package

type Level int

const (
	_ Level = iota
	Trace
	Debug
	Info
	Warn
	Error
)

type Logger struct {
	Level     Level
	Transport transport.Transport
}

type Config struct {
	Level Level
}

type MsgFormat struct {
	Msg       string                 `json:"msg"`
	Err       error                  `json:"err"`
	Params    map[string]interface{} `json:"params"`
	Timestamp time.Time              `json:"timestamp"`
}

func (m MsgFormat) String() string {
	finalString := "|| " + m.Timestamp.Format("2006-01-02 15:04:05") + " || " + m.Msg
	if m.Err != nil {
		finalString = fmt.Sprintf("%s ||[ ERROR] %v ||", finalString, m.Err.Error())
	}
	if m.Params != nil {
		for k, v := range m.Params {
			finalString = fmt.Sprintf("%s || %s=%v ||", finalString, k, v)
		}
	}
	return finalString
}

func (m MsgFormat) Metric() []byte {
	metricJSON := map[string]interface{}{
		"name":   m.Msg,
		"params": m.Params,
	}
	byteJSON, err := json.Marshal(metricJSON)
	if err != nil {
		return []byte("{\"error\":\"" + err.Error() + "\"}")
	}
	return byteJSON
}

func (m MsgFormat) JSON() []byte {
	bytes, err := json.Marshal(m)
	if err != nil {
		return []byte("{\"error\":\"" + err.Error() + "\"}")
	}
	return bytes
}

// TODO configuration
func NewLogger(logConfig Config) *Logger {
	return &Logger{
		Level:     logConfig.Level,
		Transport: transport.NewSocketTransport(),
	}
}

func (l Logger) Trace(msg MsgFormat) {
	_ = l.Transport.Send(string(msg.Metric()))
}

func (l Logger) Debug(msg MsgFormat) {
	_ = l.Transport.Send(string(msg.Metric()))
	if l.Level <= Debug {
		err := l.Transport.Send(fmt.Sprintf("%s %s", "[DEBUG]", msg.String()))
		if err != nil {
			fmt.Println(err)
		}
	}
}

func (l Logger) Info(msg MsgFormat) {
	_ = l.Transport.Send(string(msg.Metric()))
	if l.Level <= Info {
		err := l.Transport.Send(fmt.Sprintf("%s %s", "[INFO]", msg.String()))
		if err != nil {
			fmt.Println(err)
		}
	}
}

func (l Logger) Warn(msg MsgFormat) {
	_ = l.Transport.Send(string(msg.Metric()))
	if l.Level <= Warn {
		err := l.Transport.Send(fmt.Sprintf("%s %s", "[WARN]", msg.String()))
		if err != nil {
			fmt.Println(err)
		}
	}
}

func (l Logger) Error(msg MsgFormat) {
	_ = l.Transport.Send(string(msg.Metric()))
	if l.Level <= Error {
		err := l.Transport.Send(fmt.Sprintf("%s %s", "[ERROR]", msg.String()))
		if err != nil {
			fmt.Println(err)
		}
	}
}
