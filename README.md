# Medea Workflow Orchestration System

The Medea system is a distributed orchestration solution designed to balance and manage the lifecycle of **Argo Workflows** Spark-jobs across multiple Kubernetes clusters. It consists of two microservices that work together to optimize resource allocation.

---

## System Architecture

The system is split into two specialized components:

1.  **Medea Balancer**: The primary entry point. It calculates resource requirements, selects a cluster via the Scout service, and persists workflow states in a database.
2.  **Medea Scout**: An analytical engine that queries Prometheus to identify clusters with sufficient free capacity based on real-time metrics.

![Medea Architecture](./docs/diagram.svg)

---

## 1. Medea Balancer

A microservice that handles incoming user requests for workflow submission and management.

### Key Features
* **Resource Calculation**: Computes total requirements using the following formulas:
    * $CPU_{total} = (executor\_cores\_limit \times executor\_num) + driver\_cores\_limit$
    * $RAM_{total} = (executor\_memory\_limit \times executor\_num) + driver\_memory\_limit$
* **Validation**: Enforces that all memory parameters are specified in gigabytes (e.g., `0.5g`).
* **Persistence**: Automatically creates and maintains a `workflows` table in PostgreSQL to track workflow names, templates, namespaces, and assigned clusters.
* **Request Proxying**: Seamlessly forwards `GET`, `DELETE`, and `PUT` requests to the correct target cluster by retrieving the cluster location from the database.

### Environment Variables 
| Variable | Description | Example |
| :--- | :--- | :--- |
| `POSTGRESQL_URL` | Database host, port, and name | `127.0.0.1:5432/medeadb` |
| `POSTGRESQL_USER` | Database username | `pguser` |
| `POSTGRESQL_PASS` | Database password | `pgpass` |
| `MEDEA_SCOUT_URL` | Endpoint for the Scout service | `http://127.0.0.1:8081` |
| `MEDEA_BALANCER_PORT` | Port the balancer listens on | `8080` |

### Build:
```bash
CGO_ENABLED=0 GOOS=linux go build -o medea-balancer main.go
```
### Run:
```bash
export POSTGRESQL_URL="127.0.0.1:5432/medeadb?sslmode=disable" 
export POSTGRESQL_USER="postgres"
export POSTGRESQL_PASS="secret"
export MEDEA_SCOUT_URL="http://127.0.0.1:8081"
export MEDEA_BALANCER_PORT="8090"
./medea-balancer
```
### Test:
```bash
curl -X POST --url http://localhost:8090/api/v1/workflows/argo-workflows/submit --header "tuz: TUZ1234" --header "Content-Type: application/json" --data '{"resourceKind": "WorkflowTemplate", "resourceName": "hello-world-template", "submitOptions": {"labels": "workflows.argoproj.io/workflow-template=hello-world-template", "parameters": ["executor_num=2","driver_cores=1","driver_cores_limit=1","driver_memory=0.2g","driver_memory_limit=0.25g","executor_cores=1","executor_cores_limit=1","executor_memory_limit=6g"]}}'
```
---

## 2. Medea Scout

A minimalist microservice that identifies optimal clusters based on available resource quotas.

### Key Features
* **PromQL Integration**: Queries Prometheus to determine available `limits.cpu` and `limits.memory` by subtracting used resources from hard limits within a namespace.
* **Selection Logic**: Filters clusters that meet the requested CPU and RAM requirements.
* **Randomization**: If multiple clusters are suitable, it returns a random one to ensure balanced load distribution.

### Environment Variables 
| Variable | Description | Example |
| :--- | :--- | :--- |
| `PROMETHEUS_URL` | URL of the Prometheus server | `http://172.20.0.1:9090` |
| `MEDEA_SCOUT_PORT` | Port for the Scout service | `8081` |

### Build:
```bash
CGO_ENABLED=0 GOOS=linux go build -o medea-scout main.go
```
### Run:
```bash
PROMETHEUS_URL="http://172.20.0.1:9090" MEDEA_SCOUT_PORT="8081" ./medea-scout
```

### Test:
```bash
curl -X POST http://localhost:8081/api/request -H "Content-Type: application/json"  -d '{"namespace": "argo-workflows", "cpu": 10, "ram": 9.3}'
```
---

## API Reference

### Workflow Submission
**POST** `/api/v1/workflows/{namespace}/submit`

**Example Request:**
```bash
curl -X POST http://localhost:8080/api/v1/workflows/my-namespace/submit \
  -H "tuz: my-token" \
  -H "Content-Type: application/json" \
  -d '{
    "resourceKind": "WorkflowTemplate",
    "resourceName": "template-v1",
    "submitOptions": {
      "parameters": [
        "executor_num=2",
        "driver_cores_limit=1",
        "executor_cores_limit=1",
        "driver_memory_limit=0.5g",
        "executor_memory_limit=1g"
      ]
    }
  }'