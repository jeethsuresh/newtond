package transport

import "fmt"

//TODO coloration is table stakes for loggers these days

type StdoutTransport struct {
}

func (s *StdoutTransport) Send(msg string) error {
	fmt.Println(msg)
	return nil
}
