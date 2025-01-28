package transport

type Transport interface {
	Send(msg string) error
	// TODO: MetricSend()
}
