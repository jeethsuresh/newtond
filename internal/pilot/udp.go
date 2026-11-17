package pilot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	slog "log/slog"
	"net"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jeethsuresh/newtond/log"
)

var serviceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var paramName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,47}$`)

func DecodeKeys(raw string) (map[string][]byte, error) {
	var config map[string]string
	if err := json.Unmarshal([]byte(raw), &config); err != nil || len(config) == 0 {
		return nil, errors.New("NEWTOND_UDP_KEYS must be a JSON object with at least one service key")
	}
	keys := make(map[string][]byte, len(config))
	for service, secret := range config {
		if !serviceName.MatchString(service) || len(secret) < 32 {
			return nil, fmt.Errorf("invalid UDP key configuration for service %q", service)
		}
		keys[service] = []byte(secret)
	}
	return keys, nil
}

func decode(packet []byte, keys map[string][]byte) (log.Event, bool) {
	var event log.Event
	if len(packet) > log.DatagramLimit || len(packet) <= 65 || packet[64] != '\n' {
		return event, false
	}
	if err := json.Unmarshal(packet[65:], &event); err != nil {
		return event, false
	}
	key := keys[event.Service]
	if len(key) == 0 {
		return event, false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(packet[65:])
	expected := make([]byte, sha256.Size*2)
	hex.Encode(expected, mac.Sum(nil))
	if !hmac.Equal(expected, packet[:64]) {
		return event, false
	}
	if _, err := uuid.Parse(event.ID); err != nil || len(event.ID) != 36 || event.Version != 1 ||
		event.Timestamp.IsZero() || len(event.Message) == 0 || len(event.Message) > 180 ||
		len(event.Params) > 16 {
		return event, false
	}
	switch event.Level {
	case "trace", "debug", "info", "warn", "error":
	default:
		return event, false
	}
	for name, value := range event.Params {
		if !paramName.MatchString(name) || !validValue(value) ||
			(name == "request_id" && (value.Type != "string" || len(value.Value) > 128)) {
			return event, false
		}
	}
	return event, true
}

func validValue(value log.Value) bool {
	if len(value.Value) > 512 {
		return false
	}
	switch value.Type {
	case "string":
		return true
	case "null":
		return value.Value == ""
	case "int", "duration_ns":
		_, err := strconv.ParseInt(value.Value, 10, 64)
		return err == nil
	case "uint":
		_, err := strconv.ParseUint(value.Value, 10, 64)
		return err == nil
	case "float":
		_, err := strconv.ParseFloat(value.Value, 64)
		return err == nil && json.Valid([]byte(value.Value))
	case "bool":
		return value.Value == "true" || value.Value == "false"
	case "timestamp":
		_, err := time.Parse(time.RFC3339Nano, value.Value)
		return err == nil
	default:
		return false
	}
}

func ListenUDP(address string) (*net.UDPConn, error) {
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	if addr.IP.IsMulticast() {
		return net.ListenMulticastUDP("udp", nil, addr)
	}
	return net.ListenUDP("udp", addr)
}

func Receive(ctx context.Context, conn *net.UDPConn, store *Store, keys map[string][]byte) {
	packets := make(chan []byte, 1024)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	go func() {
		defer close(packets)
		buf := make([]byte, log.DatagramLimit+1)
		var drops int
		last := time.Now()
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("UDP receive failed", "error", err)
				}
				return
			}
			if len(packets) == cap(packets) {
				drops++
			} else {
				packets <- bytes.Clone(buf[:n])
			}
			if drops > 0 && time.Since(last) >= time.Minute {
				slog.Warn("UDP receiver queue full", "dropped", drops)
				drops, last = 0, time.Now()
			}
		}
	}()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	batch := make([]log.Event, 0, 64)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := store.Ingest(ctx, batch, time.Now()); err != nil && ctx.Err() == nil {
			slog.Error("UDP batch could not be stored", "events", len(batch), "error", err)
		}
		clear(batch)
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			return
		case packet, ok := <-packets:
			if !ok {
				flush()
				return
			}
			if event, authentic := decode(packet, keys); authentic {
				batch = append(batch, event)
			}
			if len(batch) == cap(batch) {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}
