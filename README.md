# ConcertMaster

ConcertMaster is a component of the Kube Orchestra Project, a multi-cluster resources orchestrator for Kubernetes.

ConcertMaster is in-cluster Operator that lifecycles the resource definitions coming from the MQTT topics. It also propagates back into MQTT the state of all resources lifecycled.

## Kube Orchestra Architecture

![Kube Orchestra Architecture](./architecture.png)
