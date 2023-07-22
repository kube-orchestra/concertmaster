package mqtt

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mqttv1 "gitlab.cee.redhat.com/asegundo/project-maestro/concertmaster/apis/mqtt/v1"
	"gitlab.cee.redhat.com/asegundo/project-maestro/concertmaster/internal/dynamiccache"
)

type ControllerManager struct {
	log           logr.Logger
	mgr           manager.Manager
	dc            dynamicCache
	newReconciler newReconcilerFn

	hub           eventHub
	newController newControllerFn

	controllerReferencesMux sync.Mutex
	controllerReferences    map[schema.GroupVersionKind]map[dynamiccache.OwnerReference]struct{}
	controllers             map[schema.GroupVersionKind]controllerEntry
}

func NewControllerManager(
	log logr.Logger, hub eventHub,
	mgr manager.Manager, dc dynamicCache,
	newReconciler newReconcilerFn,
) *ControllerManager {

	return &ControllerManager{
		log:           log,
		mgr:           mgr,
		dc:            dc,
		newReconciler: newReconciler,
		hub:           hub,

		newController: controller.NewUnmanaged,

		controllerReferences: map[schema.GroupVersionKind]map[dynamiccache.OwnerReference]struct{}{},
		controllers:          map[schema.GroupVersionKind]controllerEntry{},
	}
}

type newReconcilerFn func() Reconciler

type Reconciler interface {
	reconcile.Reconciler
	InjectGVK(gvk schema.GroupVersionKind)
}

type eventHub interface {
	Broadcast(e event.GenericEvent)
	Register(c *EventClient)
	Unregister(c *EventClient)
}

type controllerEntry struct {
	eventClient *EventClient
	controller  controller.Controller
	stopCh      chan struct{}
}

type dynamicCache interface {
	List(
		ctx context.Context,
		out client.ObjectList, opts ...client.ListOption,
	) error
	Source() source.Source
}

type newControllerFn func(
	name string, mgr manager.Manager,
	options controller.Options,
) (controller.Controller, error)

func (cm *ControllerManager) controllerForGVK(
	gvk schema.GroupVersionKind,
	obj *unstructured.Unstructured,
) error {
	cm.controllerReferencesMux.Lock()
	defer cm.controllerReferencesMux.Unlock()

	if _, ok := cm.controllerReferences[gvk]; ok {
		// controller already exists
		cm.controllerReferences[gvk][ownerRef(obj)] = struct{}{}
		cm.log.Info("controller exists", "gvk", gvk)
		return nil
	} else {
		cm.controllerReferences[gvk] = map[dynamiccache.OwnerReference]struct{}{
			ownerRef(obj): {},
		}
	}
	cm.log.Info("creating new controller", "gvk", gvk)

	r := cm.newReconciler()
	r.InjectGVK(gvk)
	c, err := cm.newController(gvk.Kind, cm.mgr, controller.Options{
		Reconciler: r,
	})
	if err != nil {
		return fmt.Errorf("creating new controller: %w", err)
	}

	isSameGVKPredicate := func(object client.Object) bool {
		objGVK := object.GetObjectKind().GroupVersionKind()
		if objGVK == gvk {
			cm.log.Info(
				"predicate success",
				"gvk", objGVK.String(), "key", client.ObjectKeyFromObject(obj))
			return true
		}
		cm.log.Info(
			"predicate filtered",
			"gvk", objGVK.String(), "key", client.ObjectKeyFromObject(obj))
		return false
	}

	// MQTT Source
	eventClient := NewEventClient()
	cm.hub.Register(eventClient)
	if err := c.Watch(
		&source.Channel{
			Source: eventClient.send,
		},
		&handler.EnqueueRequestForObject{},
		predicate.NewPredicateFuncs(isSameGVKPredicate)); err != nil {
		return fmt.Errorf("watching MQTT source: %w", err)
	}

	// DynamicSource
	if err := c.Watch(
		cm.dc.Source(),
		&handler.EnqueueRequestForObject{},
		predicate.NewPredicateFuncs(isSameGVKPredicate)); err != nil {
		return fmt.Errorf("watching Dynamic source: %w", err)
	}

	controllerEntry := &controllerEntry{
		controller:  c,
		eventClient: eventClient,
		stopCh:      make(chan struct{}),
	}

	cm.controllers[gvk] = *controllerEntry

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-controllerEntry.stopCh
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer shutdownCancel()
		shutdownErr := wait.PollImmediateUntilWithContext(shutdownCtx, 1*time.Second, func(ctx context.Context) (done bool, err error) {
			done = cm.hasCleanedUp(ctx, gvk)
			if !done {
				cm.log.Info("controller still cleaning up", "gvk", gvk)
			}
			return
		})
		if shutdownErr != nil {
			cm.log.Error(err, "graceful shutdown failed, dangling objects may persist")
		}

		cm.hub.Unregister(eventClient)
		cancel()
	}()

	go func() {
		err := c.Start(ctx)
		if err != nil && err != context.Canceled {
			panic(err)
		}
	}()

	return nil
}

func (cm *ControllerManager) getOwnerRefForUID(uid types.UID) (dynamiccache.OwnerReference, bool) {
	for gvk := range cm.controllerReferences {
		for or := range cm.controllerReferences[gvk] {
			if or.UID == uid {
				return or, true
			}
		}
	}
	return dynamiccache.OwnerReference{}, false
}

// Free all watches associated with the given owner.
func (cm *ControllerManager) free(
	ctx context.Context, uid types.UID,
) error {
	cm.controllerReferencesMux.Lock()
	defer cm.controllerReferencesMux.Unlock()

	ownerRef, ok := cm.getOwnerRefForUID(uid)
	if !ok {
		return nil
	}

	for gvk, refs := range cm.controllerReferences {
		if _, ok := refs[ownerRef]; ok {
			delete(refs, ownerRef)

			if len(refs) == 0 {
				cm.log.Info("shutting down controller",
					"kind", gvk.Kind, "group", gvk.Group,
					"ownerNamespace", ownerRef.Namespace)

				if ce, ok := cm.controllers[gvk]; ok {
					close(ce.stopCh)
					delete(cm.controllerReferences, gvk)
					delete(cm.controllers, gvk)
				}
			}
		}
	}
	return nil
}

func (cm *ControllerManager) hasCleanedUp(ctx context.Context, gvk schema.GroupVersionKind) bool {
	objList := &unstructured.UnstructuredList{}
	objList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvk.Group,
		Kind:    gvk.Kind + "List",
		Version: gvk.Version,
	})
	if err := cm.dc.List(ctx, objList); err != nil {
		cm.log.Error(err, "listing objects for cleanup")
		return false
	}
	return len(objList.Items) == 0
}

// DecoderSink Interface
func (cm *ControllerManager) Update(
	msg *mqttv1.ResourceMessage,
) error {
	obj := msg.Content
	gvk := obj.GetObjectKind().GroupVersionKind()
	return cm.controllerForGVK(gvk, obj)
}

func (cm *ControllerManager) Delete(uid types.UID) error {
	return cm.free(context.Background(), uid)
}

func ownerRef(owner *unstructured.Unstructured) dynamiccache.OwnerReference {
	ownerGVK := owner.GetObjectKind().GroupVersionKind()

	return dynamiccache.OwnerReference{
		GroupKind: schema.GroupKind{
			Group: ownerGVK.Group,
			Kind:  ownerGVK.Kind,
		},

		UID:       owner.GetUID(),
		Name:      owner.GetName(),
		Namespace: owner.GetNamespace(),
	}
}
