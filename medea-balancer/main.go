package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// Config хранит конфигурацию приложения из ENV
type Config struct {
	PgURL       string
	PgUser      string
	PgPass      string
	MedeaScout  string
	ServicePort string
}

// Global DB handle
var db *sql.DB

// Структуры для парсинга запросов
type SubmitRequest struct {
	ResourceKind  string `json:"resourceKind"`
	ResourceName  string `json:"resourceName"`
	SubmitOptions struct {
		Labels     string   `json:"labels"`
		Parameters []string `json:"parameters"`
	} `json:"submitOptions"`
}

type ScoutRequest struct {
	Namespace string  `json:"namespace"`
	CPU       float64 `json:"cpu"`
	RAM       float64 `json:"ram"`
}

type ScoutResponse struct {
	Cluster string `json:"cluster"`
}

// WorkflowResponse используется для частичного парсинга ответа от Argo для получения имени
type WorkflowResponse struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

func main() {
	// 1. Загрузка конфигурации
	cfg := loadConfig()

	// 2. Подключение к PostgreSQL
	// Формируем DSN: postgres://user:pass@host:port/dbname?params
	dsn := fmt.Sprintf("postgres://%s:%s@%s", cfg.PgUser, cfg.PgPass, cfg.PgURL)
	var err error
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Ошибка открытия подключения к БД: %v", err)
	}
	defer db.Close()

	if err = db.Ping(); err != nil {
		log.Fatalf("Ошибка подключения к БД: %v", err)
	}

	// 3. Инициализация таблицы (для удобства, если не создана)
	initDB()

	// 4. Настройка роутера (Go 1.22+)
	mux := http.NewServeMux()

	// Part A: Создание Workflow
	mux.HandleFunc("POST /api/v1/workflows/{namespace}/submit", func(w http.ResponseWriter, r *http.Request) {
		handleSubmit(w, r, cfg.MedeaScout)
	})

	// Part B: Статус, Удаление, Остановка
	mux.HandleFunc("GET /api/v1/workflows/{namespace}/{workflowName}", handleProxy)
	mux.HandleFunc("DELETE /api/v1/workflows/{namespace}/{workflowName}", handleProxy)
	mux.HandleFunc("PUT /api/v1/workflows/{namespace}/{workflowName}/stop", handleProxy)

	log.Println("medea-balancer запущен. Ожидание запросов...")
	if err := http.ListenAndServe(":" + cfg.ServicePort, mux); err != nil {
		log.Fatal(err)
	}
}

// --- Обработчики (Handlers) ---

// handleSubmit реализует Процесс Создания Workflows (Part A)
func handleSubmit(w http.ResponseWriter, r *http.Request, scoutURL string) {
	namespace := r.PathValue("namespace")
	tuz := r.Header.Get("tuz")

	// Читаем тело запроса
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusInternalServerError)
		return
	}
	// Восстанавливаем тело для повторного использования
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	var req SubmitRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Шаг 2: Расчет ресурсов
	cpuTotal, memTotal, err := calculateResources(req.SubmitOptions.Parameters)
	if err != nil {
		// Ошибка, если память не в гигабайтах
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("Требуемые ресурсы для workflow: CPU=%.2f, RAM=%.2f GB", cpuTotal, memTotal)

	// Шаг 3: Запрос к medea-scout
	targetCluster, err := getTargetCluster(scoutURL, namespace, cpuTotal, memTotal)
	if err != nil {
		log.Printf("Ошибка получения кластера от medea-scout: %v", err)
		if strings.Contains(err.Error(), "404") {
			http.Error(w, "Cluster not found", http.StatusNotFound)
		} else {
			http.Error(w, "Scout service error", http.StatusInternalServerError)
		}
		return
	}

	// Шаг 4: Перенаправление запроса на целевой кластер
	targetURL := fmt.Sprintf("%s/api/v1/workflows/%s/submit", targetCluster, namespace)
	
	// Создаем новый запрос к целевому кластеру
	proxyReq, err := http.NewRequest("POST", targetURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		http.Error(w, "Failed to create proxy request", http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("tuz", tuz)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("Ошибка запроса к целевому кластеру %s: %v", targetCluster, err)
		http.Error(w, "Failed to forward request", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Если успех, сохраняем в БД
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var wfResp WorkflowResponse
		if err := json.Unmarshal(respBody, &wfResp); err == nil && wfResp.Metadata.Name != "" {
			// Шаг 5: Запись в PostgreSQL
			saveWorkflowToDB(wfResp.Metadata.Name, req.ResourceName, namespace, targetCluster)
		}
	}

	// Возвращаем ответ клиенту
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// handleProxy реализует Запрос статуса, удаления или остановки (Part B)
func handleProxy(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	workflowName := r.PathValue("workflowName")
	tuz := r.Header.Get("tuz")

	// Смотрим в БД, где запущен workflow
	clusterURL, err := getClusterFromDB(workflowName, namespace)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Workflow not found in DB", http.StatusNotFound)
		} else {
			log.Printf("DB Error: %v", err)
			http.Error(w, "Database error", http.StatusInternalServerError)
		}
		return
	}

	// Проксируем запрос
	// Формируем целевой URL, сохраняя путь и query параметры
	targetPath := r.URL.Path // /api/v1/workflows/...
	targetFullURL := fmt.Sprintf("%s%s", clusterURL, targetPath)
	if r.URL.RawQuery != "" {
		targetFullURL += "?" + r.URL.RawQuery
	}

	// Копируем тело запроса (если есть, например для DELETE/PUT)
	bodyBytes, _ := io.ReadAll(r.Body)
	proxyReq, err := http.NewRequest(r.Method, targetFullURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		http.Error(w, "Failed to create proxy request", http.StatusInternalServerError)
		return
	}

	// Копируем заголовки
	proxyReq.Header.Set("tuz", tuz)
	proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Failed to contact target cluster", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Возвращаем ответ
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// --- Вспомогательные функции ---

func calculateResources(params []string) (float64, float64, error) {
	vals := make(map[string]string)
	for _, p := range params {
		parts := strings.Split(p, "=")
		if len(parts) == 2 {
			vals[parts[0]] = parts[1]
		}
	}

	// Helper to parse float
	getVal := func(key string) float64 {
		if v, ok := vals[key]; ok {
			f, _ := strconv.ParseFloat(v, 64)
			return f
		}
		return 0 // Default if missing
	}

	// Helper to parse memory
	getMem := func(key string) (float64, error) {
		v, ok := vals[key]
		if !ok {
			return 0, nil // assume 0 if missing
		}
		if !strings.Contains(v, "g") {
			return 0, fmt.Errorf("memory param %s must contain 'g' (gigabytes)", key)
		}
		cleanVal := strings.ReplaceAll(v, "g", "")
		return strconv.ParseFloat(cleanVal, 64)
	}

	executorNum := getVal("executor_num")
	driverCoresLimit := getVal("driver_cores_limit")
	executorCoresLimit := getVal("executor_cores_limit")

	driverMemLimit, err := getMem("driver_memory_limit")
	if err != nil {
		return 0, 0, err
	}
	executorMemLimit, err := getMem("executor_memory_limit")
	if err != nil {
		return 0, 0, err
	}

	// Формулы из ТЗ
	// cpu_total=executor_cores_limit*executor_num+driver_cores_limit
	cpuTotal := executorCoresLimit*executorNum + driverCoresLimit
	// mem_total=executor_memory_limit*executor_num+driver_memory_limit
	memTotal := executorMemLimit*executorNum + driverMemLimit

	return cpuTotal, memTotal, nil
}

func getTargetCluster(scoutURL, ns string, cpu, ram float64) (string, error) {
	reqBody := ScoutRequest{
		Namespace: ns,
		CPU:       cpu,
		RAM:       ram,
	}
	jsonBody, _ := json.Marshal(reqBody)

	// POST запрос к medea-scout
	resp, err := http.Post(scoutURL+"/api/request", "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("404 Cluster not found")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("scout returned status %d", resp.StatusCode)
	}

	var scoutResp ScoutResponse
	if err := json.NewDecoder(resp.Body).Decode(&scoutResp); err != nil {
		return "", err
	}
	return scoutResp.Cluster, nil
}

func saveWorkflowToDB(wfName, wfTemplate, ns, cluster string) {
	// Запись в базу: id, workflowname, workflowtemplate, namespace, cluster
	query := `INSERT INTO workflows (workflowname, workflowtemplate, namespace, cluster) VALUES ($1, $2, $3, $4)`
	_, err := db.Exec(query, wfName, wfTemplate, ns, cluster)
	if err != nil {
		log.Printf("Ошибка записи в БД: %v", err)
	} else {
		log.Printf("Workflow %s сохранен в БД (cluster: %s)", wfName, cluster)
	}
}

func getClusterFromDB(wfName, ns string) (string, error) {
	var cluster string
	// Ищем кластер по имени workflow и namespace
	query := `SELECT cluster FROM workflows WHERE workflowname = $1 AND namespace = $2 ORDER BY id DESC LIMIT 1`
	err := db.QueryRow(query, wfName, ns).Scan(&cluster)
	return cluster, err
}

func loadConfig() Config {
	return Config{
		PgURL:       os.Getenv("POSTGRESQL_URL"),  // [cite: 5]
		PgUser:      os.Getenv("POSTGRESQL_USER"), // [cite: 6]
		PgPass:      os.Getenv("POSTGRESQL_PASS"), // [cite: 6]
		MedeaScout:  os.Getenv("MEDEA_SCOUT_URL"), // [cite: 7]
		ServicePort: os.Getenv("MEDEA_BALANCER_PORT"),
		//ServicePort: "8080",
	}
}

func initDB() {
	// Создание таблицы при старте [cite: 23]
	query := `CREATE TABLE IF NOT EXISTS workflows (
		id SERIAL PRIMARY KEY,
		workflowname VARCHAR(255) NOT NULL,
		workflowtemplate VARCHAR(255) NOT NULL,
		namespace VARCHAR(255) NOT NULL,
		cluster VARCHAR(255) NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := db.Exec(query); err != nil {
		log.Printf("Warning: Failed to ensure table exists: %v", err)
	}
}