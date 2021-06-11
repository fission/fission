package main

import (
	"log"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	router := os.Getenv("FISSION_ROUTER")
	if len(router) == 0 {
		log.Fatal("FISSION_ROUTER variable is not set")
	}
	funcURL := "ws://" + router + "/fission-function/bs"

	conn, _, err := websocket.DefaultDialer.Dial(funcURL, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Println("read:", err)
				return
			}
			log.Printf("recv: %s", message)
		}
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	stop := time.After(10 * time.Second)
	for i := 0; i < 30; i++ {

		select {
		case <-done:
			return
		case t := <-ticker.C:
			err := conn.WriteMessage(websocket.TextMessage, []byte(t.String()))
			if err != nil {
				log.Fatal("write:", err)
			}
		case <-stop:
			log.Println("Closing")

			// Cleanly close the connection by sending a close message and then
			// waiting (with timeout) for the server to close the connection.
			err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				log.Fatal("write close:", err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			return
		}
	}

}
