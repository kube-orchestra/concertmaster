//go:build mage
// +build mage

package main

import (
	"context"
	"path/filepath"
	"time"

	"github.com/mt-sre/devkube/dev"
	kindv1alpha4 "sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
)

var devEnvironment *dev.Environment

func init() {
	devEnvironment = dev.NewEnvironment(
		"maestro",
		filepath.Join(".cache", "dev-env"),
		dev.WithClusterOptions([]dev.ClusterOption{
			dev.WithWaitOptions([]dev.WaitOption{dev.WithTimeout(2 * time.Minute)}),
		}),
		dev.WithKindClusterConfig(kindv1alpha4.Cluster{
			Nodes: []kindv1alpha4.Node{
				{
					Role: kindv1alpha4.ControlPlaneRole,
					ExtraPortMappings: []kindv1alpha4.PortMapping{
						// Open port to enable connectivity with the local MQTT broker
						{
							ContainerPort: 31320,
							HostPort:      31320,
							ListenAddress: "127.0.0.1",
							Protocol:      "TCP",
						},
					},
				},
			},
		}),
		dev.WithClusterInitializers{
			// TODO: fix
			// dev.ClusterLoadObjectsFromFiles{
			// 	"config/mqtt-server.yaml",
			// },
		},
	)
}

func Setup(ctx context.Context) error {
	return devEnvironment.Init(ctx)
}

func Teardown(ctx context.Context) error {
	return devEnvironment.Destroy(ctx)
}
