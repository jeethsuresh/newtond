package log

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const DatagramLimit = 1200

type Value struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Event struct {
	Version   int              `json:"version"`
	ID        string           `json:"id"`
	Service   string           `json:"service"`
	Timestamp time.Time        `json:"timestamp"`
	Level     string           `json:"level"`
	Message   string           `json:"message"`
	Params    map[string]Value `json:"params"`
}

func EncodeEvent(service, level string, msg MsgFormat, key []byte) ([]byte, error) {
	if service == "" || len(key) < 32 {
		return nil, errors.New("newtond sender requires a service name and a key of at least 32 bytes")
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	event := Event{
		Version: 1, ID: uuid.NewString(), Service: service, Timestamp: msg.Timestamp.UTC(),
		Level: level, Message: msg.Msg, Params: make(map[string]Value, len(msg.Params)),
	}
	for name, raw := range msg.Params {
		value, err := typedValue(raw)
		if err != nil {
			return nil, err
		}
		event.Params[name] = value
	}
	if msg.Err != nil {
		event.Params["error"] = Value{Type: "string", Value: msg.Err.Error()}
	}
	body, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if len(body)+sha256.Size*2+1 > DatagramLimit {
		return nil, errors.New("newtond event exceeds UDP datagram limit")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return append(append([]byte(hex.EncodeToString(mac.Sum(nil))), '\n'), body...), nil
}

func typedValue(raw any) (Value, error) {
	switch value := raw.(type) {
	case nil:
		return Value{Type: "null"}, nil
	case time.Duration:
		return Value{Type: "duration_ns", Value: strconv.FormatInt(int64(value), 10)}, nil
	case time.Time:
		return Value{Type: "timestamp", Value: value.UTC().Format(time.RFC3339Nano)}, nil
	}
	value := reflect.ValueOf(raw)
	switch value.Kind() {
	case reflect.Bool:
		return Value{Type: "bool", Value: strconv.FormatBool(value.Bool())}, nil
	case reflect.String:
		return Value{Type: "string", Value: value.String()}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return Value{Type: "int", Value: strconv.FormatInt(value.Int(), 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return Value{Type: "uint", Value: strconv.FormatUint(value.Uint(), 10)}, nil
	case reflect.Float32, reflect.Float64:
		if bytes, err := json.Marshal(value.Float()); err == nil {
			return Value{Type: "float", Value: string(bytes)}, nil
		}
	}
	return Value{}, errors.New("unsupported newtond parameter type")
}

// UDPSender queues datagrams without waiting for the receiver. Emit returns false
// when the queue is full or the record cannot be encoded.
type UDPSender struct {
	service string
	key     []byte
	queue   chan []byte
}

func NewUDPSender(ctx context.Context, address, service string, key []byte) (*UDPSender, error) {
	if address == "" || service == "" || len(key) < 32 {
		return nil, errors.New("newtond sender requires address, service and key")
	}
	sender := &UDPSender{service: service, key: append([]byte(nil), key...), queue: make(chan []byte, 256)}
	go sender.send(ctx, address)
	return sender, nil
}

func (s *UDPSender) Emit(level string, msg MsgFormat) bool {
	packet, err := EncodeEvent(s.service, level, msg, s.key)
	if err != nil {
		return false
	}
	select {
	case s.queue <- packet:
		return true
	default:
		return false
	}
}

func (s *UDPSender) send(ctx context.Context, address string) {
	var conn net.Conn
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	dialer := net.Dialer{Timeout: time.Second}
	var retryAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case packet := <-s.queue:
			if conn == nil {
				if time.Now().Before(retryAt) {
					continue
				}
				var err error
				conn, err = dialer.DialContext(ctx, "udp", address)
				if err != nil {
					retryAt = time.Now().Add(time.Second)
					continue
				}
			}
			conn.SetWriteDeadline(time.Now().Add(time.Second))
			if _, err := conn.Write(packet); err != nil {
				conn.Close()
				conn = nil
				retryAt = time.Now().Add(time.Second)
			}
		}
	}
}
