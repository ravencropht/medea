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