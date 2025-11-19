# Phase 2 Report: Resource Broker (RB) Implementation

## Overview
This phase implements the central Resource Broker that receives advertisements from multiple clusters, aggregates resource data, and makes intelligent decisions about resource allocation through reservations.

---

## Architecture

### Components Built

1. **ClusterAdvertisement CRD**
   - Location: `api/v1alpha1/clusteradvertisement_types.go`
   - Purpose: Stores resource advertisements from remote clusters
   
2. **Reservation CRD**
   - Location: `api/v1alpha1/reservation_types.go`
   - Purpose: Manages resource reservation requests and lifecycle
   
3. **Decision Engine**
   - Location: `internal/broker/decision.go`
   - Purpose: Selects the best cluster based on availability and scoring

4. **ClusterAdvertisement Controller**
   - Location: `internal/controller/clusteradvertisement_controller.go`
   - Purpose: Monitors cluster advertisements, calculates scores, tracks staleness

5. **Reservation Controller**
   - Location: `internal/controller/reservation_controller.go`
   - Purpose: Processes reservation requests, selects clusters, manages lifecycle

---

## How It Works

### 1. ClusterAdvertisement

Represents a cluster's available resources:
```yaml
apiVersion: broker.fluidos.eu/v1alpha1
kind: ClusterAdvertisement
spec:
  clusterID: "cluster-1-abc123"
  clusterName: "Production Cluster 1"
  resources:
    capacity: {cpu: "16", memory: "32Gi"}
    allocatable: {cpu: "15", memory: "30Gi"}
    allocated: {cpu: "5", memory: "10Gi"}
    available: {cpu: "10", memory: "20Gi"}
  cost:
    cpuCost: "0.05"
    memoryCost: "0.01"
    currency: "USD"
  timestamp: "2025-11-19T16:00:00Z"
status:
  active: true
  score: "66.67"
  phase: "Active"
```

**Key Fields:**
- **Capacity**: Total hardware resources
- **Allocatable**: Available to pods (capacity - system reserved)
- **Allocated**: Currently requested by pods
- **Available**: Still schedulable (allocatable - allocated)
- **Active**: False if advertisement is stale (>10 minutes old)
- **Score**: 0-100 rating (higher = better choice)

### 2. Reservation Lifecycle
```
User creates Reservation
         ↓
Phase: Pending
         ↓
Decision Engine selects best cluster
         ↓
Phase: Reserved (resources locked)
         ↓
[Application uses resources]
         ↓
Phase: Active (in use)
         ↓
Duration expires or manual release
         ↓
Phase: Released
```

**Reservation Phases:**
- **Pending**: Just created, awaiting cluster selection
- **Reserved**: Cluster selected, resources locked
- **Active**: Resources being used
- **Failed**: No suitable cluster or insufficient resources
- **Released**: Completed or expired

### 3. Decision Engine Algorithm

**Score Calculation:**
```
CPU Score = (Available CPU / Allocatable CPU) × 50
Memory Score = (Available Memory / Allocatable Memory) × 50
Total Score = CPU Score + Memory Score (0-100)
```

**Selection Process:**
1. Filter out inactive clusters (stale >10 minutes)
2. Filter clusters without enough resources
3. Calculate score for each remaining cluster
4. Select cluster with **highest score**
5. Create reservation targeting that cluster

**Example:**
- Cluster-1: 10/15 CPU available = 66.67 score
- Cluster-2: 5/7 CPU available = 71.43 score
- **Winner**: Cluster-2 (higher score)

---

## Controllers Behavior

### ClusterAdvertisement Controller

**Responsibilities:**
- Monitors all ClusterAdvertisements
- Checks staleness (>10 minutes = inactive)
- Calculates and updates scores
- Updates status fields
- Reconciles every 5 minutes

**Staleness Check:**
```go
age := time.Since(clusterAdv.Spec.Timestamp.Time)
isStale := age > 10*time.Minute
clusterAdv.Status.Active = !isStale
```

### Reservation Controller

**Responsibilities:**
- Processes new reservation requests
- Invokes Decision Engine to select cluster
- Validates resource availability
- Tracks reservation lifecycle
- Handles expiration
- Reconciles every 1 minute

**State Machine:**
```go
switch reservation.Status.Phase {
case Pending:
    selectCluster() → Reserved
case Reserved:
    checkExpiration() → Released
case Active:
    checkExpiration() → Released
case Failed, Released:
    // Terminal states
}
```

---

## Testing Results

### Test Scenarios

**Test 1: Small Reservation**
- Request: 2 CPU, 4Gi memory
- Result: ✅ Reserved in cluster-1
- Duration: 1 hour
- Outcome: Expired and Released successfully

**Test 2: Large Reservation**
- Request: 8 CPU, 15Gi memory
- Result: ✅ Reserved in cluster-1 (only cluster with enough resources)
- Duration: 2 hours
- Outcome: Successfully reserved

**Test 3: Impossible Reservation**
- Request: 20 CPU, 50Gi memory
- Result: ✅ Failed (no cluster has 20 CPU)
- Outcome: Properly rejected with clear error message

---

## Current Limitations

### 1. Manual Advertisement Creation
**Current**: ClusterAdvertisements are manually created with `kubectl apply`
**Production**: Resource Agents should automatically push advertisements
**Solution**: Implement in Phase 3 with authentication

### 2. No Actual Resource Locking
**Current**: Reservations are tracked in CRD status only
**Production**: Should actually reserve resources in target cluster
**Solution**: Implement in Phase 4 with concurrency control

### 3. Staleness Detection
**Current**: Simple time-based check (>10 minutes)
**Production**: More sophisticated health monitoring
**Improvement**: Add heartbeat mechanism

### 4. Score Algorithm
**Current**: Simple availability-based scoring
**Production**: Consider cost, latency, policies, SLAs
**Improvement**: Pluggable scoring strategies

---

## Project Structure
```
liqo-resource-broker/
├── api/v1alpha1/
│   ├── clusteradvertisement_types.go  # ClusterAdvertisement CRD
│   └── reservation_types.go           # Reservation CRD
├── internal/
│   ├── broker/
│   │   └── decision.go                # Decision engine logic
│   └── controller/
│       ├── clusteradvertisement_controller.go
│       └── reservation_controller.go
├── config/
│   ├── crd/bases/                     # Generated CRDs
│   ├── samples/                       # Test resources
│   └── rbac/                          # RBAC rules
└── cmd/
    └── main.go                        # Broker entrypoint
```

---

## Key Concepts

### REAR Protocol Compliance

The implementation follows REAR (Resource Exchange and Advertisement for the Continuum) principles:

- **Advertisement**: Clusters advertise available resources
- **Reservation**: Resources are reserved before use
- **Structured Data**: Uses Kubernetes Quantities for resources
- **Metadata**: Includes cost, timestamps, identifiers

### Event-Driven Architecture

Controllers respond immediately to changes:
- New ClusterAdvertisement → Calculate score
- New Reservation → Select cluster immediately
- Periodic reconciliation as backup (5 minutes for clusters, 1 minute for reservations)

---

## What We Achieved

✅ **Central broker** that aggregates multi-cluster resources  
✅ **Intelligent decision-making** based on availability scoring  
✅ **Complete reservation lifecycle** management  
✅ **Staleness detection** to avoid using outdated data  
✅ **Failure handling** for insufficient resources  
✅ **Expiration tracking** for time-limited reservations  
✅ **Event-driven updates** with periodic backup  

---

## Next Steps (Phase 3)

1. Implement **RA→RB communication** (Resource Agents push to Broker)
2. Add **mutual TLS authentication** between components
3. Implement **access control** (which clusters can register)
4. Secure **credential management** and rotation
5. Add **data integrity** checks for advertisements

---

## Testing Commands
```bash
# View all cluster advertisements
kubectl get clusteradvertisements

# View all reservations
kubectl get reservations

# Describe specific reservation
kubectl describe reservation <name>

# Update cluster timestamp (simulate fresh data)
kubectl patch clusteradvertisement <name> --type='json' \
  -p="[{'op': 'replace', 'path': '/spec/timestamp', 'value': '$(date -u +"%Y-%m-%dT%H:%M:%SZ")'}]"
```

---

*Generated: November 19, 2025*  
*Phase: 2 - Resource Broker (RB)*  
*Status: ✅ COMPLETE*
