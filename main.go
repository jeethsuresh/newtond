package main

import (
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jeethsuresh/newtond/log"
	"github.com/jeethsuresh/newtond/server"
	"golang.org/x/exp/rand"
)

func main() {
	//if the first arg is "server", start the server. Otherwise, call the startClient function
	if len(os.Args) > 1 && os.Args[1] == "server" {
		server.StartServer()
	} else {
		startClient()
	}

}

// TODO add metrics to this thing, and better representative load
func startClient() {
	//create a new logger with a SocketTransport and Info level
	logger := log.NewLogger(log.Config{Level: log.Info})
	//log a message
	logger.Info(log.MsgFormat{Msg: "Test Message dont delete", Params: map[string]interface{}{"requestID": uuid.New().String()}})
	logger.Info(log.MsgFormat{Msg: "Test Message dont delete", Params: map[string]interface{}{"requestID": uuid.New().String()}})
	logger.Info(log.MsgFormat{Msg: "Test Message dont delete", Params: map[string]interface{}{"requestID": uuid.New().String()}})

	messages := []string{
		"Hello World",
		"Goodbye World",
		"Test Message dont delete",
		"asdf",
		"qwerzxcvqwerasdf",
	}
	for {
		// select a random message from an array
		logger.Info(log.MsgFormat{Msg: messages[rand.Intn(len(messages))], Params: map[string]interface{}{"requestID": uuid.New().String()}})
		fmt.Println("SEND")
		time.Sleep(1 * time.Millisecond)
	}
}
