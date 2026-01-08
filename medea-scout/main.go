package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	//"strings"
	"time"
)

// RequestPayload описывает входящий JSON [cite: 4]
type RequestPayload struct {
	Namespace    string `json:"namespace"`
	CPU          float64    `json:"cpu"`
	RAM          float64    `json:"ram"`
	//CPU          int    `json:"cpu"`
	//RAM          int    `json:"ram"`
	//ExecutorsNum int    `json:"executors_num"`
}

// ResponsePayload описывает исходящий JSON [cite: 2]
type ResponsePayload struct {
	Cluster string `json:"cluster"`
}

// PrometheusResponse для десериализации ответа от Prometheus [cite: 7, 9]
type PrometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Metric struct {
				Cluster string `json:"cluster"`
			} `json:"metric"`
			Value []interface{} `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// cleanClusterName убирает порт (например, :8080) из имени кластера [cite: 7]
//func cleanClusterName(name string) string {
//	if i := strings.Index(name, ":"); i != -1 {
//		return name[:i]
//	}
//	return name
//}

// fetchResources делает запрос к Prometheus и возвращает карту [кластер]значение [cite: 5]
func fetchResources(pURL, namespace, queryTemplate string) (map[string]float64, error) {
	results := make(map[string]float64)
	query := fmt.Sprintf(queryTemplate, namespace, namespace)
	apiURL := fmt.Sprintf("%s/api/v1/query?query=%s", pURL, url.QueryEscape(query))

	resp, err := http.Get(apiURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var pResp PrometheusResponse
	if err := json.NewDecoder(resp.Body).Decode(&pResp); err != nil {
		return nil, err
	}

	for _, res := range pResp.Data.Result {
		if len(res.Value) < 2 {
			continue
		}
		// Prometheus возвращает значение как строку (напр. "29"), парсим в float64 [cite: 7, 10]
		valStr, ok := res.Value[1].(string)
		if !ok {
			continue
		}
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}
		results[res.Metric.Cluster] = val
		//results[cleanClusterName(res.Metric.Cluster)] = val
	}
	return results, nil
}

func main() {
	rand.Seed(time.Now().UnixNano())

	pURL := os.Getenv("PROMETHEUS_URL") // 
	port := os.Getenv("MEDEA_SCOUT_PORT") // 
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/api/request", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req RequestPayload
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		// Расчет необходимых ресурсов: executors_num * cpu и executors_num * ram [cite: 2]
		//needCPU := float64(req.ExecutorsNum * req.CPU)
		//needRAM := float64(req.ExecutorsNum * req.RAM)
		needCPU := float64(req.CPU)
		needRAM := float64(req.RAM)

		// PromQL шаблоны для CPU и RAM [cite: 7, 9]
		cpuQ := `kube_resourcequota{namespace="%s",resource="limits.cpu",type="hard"} - on(cluster) kube_resourcequota{namespace="%s",resource="limits.cpu",type="used"}`
		ramQ := `(kube_resourcequota{namespace="%s",resource="limits.memory",type="hard"} - on(cluster) kube_resourcequota{namespace="%s",resource="limits.memory",type="used"})/1024^3`

		cpus, errCPU := fetchResources(pURL, req.Namespace, cpuQ)
		mems, errRAM := fetchResources(pURL, req.Namespace, ramQ)

		if errCPU != nil || errRAM != nil {
			http.Error(w, "Prometheus communication error", http.StatusInternalServerError)
			return
		}

		var suitable []string
		for cluster, cVal := range cpus {
			// Сравнение доступных ресурсов в кластере с требуемыми [cite: 2]
			if cVal >= needCPU && mems[cluster] >= needRAM {
				suitable = append(suitable, cluster)
			}
		}

		if len(suitable) == 0 {
			http.Error(w, "No suitable clusters found", http.StatusNotFound)
			return
		}

		// Возвращаем случайный кластер из подходящих [cite: 2]
		selected := suitable[rand.Intn(len(suitable))]
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ResponsePayload{Cluster: selected})
	})

	fmt.Printf("Medea Scout starting on :%s (Prometheus: %s)\n", port, pURL)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Server failed: %v\n", err)
		os.Exit(1)
	}
}