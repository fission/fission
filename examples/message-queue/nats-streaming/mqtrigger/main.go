package main

import (
	"fmt"
	nats "github.com/nats-io/go-nats-streaming"
	uuid "github.com/satori/go.uuid"
	"log"
	"net/http"
)

const (
	authToken = "defaultFissionAuthToken"
	host      = "nats-streaming.fission"
	clusterID = "fissionMQTrigger"
	topic     = "foobar"
)

func Handler(w http.ResponseWriter, r *http.Request) {
	addr := fmt.Sprintf("nats://%v@%v:4222", authToken, host)
	nc, err := nats.Connect(clusterID, uuid.NewV4().String(), nats.NatsURL(addr))
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Publishing message to topic '%v'\n", topic)

	err = nc.Publish(topic, []byte("dummy"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		log.Printf("error sending message to topic: %v", err.Error())
		return
	}
	nc.Close()

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Publish Success"))
}
