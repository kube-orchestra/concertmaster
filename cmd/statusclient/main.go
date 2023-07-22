package main

import (
	// mqtt "github.com/eclipse/paho.golang"
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
			if _, err := cm.Subscribe(ctx, &paho.Subscribe{
				Subscriptions: map[string]paho.SubscribeOptions{
					"/my-cluster/+/status": {QoS: 1},
				},
			}); err != nil {
				fmt.Printf("failed to subscribe (%s). This is likely to mean no messages will be received.", err)
				return
			}
			fmt.Println("mqtt subscribed")
		},
		OnConnectError: func(err error) { fmt.Printf("error whilst attempting connection: %v\n", err) },
		ClientConfig: paho.ClientConfig{
			ClientID: "agent-124",
			Router: paho.NewSingleHandlerRouter(func(m *paho.Publish) {
				fmt.Printf("received: %s %s\n", m.Topic, string(m.Payload))
				// h.handle(m)
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
