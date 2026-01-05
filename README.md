# Medea
lab

## Build and run Medea-scout
```bash
CGO_ENABLED=0 GOOS=linux go build -o medea-scout main.go
```

```bash
PROMETHEUS_URL="http://172.20.0.1:9090" MEDEA_SCOUT_PORT="8081" ./medea-scout
```