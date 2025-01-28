package transport

import (
	"net"
)

type SocketTransport struct {
	Conn net.Conn
}

func NewSocketTransport() *SocketTransport {
	conn, err := net.Dial("tcp", "127.0.0.1:8080")
	if err != nil {
		panic(err)
	}
	return &SocketTransport{Conn: conn}
}

func (s *SocketTransport) Send(msg string) error {
	_, err := s.Conn.Write([]byte(msg + "\n"))
	if err != nil {
		return err
	}
	return nil
}
