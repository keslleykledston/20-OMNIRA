# OMNIRA Helm Chart — Production Deployment Guide

**Release:** R0.2  
**Ticket:** T24  
**Version:** 0.1.0

## Overview

This guide covers production deployment of OMNIRA using Helm with `values-prod.yaml`.

Key differences from development:
- **Replicas**: 3x API, 2x Worker (HA)
- **Resources**: 512Mi/1Gi memory, 250m/1000m CPU
- **Autoscaling**: Enabled with aggressive thresholds
- **Security**: Network policies, RBAC, security contexts
- **Monitoring**: Prometheus scraping, alerts, recording rules
- **Database/NATS**: External managed services only
- **Ingress**: Production TLS, security headers, rate limiting

---

## Prerequisites

Before deploying to production:

1. **Kubernetes Cluster** (1.20+)
   ```bash
   kubectl cluster-info
   ```

2. **Helm 3** installed
   ```bash
   helm version
   ```

3. **Nginx Ingress Controller**
   ```bash
   helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
   helm install nginx-ingress ingress-nginx/ingress-nginx -n ingress-nginx --create-namespace
   ```

4. **Cert-Manager** (for TLS)
   ```bash
   helm repo add jetstack https://charts.jetstack.io
   helm install cert-manager jetstack/cert-manager -n cert-manager --create-namespace --set installCRDs=true
   ```

5. **Prometheus Operator** (optional, for monitoring)
   ```bash
   helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
   helm install prometheus prometheus-community/kube-prometheus-stack -n monitoring --create-namespace
   ```

6. **External PostgreSQL** (RDS, Cloud SQL, or managed service)
   - Ensure network connectivity from cluster
   - Create database: `omnira_prod`
   - Create user with privileges

7. **External NATS JetStream** (or managed NATS service)
   - HA setup recommended (3+ nodes)
   - Persistence enabled

---

## Installation

### 1. Create Namespace
```bash
kubectl create namespace omnira
```

### 2. Create Database Secret
```bash
kubectl create secret generic omnira-db \
  --from-literal=host=postgres.prod.internal \
  --from-literal=port=5432 \
  --from-literal=database=omnira_prod \
  --from-literal=username=omnira_user \
  --from-literal=password=<strong-password> \
  -n omnira
```

### 3. Create NATS Secret (if not using managed service)
```bash
kubectl create secret generic omnira-nats \
  --from-literal=urls=nats://nats1.prod:4222,nats://nats2.prod:4222 \
  -n omnira
```

### 4. Install Chart
```bash
helm install omnira ./helm/omnira \
  -f helm/omnira/values.yaml \
  -f helm/omnira/values-prod.yaml \
  -n omnira \
  --wait
```

### 5. Verify Installation
```bash
# Check pods
kubectl get pods -n omnira

# Check services
kubectl get svc -n omnira

# Check ingress
kubectl get ingress -n omnira

# View logs
kubectl logs -f -n omnira -l app=omnira-api
```

---

## Production Configuration

### Database Configuration

Update `values-prod.yaml`:
```yaml
database:
  host: your-rds-endpoint.amazonaws.com
  port: 5432
  database: omnira_prod
```

Or use Kubernetes secret:
```bash
kubectl set env deployment/omnira-api \
  OMNIRA_DATABASE_URL=postgres://user:pass@host:5432/omnira_prod \
  -n omnira
```

### NATS Configuration

For managed NATS (e.g., Aiven, Upstash):
```yaml
nats:
  servers:
    - nats://nats1.example.com:4222
    - nats://nats2.example.com:4222
    - nats://nats3.example.com:4222
```

### TLS Certificate

Update ingress host:
```yaml
ingress:
  hosts:
    - host: api.yourdomain.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: omnira-tls
      hosts:
        - api.yourdomain.com
```

Cert-manager will automatically provision LetsEncrypt certificate.

---

## Scaling & Performance

### Horizontal Pod Autoscaling

API autoscaling is enabled:
```yaml
autoscaling:
  minReplicas: 3
  maxReplicas: 10
  targetCPUUtilizationPercentage: 70
  targetMemoryUtilizationPercentage: 75
```

Monitor scaling:
```bash
kubectl get hpa -n omnira -w
```

### Vertical Pod Autoscaling (Optional)

For automated resource tuning:
```bash
helm install vpa fairwinds-stable/vpa \
  --namespace kube-system \
  --set serviceAccount.create=true
```

### Load Testing

Before production traffic:
```bash
# k6 load test
k6 run tools/k6/tenancy-endpoints.js --vus 100 --duration 5m

# Apache Bench
ab -n 10000 -c 100 https://api.yourdomain.com/healthz
```

---

## Monitoring & Alerts

### Prometheus Metrics

Endpoints available at:
- API: `http://api-pod:8080/metrics`
- Worker: `http://worker-pod:9090/metrics`

### Grafana Dashboards

Import pre-built dashboards:
1. Navigate to Grafana
2. Import dashboard ID: [TBD]
3. Select Prometheus datasource

### Alert Rules

Alerts configured in `values-prod.yaml`:
- High error rate (>5% for 5m)
- High latency (P95 >1s for 5m)
- Pod crash loop

Route alerts to:
```yaml
monitoring:
  alerts:
    destination: slack  # or pagerduty, opsgenie
    channel: "#omnira-alerts"
```

---

## Backup & Disaster Recovery

### Database Backups

External database handles backups. Verify:
```bash
# AWS RDS
aws rds describe-db-instances --db-instance-identifier omnira-prod

# GCP Cloud SQL
gcloud sql instances describe omnira-prod
```

### NATS Persistence

Ensure JetStream has persistent storage:
```bash
kubectl get pvc -n nats
```

### Disaster Recovery Plan

1. **Database Recovery**
   - Restore from latest backup
   - Verify data integrity

2. **NATS Recovery**
   - Restore stream state from backup
   - Replay unprocessed messages

3. **Application Recovery**
   - Helm rollback to previous version
   ```bash
   helm rollback omnira 1 -n omnira
   ```

---

## Security Best Practices

### Network Security

Network policies restrict traffic:
- Ingress: Only from nginx-ingress namespace
- Egress: Only to database, NATS, DNS

### RBAC

ServiceAccount has minimal permissions:
```bash
kubectl get rolebindings -n omnira
kubectl describe role omnira -n omnira
```

### Secrets Management

Best practices:
- Use Sealed Secrets or external secret manager
- Rotate credentials every 90 days
- Never commit secrets to git

Sealed Secrets example:
```bash
echo -n 'mypassword' | kubeseal -n omnira --scope cluster -o yaml
```

### Image Security

- Use specific image tags (not `latest`)
- Scan images for vulnerabilities
- Use private image registry with authentication

---

## Troubleshooting

### Pods not starting

```bash
# Check events
kubectl describe pod <pod-name> -n omnira

# Check logs
kubectl logs <pod-name> -n omnira

# Check resource availability
kubectl top nodes
kubectl top pods -n omnira
```

### High latency

```bash
# Check CPU/memory usage
kubectl top pods -n omnira -l app=omnira-api

# Check network policies
kubectl get networkpolicy -n omnira

# Check database connection pool
curl http://api-pod:8080/metrics | grep database
```

### Database connection issues

```bash
# Verify credentials
kubectl get secret omnira-db -n omnira -o jsonpath='{.data.password}' | base64 -d

# Test connectivity
kubectl run -it --rm debug --image=postgres:14 -n omnira -- \
  psql -h postgres.prod.internal -U omnira_user -d omnira_prod
```

### NATS connectivity issues

```bash
# Check NATS servers
kubectl logs -f -n omnira -l app=omnira-worker | grep NATS

# Test connectivity
kubectl run -it --rm debug --image=natsio/nats-box -n omnira -- \
  nats-conn -s nats://nats1.prod:4222
```

---

## Upgrades

### Zero-Downtime Upgrades

```bash
# Upgrade using Helm
helm upgrade omnira ./helm/omnira \
  -f helm/omnira/values.yaml \
  -f helm/omnira/values-prod.yaml \
  -n omnira \
  --wait

# Verify rollout
kubectl rollout status deployment/omnira-api -n omnira
kubectl rollout status deployment/omnira-worker -n omnira
```

### Rollback

```bash
# List releases
helm history omnira -n omnira

# Rollback to previous version
helm rollout omnira 1 -n omnira
```

---

## Cost Optimization

### Right-Sizing

Monitor actual usage:
```bash
kubectl top pods -n omnira --containers --sort-by=memory
```

Adjust requests/limits based on actual consumption.

### Node Pool Selection

- Use spot instances for workers (cost savings: 70%)
- Use on-demand for API tier (reliability)

### Cleanup

Remove unused resources:
```bash
# Remove old PVCs
kubectl delete pvc -n omnira --all

# Remove old ConfigMaps/Secrets
kubectl delete configmap -n omnira -l app=omnira,version!=current
```

---

## Related Documentation

- **Kubernetes Deployment:** [`docs/KUBERNETES.md`](./KUBERNETES.md)
- **Helm Chart Structure:** `helm/omnira/`
- **API Performance:** [`docs/PERFORMANCE.md`](./PERFORMANCE.md)
- **Operations Runbook:** `docs/ops/`
