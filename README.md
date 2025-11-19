# Liqo Resource Broker (liqo-rb)

Central broker that aggregates resource advertisements from multiple Kubernetes clusters and makes intelligent decisions about resource allocation.

## Quick Start

### Prerequisites
- Go 1.25+
- kubectl
- kind (for local testing)

### Installation

1. **Navigate to project:**
```bash
   cd ~/liqo-resource-broker
```

2. **Install CRDs:**
```bash
   make install
```

3. **Run broker locally:**
```bash
   make run
```

4. **Create sample cluster advertisements:**
```bash
   kubectl apply -f config/samples/broker_v1alpha1_clusteradvertisement.yaml
   kubectl apply -f config/samples/broker_v1alpha1_clusteradvertisement_2.yaml
```

5. **Create a reservation:**
```bash
   kubectl apply -f config/samples/broker_v1alpha1_reservation.yaml
```

6. **View results:**
```bash
   kubectl get clusteradvertisements
   kubectl get reservations
```

## What It Does

The Resource Broker:

- **Aggregates** resource advertisements from multiple clusters
- **Scores** each cluster based on availability (0-100)
- **Selects** the best cluster for reservation requests
- **Tracks** reservation lifecycle (Pending → Reserved → Active → Released)
- **Monitors** cluster health (marks stale clusters as inactive)

## Architecture
```
┌─────────────────────────────────────────────┐
│         Resource Broker (Central)           │
│                                             │
│  ┌───────────────────────────────────┐    │
│  │   ClusterAdvertisements           │    │
│  │   - Cluster 1: 10 CPU, 20Gi      │    │
│  │   - Cluster 2: 5 CPU, 10Gi       │    │
│  │   - Cluster 3: 8 CPU, 16Gi       │    │
│  └───────────────┬───────────────────┘    │
│                  ↓                         │
│  ┌───────────────────────────────────┐    │
│  │     Decision Engine               │    │
│  │     (Score & Select Best)         │    │
│  └───────────────┬───────────────────┘    │
│                  ↓                         │
│  ┌───────────────────────────────────┐    │
│  │     Reservation Management        │    │
│  │     - Create                      │    │
│  │     - Track Lifecycle             │    │
│  │     - Handle Expiration           │    │
│  └───────────────────────────────────┘    │
└─────────────────────────────────────────────┘
```

## Custom Resources

### ClusterAdvertisement
Represents resources available in a remote cluster:
```yaml
apiVersion: broker.fluidos.eu/v1alpha1
kind: ClusterAdvertisement
metadata:
  name: cluster-1-adv
spec:
  clusterID: "cluster-1-abc123"
  resources:
    capacity: {cpu: "16", memory: "32Gi"}
    allocatable: {cpu: "15", memory: "30Gi"}
    allocated: {cpu: "5", memory: "10Gi"}
    available: {cpu: "10", memory: "20Gi"}
  timestamp: "2025-11-19T16:00:00Z"
status:
  active: true
  score: "66.67"
```

### Reservation
Requests resources from the broker:
```yaml
apiVersion: broker.fluidos.eu/v1alpha1
kind: Reservation
metadata:
  name: my-reservation
spec:
  requestedResources:
    cpu: "2"
    memory: "4Gi"
  duration: "1h"
status:
  phase: "Reserved"
  targetClusterID: "cluster-1-abc123"
```

## Decision Algorithm

**Score Calculation:**
```
Score = (Available_CPU / Allocatable_CPU × 50) + 
        (Available_Memory / Allocatable_Memory × 50)
```

Higher score = more available resources = better choice

**Selection:**
1. Filter inactive clusters (stale >10 min)
2. Filter clusters without enough resources
3. Calculate score for remaining clusters
4. Select highest scoring cluster

## Development

### Build
```bash
make build
```

### Run Tests
```bash
make test
```

### Generate Manifests
```bash
make manifests
```

### Update Cluster Timestamp (for testing)
```bash
kubectl patch clusteradvertisement cluster-1-adv --type='json' \
  -p="[{'op': 'replace', 'path': '/spec/timestamp', 'value': '$(date -u +"%Y-%m-%dT%H:%M:%SZ")'}]"
```

## Project Structure
```
.
├── api/v1alpha1/              # CRD definitions
│   ├── clusteradvertisement_types.go
│   └── reservation_types.go
├── internal/
│   ├── broker/                # Decision engine
│   │   └── decision.go
│   └── controller/            # Controllers
│       ├── clusteradvertisement_controller.go
│       └── reservation_controller.go
├── config/                    # Kubernetes manifests
│   ├── crd/                   # Generated CRDs
│   ├── samples/               # Example resources
│   └── rbac/                  # RBAC rules
└── cmd/                       # Main entrypoint
```

## Documentation

- [Phase 2 Report](PHASE2_REPORT.md) - Detailed technical explanation
- [API Reference](api/v1alpha1/) - CRD schemas

## Current Limitations

- Cluster advertisements must be manually created (Phase 3 will add RA→RB communication)
- No actual resource locking in target clusters (Phase 4)
- Simple scoring algorithm (can be enhanced)
- No authentication between components (Phase 3)

## License

Apache 2.0

## Project Info

- **Domain**: fluidos.eu
- **API Group**: broker.fluidos.eu
- **Version**: v1alpha1
- **REAR Protocol**: Compliant
