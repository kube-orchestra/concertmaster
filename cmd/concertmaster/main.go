package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/eclipse/paho.golang/paho"
	"gitlab.cee.redhat.com/asegundo/project-maestro/concertmaster/internal/controllers"
	"gitlab.cee.redhat.com/asegundo/project-maestro/concertmaster/internal/controllers/syncer"
	"gitlab.cee.redhat.com/asegundo/project-maestro/concertmaster/internal/dynamiccache"
	"gitlab.cee.redhat.com/asegundo/project-maestro/concertmaster/internal/mqtt"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
)

const (
	concertmasterTopicPrefixEnvVar = "CONCERTMASTER_TOPIC_PREFIX"
	concertmasterClientIDEnvVar    = "CONCERTMASTER_CLIENT_ID"
)

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	if err := run(); err != nil {
		panic(err)
	}
}

func run() error {
	concertmasterTopicPrefix := strings.TrimRight(os.Getenv(concertmasterTopicPrefixEnvVar), "/")
	if len(concertmasterTopicPrefixEnvVar) == 0 {
		return fmt.Errorf("%s must be set!", concertmasterTopicPrefixEnvVar)
	}

	clientID := os.Getenv(concertmasterClientIDEnvVar)
	if len(clientID) == 0 {
		return fmt.Errorf("%s must be set!", concertmasterClientIDEnvVar)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		// Namespace:                  opts.namespace,
		// Scheme:                     scheme,
		MetricsBindAddress: ":8091",
		// HealthProbeBindAddress:     opts.probeAddr,
		// Port:                       9443,
		LeaderElectionResourceLock: "leases",
		// LeaderElection:             opts.enableLeaderElection,
		LeaderElectionID: "klsdfu452p3.package-operator-lock",
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	// Health and Ready checks
	if err := mgr.AddHealthzCheck("health", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("check", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	cache := mqtt.NewCache()

	dc := dynamiccache.NewCache(
		mgr.GetConfig(), mgr.GetScheme(), mgr.GetRESTMapper(), nil,
		dynamiccache.SelectorsByGVK{
			// Only cache objects with our label selector,
			// so we prevent our caches from exploding!
			schema.GroupVersionKind{}: dynamiccache.Selector{
				Label: labels.SelectorFromSet(labels.Set{
					controllers.CacheLabel: "True",
				}),
			},
		})

	hub := mqtt.NewEventHub()
	go hub.Run()

	var conn *mqtt.Connection

	cm := mqtt.NewControllerManager(
		ctrl.Log.WithName("controller-manager"), hub, mgr, dc,
		func() mqtt.Reconciler {
			// return NewNoOpReconciler()
			return syncer.NewSyncController(
				ctrl.Log.WithName("syncer"), mgr.GetClient(),
				cache, dc, conn, fmt.Sprintf("%s/%s", concertmasterTopicPrefix, clientID),
			)
		})

	ctx := signals.SetupSignalHandler()
	decoder := mqtt.NewDecoder(mqtt.DecoderSinkList{cache, cm, hub})

	conn = mqtt.NewConnection(
		ctrl.Log.WithName("mqtt"), mqtt.ConnectionOptions{
			BrokerURLs: []string{"tcp://localhost:31320"},
			KeepAlive:  30 * time.Second,
			ClientID:   clientID,
			Username:   "admin",
			Password:   "password",
			Topic:      fmt.Sprintf("%s/%s/+/content", concertmasterTopicPrefix, clientID),
			OnMessage: func(m *paho.Publish) {
				if err := decoder.Decode(m); err != nil {
					panic(err)
				}
			},
		})
	go conn.Start(ctx)

	ctrl.Log.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("problem running manager: %w", err)
	}
	return nil
}

type noopReconciler struct {
	gvk schema.GroupVersionKind
}

func NewNoOpReconciler() *noopReconciler {
	return &noopReconciler{}
}

func (c *noopReconciler) InjectGVK(gvk schema.GroupVersionKind) {
	c.gvk = gvk
}

func (c *noopReconciler) Reconcile(
	ctx context.Context, req ctrl.Request,
) (res ctrl.Result, err error) {
	fmt.Printf("reconcile: %v %v\n", c.gvk, req)
	return
}
