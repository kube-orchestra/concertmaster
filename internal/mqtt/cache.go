package mqtt

import (
	"sync"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mqttv1 "gitlab.cee.redhat.com/asegundo/project-maestro/concertmaster/apis/mqtt/v1"
)

type Cache struct {
	mu   sync.RWMutex
	data map[key]mqttv1.ResourceMessage
}

var _ decoderSink = (*Cache)(nil)

type key struct {
	gvk schema.GroupVersionKind
	key client.ObjectKey
}

func NewCache() *Cache {
	return &Cache{
		data: make(map[key]mqttv1.ResourceMessage),
	}
}

func (c *Cache) Update(msg *mqttv1.ResourceMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := key{
		gvk: msg.Content.GroupVersionKind(),
		key: client.ObjectKeyFromObject(msg.Content),
	}
	c.data[key] = *msg
	return nil
}

func (c *Cache) Delete(uid types.UID) error {
	return nil
}

func (c *Cache) Get(gvk schema.GroupVersionKind, k client.ObjectKey) (*mqttv1.ResourceMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	msg, ok := c.data[key{
		gvk: gvk,
		key: k,
	}]
	if !ok {
		return nil, errors.NewNotFound(schema.GroupResource{
			Group: gvk.Group,
		}, k.Name)
	}
	return &msg, nil
}
