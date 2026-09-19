# OMNIRA Kubernetes Deployment

## Overview

OMNIRA is deployed to Kubernetes using Helm charts. This guide covers:

- Local K8s development setup
- Production deployment architecture
- Configuration and secrets management
- Monitoring and observability
- Scaling and resource management

**Supported versions:**
- Kubernetes 1.24+
- Helm 3.10+

## Quick Start

### Prerequisites

1. Kubernetes cluster (local: Docker Desktop, minikube, or kind)
2. `kubectl` configured to access your cluster
3. `helm` CLI installed

### Local Development (minikube)

```bash
# Start minikube
minikube start --cpus=4 --memory=8192 --kubernetes-version=v1.28.0

# Enable ingress addon
minikube addons enable ingress

# Install OMNIRA
helm install omnira ./helm/omnira \
  --namespace omnira --create-namespace \
  --set global.environment=development \
  --set postgresql.url="postgresql://omnira:dev@postgres:5432/omnira"

# Port-forward for local access
kubectl port-forward -n omnira svc/omnira-api 8080:8080
```

Access API at `http://localhost:8080`

### Production Deployment

```bash
# Set production values
helm install omnira ./helm/omnira \
  --namespace omnira --create-namespace \
  --values helm/omnira/values-prod.yaml \
  --set postgresql.url=$DB_URL \
  --set global.domain="api.omnira.com"
```

## Chart Structure

```
helm/omnira/
├── Chart.yaml                    # Chart metadata
├── values.yaml                   # Default values
├── values-prod.yaml              # Production overrides (TBD)
└── templates/
    ├── _helpers.tpl              # Template helpers
    ├── namespace.yaml            # Kubernetes namespace
    ├── configmap.yaml            # Configuration
    ├── secret.yaml               # Database URL (create via --set)
    ├── api-deployment.yaml       # API deployment
    ├── api-service.yaml          # API service
    ├── api-hpa.yaml              # API horizontal pod autoscaler
    ├── worker-deployment.yaml    # Worker deployment
    ├── ingress.yaml              # Ingress (API routing)
    ├── rbac.yaml                 # ServiceAccount + RBAC
    ├── network-policy.yaml       # Network policies
    └── pdb.yaml                  # Pod disruption budget
```

## Configuration

### Environment Variables

All configuration flows through environment variables (12-factor):

| Variable | Default | Required | Notes |
|----------|---------|----------|-------|
| `OMNIRA_ENV` | development | No | Set via values.yaml |
| `OMNIRA_HTTP_ADDR` | `0.0.0.0:8080` | No | API listen address |
| `OMNIRA_DATABASE_URL` | — | Yes | PostgreSQL connection URL |
| `OMNIRA_NATS_URL` | `nats://localhost:4222` | No | NATS broker URL |
| `OMNIRA_OTEL_ENDPOINT` | `http://otel-collector:4317` | No | OpenTelemetry OTLP receiver |

### Secrets

Database credentials are injected via Kubernetes Secret:

```bash
# During install/upgrade
helm install omnira ./helm/omnira \
  --set postgresql.url="postgresql://user:pass@host:5432/db"
```

**Never commit secrets to git!**

### ConfigMap

Non-secret configuration:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: omnira-config
data:
  nats-url: nats://nats:4222
  otel-endpoint: http://otel-collector:4317
  environment: production
```

## Deployment Architecture

### API Server

- **Deployment:** 2 replicas (configurable)
- **Autoscaling:** CPU-based HPA (2-5 replicas)
- **Service:** ClusterIP (internal routing)
- **Health:** liveness + readiness probes
- **Security:** non-root user, read-only filesystem, pod security policy

**Resource Requests/Limits:**
```
CPU: 100m request / 500m limit
Memory: 256Mi request / 512Mi limit
```

### Worker

- **Deployment:** 1 replica (no HPA by default)
- **Metrics Port:** 9090 (`/healthz`, `/metrics`)
- **Service:** None (internal only)
- **Health:** liveness + readiness probes

**Resource Requests/Limits:**
```
CPU: 100m request / 500m limit
Memory: 256Mi request / 512Mi limit
```

### Ingress

Routes HTTP traffic to API:

```yaml
api.omnira.local → omnira-api:8080
```

**TLS:** Configured with cert-manager (automatic Let's Encrypt)

## Dependencies

### External (must be provisioned separately)

- **PostgreSQL:** Managed service (RDS, Cloud SQL, Azure Database)
  - Versions: 14, 15, 16
  - Connection pooling: PgBouncer recommended
  - Backups: automated daily + point-in-time recovery

- **NATS JetStream:** Managed service or self-hosted cluster
  - Version: 2.10+
  - Persistence: enabled
  - Replication: 3-node cluster for HA

- **OpenTelemetry Collector:** Optional (for distributed tracing)
  - Exports to your observability backend (Datadog, New Relic, etc.)

### Optional (included in cluster)

- **Prometheus Operator:** For metrics collection + alerting
  - Enable: `helm install kube-prometheus-stack ...`

- **NGINX Ingress Controller:** For routing
  - Enable: `helm install ingress-nginx ...`

## Scaling

### Horizontal Scaling (API)

```bash
# Autoscaling enabled by default
# Manual scaling:
kubectl scale deployment -n omnira omnira-api --replicas=5

# Check status:
kubectl get hpa -n omnira
kubectl top pod -n omnira
```

### Vertical Scaling

Edit `values.yaml` and upgrade:

```yaml
api:
  resources:
    requests:
      memory: 512Mi
      cpu: 250m
    limits:
      memory: 1Gi
      cpu: 1000m
```

```bash
helm upgrade omnira ./helm/omnira --values values.yaml
```

## Observability

### Metrics

Prometheus scrapes:
- API: `http://omnira-api:8080/metrics`
- Worker: `http://omnira-worker:9090/metrics`

Metrics exposed:
```
omnira_health_status{component="database"}
omnira_health_latency_ms{component="database"}
```

### Logging

Logs available via kubectl:

```bash
# API logs
kubectl logs -n omnira -l app.kubernetes.io/component=api

# Worker logs
kubectl logs -n omnira -l app.kubernetes.io/component=worker

# Stream logs
kubectl logs -n omnira -f deployment/omnira-api
```

### Distributed Tracing

Via OpenTelemetry:
```bash
kubectl port-forward -n otel svc/jaeger 6831:6831/udp 16686:16686
# Then visit http://localhost:16686
```

## High Availability

### Multi-Region Deployment

For production HA:

1. **Primary Region**
   - API: 3+ replicas across availability zones
   - Worker: 2+ replicas

2. **Disaster Recovery**
   - Database: cross-region read replicas
   - Backups: automated + tested restore drills

3. **Monitoring**
   - Health checks: per-region
   - Failover: manual or operator-managed

## Security

### Network Policies

Restrict traffic to/from pods:

```yaml
networkPolicy:
  enabled: true
  policyTypes:
    - Ingress
    - Egress
```

Policies included:
- Ingress from Nginx controller only
- Egress to PostgreSQL, NATS, OTel on specific ports
- DNS egress to kube-system

### Pod Security

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 1000
  fsGroup: 1000
  readOnlyRootFilesystem: true
  capabilities:
    drop:
      - ALL
```

### RBAC

Minimal permissions (get/list pods for debugging):

```yaml
rbac:
  enabled: true
```

## Troubleshooting

### Pod stuck in Pending

```bash
kubectl describe pod -n omnira <pod-name>
# Check: resource requests, node capacity, storage class
```

### CrashLoopBackOff

```bash
kubectl logs -n omnira <pod-name> --previous
# Check: database connectivity, invalid config
```

### Connection timeouts

```bash
kubectl exec -it -n omnira <api-pod> -- \
  pg_isready -h postgres -p 5432
# Check: firewall rules, security groups, DNS
```

## Helm Cheatsheet

```bash
# Install
helm install omnira ./helm/omnira -n omnira --create-namespace

# Upgrade
helm upgrade omnira ./helm/omnira -n omnira

# Rollback
helm rollback omnira 1 -n omnira

# Dry-run (validate)
helm install omnira ./helm/omnira -n omnira --dry-run --debug

# Get values
helm get values omnira -n omnira

# Template (render all manifests)
helm template omnira ./helm/omnira -n omnira

# Uninstall
helm uninstall omnira -n omnira
```

## Related Documentation

- **API:** [`docs/API.md`](./API.md)
- **Events:** [`docs/EVENTS.md`](./EVENTS.md)
- **Architecture:** [`docs/architecture/`](./architecture/)
