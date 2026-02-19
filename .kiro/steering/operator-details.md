---
inclusion: fileMatch
fileMatchPattern: 'percona-server-mongodb-operator/**'
---

# Percona Server for MongoDB Operator Development Guide

This document provides detailed guidance for working with the Liberty Platform's custom fork of the Percona Server for MongoDB Operator.

## Repository Purpose

Custom fork of the [Percona Operator for MongoDB](https://github.com/percona/percona-server-mongodb-operator) that deploys and manages MongoDB instances on Kubernetes. The operator automates deployments, scaling, backups, and day-to-day operations for both replica sets and sharded clusters.

## Key Features

- **Automated workflows** - Simplified MongoDB management
- **High availability** - No single point of failure
- **Easy sharding and scaling** - Horizontal scaling support
- **Integrated backups** - Automated backup and restore
- **Monitoring integration** - PMM (Percona Monitoring and Management)
- **Automated updates** - Rolling updates with zero downtime
- **Password rotation** - Automated credential management
- **Private registries** - Support for custom container registries

## Repository Structure

```
cmd/
  manager/              # Operator main entry point
  mongodb-healthcheck/  # Health check binary
pkg/
  apis/                 # Custom Resource Definitions (CRDs)
    psmdb/v1/          # PerconaServerMongoDB CRD
  controller/           # Reconciliation logic
    perconaservermongodb/
  psmdb/               # MongoDB-specific logic
    backup/            # Backup operations
    cluster/           # Cluster management
    secret/            # Secret management
  k8s/                 # Kubernetes client utilities
  naming/              # Resource naming conventions
  util/                # Shared utilities
config/
  crd/                 # CRD manifests
  rbac/                # RBAC configurations
  manager/             # Operator deployment
deploy/
  bundle.yaml          # Complete deployment bundle
  operator.yaml        # Operator deployment
  rbac.yaml            # RBAC resources
  crd.yaml             # CRD definitions
  cr.yaml              # Example custom resource
  cr-minimal.yaml      # Minimal CR example
build/
  Dockerfile           # Operator container image
  ps-entry.sh          # MongoDB entrypoint
  pbm-entry.sh         # Backup agent entrypoint
e2e-tests/             # End-to-end test suites
vendor/                # Go dependencies
```

## Custom Resource Definition (CRD)

The operator manages MongoDB through the `PerconaServerMongoDB` custom resource.

### Basic CR Structure

```yaml
apiVersion: psmdb.percona.com/v1
kind: PerconaServerMongoDB
metadata:
  name: my-cluster
spec:
  crVersion: "1.21.0"
  image: ghcr.io/objectrocket/percona-server-mongodb:6.0-k8s
  
  replsets:
    - name: rs0
      size: 3
      volumeSpec:
        persistentVolumeClaim:
          resources:
            requests:
              storage: 10Gi
  
  backup:
    enabled: true
    image: percona/percona-backup-mongodb:2.0.0
    storages:
      s3-us-west:
        type: s3
        s3:
          bucket: my-backups
          region: us-west-2
```

### Key Spec Fields

- **crVersion**: Operator version compatibility
- **image**: MongoDB container image
- **replsets**: Replica set configurations
- **sharding**: Sharded cluster configuration
- **backup**: Backup configuration
- **secrets**: Secret references
- **pmm**: Monitoring configuration
- **updateStrategy**: Update behavior

## Development Workflow

### Prerequisites

1. **Go** (version specified in go.mod)
2. **Docker** (for building images)
3. **kubectl** (for Kubernetes interaction)
4. **Kubernetes cluster** (for testing)
5. **make** (for build automation)

### Building the Operator

```bash
# Build operator binary
make build

# Build operator container image
make docker-build

# Build and push image
make docker-build docker-push IMG=ghcr.io/your-org/psmdb-operator:latest
```

### Running Locally

```bash
# Install CRDs
make install

# Run operator locally (outside cluster)
make run

# Deploy to cluster
make deploy IMG=ghcr.io/your-org/psmdb-operator:latest
```

### Testing

```bash
# Run unit tests
make test

# Run e2e tests (requires cluster)
cd e2e-tests
./run

# Run specific e2e test
./run default-cr
```

## Key Components

### Controller

The main reconciliation loop in `pkg/controller/perconaservermongodb/`.

**Responsibilities:**
- Watch PerconaServerMongoDB resources
- Reconcile desired state with actual state
- Manage StatefulSets, Services, ConfigMaps
- Handle backup and restore operations
- Coordinate rolling updates

**Key files:**
- `controller.go` - Main controller logic
- `psmdb_controller.go` - Reconciliation loop
- `status.go` - Status updates

### PSMDB Package

MongoDB-specific logic in `pkg/psmdb/`.

**Modules:**
- `backup/` - Backup and restore operations
- `cluster/` - Cluster configuration
- `secret/` - Secret management
- `tls/` - TLS certificate handling
- `mongo/` - MongoDB client operations

### APIs

Custom resource definitions in `pkg/apis/psmdb/v1/`.

**Key files:**
- `perconaservermongodb_types.go` - CR type definitions
- `psmdb_defaults.go` - Default values
- `psmdb_validation.go` - Validation logic

## Common Tasks

### Adding a New Feature

1. **Update CRD types** in `pkg/apis/psmdb/v1/`
2. **Add validation** in `psmdb_validation.go`
3. **Implement reconciliation logic** in controller
4. **Update CRD manifests**: `make manifests`
5. **Generate code**: `make generate`
6. **Add tests** in appropriate test suite
7. **Update documentation**

### Modifying Reconciliation Logic

1. **Locate relevant controller code** in `pkg/controller/`
2. **Make changes** to reconciliation logic
3. **Test locally**: `make run`
4. **Run unit tests**: `make test`
5. **Run e2e tests** for affected functionality

### Updating CRD

1. **Modify types** in `pkg/apis/psmdb/v1/perconaservermongodb_types.go`
2. **Update validation** if needed
3. **Regenerate manifests**: `make manifests`
4. **Regenerate code**: `make generate`
5. **Update deploy files**: `make bundle`
6. **Test with new CR fields**

### Adding E2E Test

1. **Create test directory** in `e2e-tests/`
2. **Add test script** (usually `run.sh`)
3. **Create test CR** (YAML manifest)
4. **Add assertions** for expected behavior
5. **Update test CSV** files if needed
6. **Run test**: `./run your-test-name`

## Integration with Liberty Platform

### Deployment via FluxCD

The operator is deployed to database clusters via liberty-infrastructure:

```yaml
# In liberty-infrastructure/platform/percona-mongodb-operator/
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: percona-mongodb-operator
spec:
  chart:
    spec:
      chart: psmdb-operator
      version: 1.21.0
```

### Instance Creation via Temporal

liberty-temporal-platform creates MongoDB instances:

```go
// In temporal-platform/internal/psmdb/
func (a *Activities) CreateInstance(ctx context.Context, req *protos.CreateInstanceRequest) (*protos.CreateInstanceResponse, error) {
    // Create PerconaServerMongoDB CR
    psmdb := &psmdbv1.PerconaServerMongoDB{
        ObjectMeta: metav1.ObjectMeta{
            Name:      req.InstanceId,
            Namespace: req.Namespace,
        },
        Spec: psmdbv1.PerconaServerMongoDBSpec{
            Image: req.MongodbImage,
            Replsets: []psmdbv1.ReplsetSpec{
                {
                    Name: "rs0",
                    Size: req.ReplicaCount,
                },
            },
        },
    }
    
    // Apply to cluster
    return k8sClient.Create(ctx, psmdb)
}
```

### Image Usage

The operator uses MongoDB images from liberty-docker-images:

```yaml
spec:
  image: ghcr.io/objectrocket/percona-server-mongodb:6.0-k8s
  backup:
    image: percona/percona-backup-mongodb:2.0.0
```

## Makefile Targets

Common make targets:

```bash
# Development
make build                 # Build operator binary
make run                   # Run locally
make install               # Install CRDs
make uninstall             # Remove CRDs

# Code generation
make generate              # Generate code
make manifests             # Generate CRD manifests
make bundle                # Generate deployment bundle

# Docker
make docker-build          # Build container image
make docker-push           # Push container image

# Testing
make test                  # Run unit tests
make test-e2e              # Run e2e tests

# Deployment
make deploy                # Deploy to cluster
make undeploy              # Remove from cluster

# Utilities
make fmt                   # Format code
make vet                   # Run go vet
make lint                  # Run linter
```

## E2E Testing

### Test Structure

Each e2e test is a directory containing:
- `run.sh` - Test execution script
- `conf/*.yaml` - Test CR manifests
- `compare/*.yaml` - Expected state files

### Running Tests

```bash
# Run all tests
cd e2e-tests
./run

# Run specific test
./run default-cr

# Run test suite
./run --suite=run-pr.csv
```

### Test Categories

- **Basic**: `default-cr`, `one-pod`, `limits`
- **Scaling**: `scaling`, `smart-update`
- **Backup**: `demand-backup`, `scheduled-backup`, `pitr`
- **Sharding**: `data-sharded`, `upgrade-sharded`
- **Security**: `custom-tls`, `ldap`, `users`
- **Monitoring**: `monitoring-2-0`, `monitoring-pmm3`
- **Chaos**: `self-healing-chaos`, `operator-self-healing-chaos`

### Writing Tests

1. **Create test directory**: `e2e-tests/my-test/`
2. **Add run.sh**:
   ```bash
   #!/bin/bash
   set -o errexit
   
   test_dir=$(realpath $(dirname $0))
   . ${test_dir}/../functions
   
   create_infra $namespace
   
   desc 'create PSMDB cluster'
   apply_cluster $test_dir/conf/my-cluster.yaml
   
   desc 'check if all pods are ready'
   wait_for_running $cluster-rs0 3
   
   desc 'verify functionality'
   # Add test assertions
   
   destroy $namespace
   ```

3. **Add CR manifest**: `conf/my-cluster.yaml`
4. **Add expected states**: `compare/` (if needed)

## Best Practices

### Code Organization

1. **Keep controllers focused** - Single responsibility
2. **Use helper functions** - Reusable logic in pkg/
3. **Validate early** - Check CR validity before reconciliation
4. **Handle errors gracefully** - Return errors for retry
5. **Update status** - Keep CR status current

### CRD Design

1. **Use clear field names** - Self-documenting
2. **Provide defaults** - Sensible default values
3. **Add validation** - Prevent invalid configurations
4. **Version carefully** - Plan for API evolution
5. **Document fields** - Add comments and examples

### Testing

1. **Write unit tests** - Test logic in isolation
2. **Add e2e tests** - Test real-world scenarios
3. **Test edge cases** - Failure scenarios
4. **Use test fixtures** - Reusable test data
5. **Clean up resources** - Proper test teardown

### Reconciliation

1. **Be idempotent** - Same input = same output
2. **Handle partial failures** - Graceful degradation
3. **Use finalizers** - Clean up external resources
4. **Requeue on errors** - Retry failed operations
5. **Update status last** - After successful reconciliation

## Troubleshooting

### Operator Not Starting

**Check logs**:
```bash
kubectl logs -n psmdb-operator deployment/percona-server-mongodb-operator
```

**Common issues**:
- CRDs not installed: `make install`
- RBAC permissions: Check `deploy/rbac.yaml`
- Image pull errors: Verify image exists

### CR Not Reconciling

**Check CR status**:
```bash
kubectl get psmdb my-cluster -o yaml
```

**Check operator logs**:
```bash
kubectl logs -n psmdb-operator deployment/percona-server-mongodb-operator | grep my-cluster
```

**Common issues**:
- Invalid CR spec: Check validation errors
- Resource conflicts: Check for existing resources
- Insufficient resources: Check node capacity

### Pods Not Starting

**Check pod status**:
```bash
kubectl get pods -l app.kubernetes.io/instance=my-cluster
kubectl describe pod my-cluster-rs0-0
```

**Common issues**:
- Image pull errors: Verify image and credentials
- PVC issues: Check storage class and capacity
- Init container failures: Check init logs

### Backup Failures

**Check backup status**:
```bash
kubectl get psmdb-backup
kubectl describe psmdb-backup my-backup
```

**Check PBM logs**:
```bash
kubectl logs my-cluster-rs0-0 -c backup-agent
```

**Common issues**:
- Storage credentials: Verify secret
- Network access: Check connectivity to storage
- Insufficient space: Check storage capacity

## Upstream Sync

### Syncing with Upstream Percona

To sync with upstream Percona operator:

```bash
# Add upstream remote (if not already added)
git remote add upstream https://github.com/percona/percona-server-mongodb-operator.git

# Fetch upstream changes
git fetch upstream

# View changes
git log HEAD..upstream/main

# Merge or rebase
git merge upstream/main
# or
git rebase upstream/main

# Resolve conflicts if any
# Test thoroughly
# Push to fork
```

### Maintaining Custom Changes

1. **Document customizations** - Track Liberty-specific changes
2. **Use feature branches** - Isolate custom features
3. **Tag releases** - Mark stable versions
4. **Test after sync** - Run full e2e suite
5. **Update dependencies** - Keep go.mod current

## Additional Resources

- [Percona Operator Documentation](https://docs.percona.com/percona-operator-for-mongodb/)
- [Operator SDK Documentation](https://sdk.operatorframework.io/)
- [Kubernetes Operator Pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/)
- [Custom Resource Definitions](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)
- [Controller Runtime](https://github.com/kubernetes-sigs/controller-runtime)
- Upstream repository: https://github.com/percona/percona-server-mongodb-operator
