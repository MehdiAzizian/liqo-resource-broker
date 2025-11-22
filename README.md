# Kubernetes Resource Broker

A Kubernetes operator that aggregates resource advertisements from multiple clusters and intelligently allocates resources through reservations.

**Master's Thesis Project** - Multi-Cluster Resource Management System

---

## Overview

The Resource Broker receives resource advertisements from multiple Kubernetes clusters (via Resource Agents) and makes intelligent decisions about where to allocate workloads based on resource availability, cost, and scoring algorithms.

### Key Features

✅ **Multi-Cluster Aggregation**
- Receives advertisements from multiple clusters
- Tracks resource availability in real-time
- Automatic staleness detection (10-minute threshold)

✅ **Intelligent Decision Engine**
- Scoring algorithm (0-100) based on resource availability
- Automatic cluster selection for reservations
- Considers CPU, Memory, and cost metrics

✅ **Resource Locking & Concurrency Control**
- Prevents resource overbooking
- Transaction-safe reservation process
- Automatic resource release on expiration
- Finalizer-based cleanup

✅ **Reservation Lifecycle**
- States: Pending → Reserved → Active → Released/Failed
- Configurable duration with auto-expiration
- Manual deletion with proper cleanup

---

## Architecture
```
┌──────────────┐         ┌──────────────┐
│  Cluster A   │         │  Cluster B   │
│              │         │              │
│ Resource     │         │ Resource     │
│ Agent        │         │ Agent        │
└──────┬───────┘         └──────┬───────┘
       │                        │
       │ Advertisement          │ Advertisement
       │ (HTTPS)                │ (HTTPS)
       ▼                        ▼
    ┌─────────────────────────────┐
    │    Resource Broker          │
    │                             │
    │  ┌─────────────────────┐   │
    │  │ ClusterAdvertisement│   │
    │  │ Controller          │   │
    │  └──────────┬──────────┘   │
    │             │               │
    │  ┌──────────▼──────────┐   │
    │  │ Decision Engine     │   │
    │  │ (Scoring)           │   │
    │  └──────────┬──────────┘   │
    │             │               │
    │  ┌──────────▼──────────┐   │
    │  │ Reservation         │   │
    │  │ Controller          │   │
    │  └─────────────────────┘   │
    └─────────────────────────────┘
               ▲
               │ User Request
               │
         ┌─────┴─────┐
         │   User    │
         └───────────┘
```

---

## Quick Start

### Prerequisites
- Go 1.23+
- Kubernetes cluster (tested with kind)
- kubectl configured

### Installation
```bash
# Install CRDs
make install

# Run locally
make run
```

### Create a Reservation
```bash
# Apply sample reservation
kubectl apply -f config/samples/broker_v1alpha1_reservation.yaml

# View reservations
kubectl get reservations
kubectl describe reservation <name>
```

### View Cluster Advertisements
```bash
kubectl get clusteradvertisements
kubectl describe clusteradvertisement <name>
```

---

## Example Resources

### ClusterAdvertisement
```yaml
apiVersion: broker.fluidos.eu/v1alpha1
kind: ClusterAdvertisement
metadata:
  name: local-cluster-adv
spec:
  clusterID: "fd32c7d2-7cc6-46e6-80aa-d3d5c835586c"
  clusterName: "local-cluster"
  resources:
    capacity:
      cpu: "10"
      memory: "8025424Ki"
    allocated:
      cpu: "1050m"
      memory: "418Mi"
    reserved:
      cpu: "3000m"
      memory: "4Gi"
    available:
      cpu: "5950m"
      memory: "3597392Ki"
status:
  active: true
  score: "61.25"
  phase: "Active"
```

### Reservation
```yaml
apiVersion: broker.fluidos.eu/v1alpha1
kind: Reservation
metadata:
  name: my-workload
spec:
  requestedResources:
    cpu: "2"
    memory: "4Gi"
  duration: "1h"
  priority: 10
  requesterID: "user-team"
status:
  phase: "Reserved"
  targetClusterID: "fd32c7d2-7cc6-46e6-80aa-d3d5c835586c"
  reservedAt: "2025-11-22T15:00:00Z"
  expiresAt: "2025-11-22T16:00:00Z"
```

---

## Scoring Algorithm

The broker calculates a score (0-100) for each cluster:
```
Score = (Available_CPU / Allocatable_CPU × 50) + 
        (Available_Memory / Allocatable_Memory × 50)
```

**Higher score = More available resources**

Example:
- 90-100: Excellent (most resources available)
- 70-89: Good
- 50-69: Moderate
- 0-49: Limited

---

## Project Structure
```
liqo-resource-broker/
├── api/v1alpha1/                    # CRD definitions
│   ├── clusteradvertisement_types.go
│   └── reservation_types.go
├── cmd/main.go                       # Entry point
├── internal/
│   ├── controller/                   # Controllers
│   │   ├── clusteradvertisement_controller.go
│   │   └── reservation_controller.go
│   ├── broker/                       # Decision engine
│   │   └── decision_engine.go
│   └── resource/                     # Resource math
│       └── calculator.go
└── config/                           # Kubernetes manifests
```

---

## Development

### Build
```bash
make build
```

### Generate CRDs
```bash
make manifests
```

### Run Tests
```bash
make test
```

---

## Resource Locking

The broker implements optimistic concurrency control:

1. **Reservation Created** → Broker selects best cluster
2. **Resources Locked** → `Reserved` field updated in ClusterAdvertisement
3. **Available Recalculated** → `Available = Allocatable - Allocated - Reserved`
4. **Expiration/Deletion** → Resources automatically released

### Example Flow
```
Initial State:
- Available: 8950m CPU

Reservation 1 (3 CPU):
- Locked: 3000m
- Available: 5950m ✅

Reservation 2 (5 CPU):
- Request: 5000m
- Available: 5950m
- Locked: 5000m
- Available: 950m ✅

Reservation 3 (2 CPU):
- Request: 2000m
- Available: 950m
- Status: FAILED ❌ (insufficient resources)
```

---

## Configuration

### Command-Line Flags

- `--health-probe-bind-address`: Health probe address (default: `:8081`)
- `--metrics-bind-address`: Metrics endpoint (default: `:8080`)
- `--leader-elect`: Enable leader election (default: `false`)

### Advertisement Staleness

Advertisements older than **10 minutes** are marked as **Inactive**.

---

## Testing Results

✅ Resource locking prevents overbooking  
✅ Handles exact resource fits (0 remaining)  
✅ Fails correctly when insufficient resources  
✅ Auto-releases on expiration  
✅ Proper cleanup on manual deletion  
✅ Concurrent reservations handled safely

---

## Documentation

- Phase reports and implementation details available in repository

---

## Related Repository

- [liqo-resource-agent](https://github.com/mehdiazizian/liqo-resource-agent) - Cluster resource agent

---

## License

Apache License 2.0

## Author

Mehdi Azizian - Master's Thesis Project (2025)