# Medea
lab

## Medea-scout
Build:
```bash
CGO_ENABLED=0 GOOS=linux go build -o medea-scout main.go
```
Run:
```bash
PROMETHEUS_URL="http://172.20.0.1:9090" MEDEA_SCOUT_PORT="8081" ./medea-scout
```

Test curl:
```bash
curl -X POST http://localhost:8081/api/request -H "Content-Type: application/json"  -d '{"namespace": "argo-workflows", "cpu": 10, "ram": 9.3}'
```

## Medea-balancer
Build:
```bash
CGO_ENABLED=0 GOOS=linux go build -o medea-balancer main.go
```
Run:
```bash
export POSTGRESQL_URL="127.0.0.1:5432/medeadb?sslmode=disable" 
export POSTGRESQL_USER="postgres"
export POSTGRESQL_PASS="secret"
export MEDEA_SCOUT_URL="http://127.0.0.1:8081"
export MEDEA_BALANCER_PORT="8090"
./medea-balancer
```
