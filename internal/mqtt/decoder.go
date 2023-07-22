package mqtt

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/eclipse/paho.golang/paho"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"

	mqttv1 "gitlab.cee.redhat.com/asegundo/project-maestro/concertmaster/apis/mqtt/v1"
)

// Decodes MQTT messages into unstructured.Unstructured.
type Decoder struct {
	sink decoderSink
}

func NewDecoder(sink decoderSink) *Decoder {
	return &Decoder{
		sink: sink,
	}
}

type DecoderSinkList []decoderSink

func (l DecoderSinkList) Update(msg *mqttv1.ResourceMessage) error {
	for _, d := range l {
		if err := d.Update(msg); err != nil {
			return err
		}
	}
	return nil
}

func (l DecoderSinkList) Delete(uid types.UID) error {
	for _, d := range l {
		if err := d.Delete(uid); err != nil {
			return err
		}
	}
	return nil
}

type decoderSink interface {
	Update(msg *mqttv1.ResourceMessage) error
	Delete(uid types.UID) error
}

func (d *Decoder) Decode(m *paho.Publish) error {
	if len(m.Payload) == 0 || m.Payload == nil {
		return d.sink.Delete(uidFromTopic(m.Topic))
	}

	obj, err := d.decode(m)
	if err != nil {
		return err
	}
	return d.sink.Update(obj)
}

func uidFromTopic(topic string) types.UID {
	raw := path.Base(strings.TrimSuffix(topic, "/content"))
	return types.UID(raw)
}

func (d *Decoder) decode(m *paho.Publish) (
	rm *mqttv1.ResourceMessage, err error) {
	// TODO: implement contentType

	rm = &mqttv1.ResourceMessage{}
	err = json.Unmarshal(m.Payload, rm)
	return
}

type EventClient struct {
	// Buffered channel of outbound messages.
	send chan event.GenericEvent
}

func NewEventClient() *EventClient {
	return &EventClient{
		send: make(chan event.GenericEvent, 1),
	}
}

type EventHub struct {
	// Registered clients.
	clients map[*EventClient]bool

	// Inbound messages from the clients.
	broadcast chan event.GenericEvent

	// Register requests from the clients.
	register chan *EventClient

	// Unregister requests from clients.
	unregister chan *EventClient
}

func NewEventHub() *EventHub {
	return &EventHub{
		broadcast:  make(chan event.GenericEvent),
		register:   make(chan *EventClient),
		unregister: make(chan *EventClient),
		clients:    make(map[*EventClient]bool),
	}
}

func (h *EventHub) Register(c *EventClient) {
	h.register <- c
}

func (h *EventHub) Unregister(c *EventClient) {
	h.unregister <- c
}

func (h *EventHub) Broadcast(e event.GenericEvent) {
	h.broadcast <- e
}

func (h *EventHub) Run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
		case message := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

// Decoder Sink
func (h *EventHub) Delete(uid types.UID) error {
	// noop
	return nil
}

func (h *EventHub) Update(msg *mqttv1.ResourceMessage) error {
	h.Broadcast(event.GenericEvent{
		Object: msg.Content,
	})
	return nil
}
