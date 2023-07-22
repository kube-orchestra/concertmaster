package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eclipse/paho.golang/paho"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mqttv1 "gitlab.cee.redhat.com/asegundo/project-maestro/concertmaster/apis/mqtt/v1"
	"gitlab.cee.redhat.com/asegundo/project-maestro/concertmaster/internal/controllers"
)

// Generic reconciler for both Package and ClusterPackage objects.
type SyncController struct {
	log    logr.Logger
	client client.Writer
	cache  cache
	dc     dynamicCache
	mqtt   mqtt

	gvk         schema.GroupVersionKind
	topicPrefix string
}

func NewSyncController(
	log logr.Logger,
	client client.Writer,
	cache cache,
	dc dynamicCache,
	mqtt mqtt, topicPrefix string,
) *SyncController {
	return &SyncController{
		log:         log,
		client:      client,
		cache:       cache,
		dc:          dc,
		mqtt:        mqtt,
		topicPrefix: topicPrefix,
	}
}

type cache interface {
	Get(gvk schema.GroupVersionKind, key client.ObjectKey) (
		*mqttv1.ResourceMessage, error)
}

type mqtt interface {
	Publish(ctx context.Context, m *paho.Publish) error
}

type dynamicCache interface {
	client.Reader
	Source() source.Source
	Free(ctx context.Context, obj client.Object) error
	Watch(ctx context.Context, owner client.Object, obj runtime.Object) error
}

func (c *SyncController) InjectGVK(gvk schema.GroupVersionKind) {
	c.gvk = gvk
}

func (c *SyncController) Reconcile(
	ctx context.Context, req ctrl.Request,
) (ctrl.Result, error) {
	log := c.log.WithValues(c.gvk.String(), req.String())
	defer log.Info("reconciled")
	ctx = logr.NewContext(ctx, log)

	resourceMsg, err := c.cache.Get(c.gvk, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !resourceMsg.Content.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, c.handleDeletion(ctx, resourceMsg)
	}

	existingObj, err := c.reconcileObject(ctx, resourceMsg)
	if err != nil {
		// TODO: report reconcile issues to MQTT.
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, c.publishStatusUpdate(ctx, resourceMsg, existingObj)
}

func (c *SyncController) reconcileObject(
	ctx context.Context, msg *mqttv1.ResourceMessage,
) (existingObj *unstructured.Unstructured, err error) {
	obj := msg.Content.DeepCopy()
	if err := c.dc.Watch(ctx, obj, obj); err != nil {
		return nil, fmt.Errorf("setting up dynamic watcher: %w", err)
	}

	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[controllers.CacheLabel] = "True"
	obj.SetLabels(labels)
	obj.SetUID("")

	existingObj = obj.DeepCopy()
	err = c.dc.Get(ctx, client.ObjectKeyFromObject(existingObj), existingObj)
	if errors.IsNotFound(err) {
		return obj, c.client.Create(ctx, obj)
	}
	if err != nil {
		return nil, err
	}

	if err := c.client.Patch(ctx, obj, client.Merge); err != nil {
		return nil, fmt.Errorf("patching %w", err)
	}
	return obj, nil
}

func (c *SyncController) handleDeletion(ctx context.Context, msg *mqttv1.ResourceMessage) error {
	log := logr.FromContextOrDiscard(ctx)
	log.Info("deleting")

	err := c.client.Delete(ctx, msg.Content)
	if errors.IsNotFound(err) {
		// success
		return c.publishStatusMessage(ctx, msg.Content.GetUID(), mqttv1.StatusMessage{
			MessageMeta:   msg.MessageMeta,
			ContentStatus: nil, // no status, object deleted
			ReconcileStatus: mqttv1.ReconcileStatus{
				Conditions: []metav1.Condition{
					{
						Type:               mqttv1.StatusMessageDeleted,
						Status:             metav1.ConditionTrue,
						ObservedGeneration: msg.Content.GetGeneration(),
						Reason:             "Success",
					},
				},
			},
		})
	}
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	return nil
}

func (c *SyncController) publishStatusUpdate(
	ctx context.Context, msg *mqttv1.ResourceMessage,
	existingObject *unstructured.Unstructured,
) error {
	rs := mqttv1.ReconcileStatus{
		Conditions: []metav1.Condition{
			{
				Type:               mqttv1.StatusMessageReconciled,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: msg.Content.GetGeneration(),
				Reason:             "Success",
			},
		},
	}
	if existingObject != nil {
		ct := existingObject.GetCreationTimestamp()
		rs.CreationTimestamp = &ct
		rs.ObservedGeneration = existingObject.GetGeneration()
	}

	statusMessage := mqttv1.StatusMessage{
		// copy message meta from latest resource message.
		MessageMeta: msg.MessageMeta,
		// Get object status.
		ContentStatus:   getObjectStatus(ctx, existingObject),
		ReconcileStatus: rs,
	}
	return c.publishStatusMessage(ctx, msg.Content.GetUID(), statusMessage)
}

func (c *SyncController) publishStatusMessage(
	ctx context.Context, uid types.UID, msg mqttv1.StatusMessage,
) error {
	log := logr.FromContextOrDiscard(ctx)
	msg.SentTimestamp = time.Now().UTC().Unix()
	j, err := json.Marshal(msg)
	if err != nil {
		panic(err)
	}

	topic := fmt.Sprintf("%s/%s/status", c.topicPrefix, uid)
	log.Info("sending status", "topic", topic)
	return c.mqtt.Publish(ctx, &paho.Publish{
		Topic:   topic,
		QoS:     1,
		Retain:  true,
		Payload: j,
	})
}

func getObjectStatus(ctx context.Context, existingObject *unstructured.Unstructured) *runtime.RawExtension {
	log := logr.FromContextOrDiscard(ctx)
	if existingObject == nil {
		return nil
	}

	objStatus, ok, err := unstructured.NestedFieldNoCopy(existingObject.Object, "status")
	if err != nil {
		log.Error(err, "get obj status")
		return nil
	}
	if !ok {
		// no .status
		return nil
	}
	sj, err := json.Marshal(objStatus)
	if err != nil {
		panic(err)
	}
	return &runtime.RawExtension{Raw: sj}
}
