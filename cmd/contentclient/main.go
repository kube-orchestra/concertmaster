package main

import (
	// mqtt "github.com/eclipse/paho.golang"
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

func main() {
	brokerURL, _ := url.Parse("tcp://localhost:31320")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := autopaho.ClientConfig{
		BrokerUrls: []*url.URL{brokerURL},
		KeepAlive:  30,
		OnConnectionUp: func(cm *autopaho.ConnectionManager, c *paho.Connack) {
			fmt.Println("mqtt connection up")

			msg, err := os.ReadFile(os.Args[1])
			if err != nil {
				panic(err)
			}

			fmt.Println("publishing /my-cluster/123/content")
			_, err = cm.Publish(ctx, &paho.Publish{
				Topic:   "/my-cluster/123/content",
				QoS:     1,
				Payload: bytes.TrimSpace(msg),
			})
			if err != nil {
				panic(err)
			}
			fmt.Println("published")
			os.Exit(0)
		},
		OnConnectError: func(err error) { fmt.Printf("error whilst attempting connection: %v\n", err) },
		ClientConfig: paho.ClientConfig{
			ClientID: "content-client",
			Router: paho.NewSingleHandlerRouter(func(m *paho.Publish) {
				fmt.Printf("received: %s %s\n", m.Topic, string(m.Payload))
			}),
			OnClientError: func(err error) { fmt.Printf("server requested disconnect: %v\n", err) },
			OnServerDisconnect: func(d *paho.Disconnect) {
				if d.Properties != nil {
					fmt.Printf("server requested disconnect: %s\n", d.Properties.ReasonString)
				} else {
					fmt.Printf("server requested disconnect; reason code: %d\n", d.ReasonCode)
				}
			},
		},
	}
	cfg.SetUsernamePassword("admin", []byte("password"))

	cm, err := autopaho.NewConnection(ctx, cfg)
	if err != nil {
		panic(err)
	}

	// Messages will be handled through the callback so we really just need to wait until a shutdown
	// is requested
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	signal.Notify(sig, syscall.SIGTERM)

	<-sig
	fmt.Println("signal caught - exiting")

	// We could cancel the context at this point but will call Disconnect instead (this waits for autopaho to shutdown)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = cm.Disconnect(ctx)

	fmt.Println("shutdown complete")
}
