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

// Config stores application configuration from Environment Variables
type Config struct {
	PgURL       string
	PgUser      string
	PgPass      string
	MedeaScout  string
	ServicePort string
}

// Global DB handle
var db *sql.DB

// Structures for request parsing
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

// WorkflowResponse used for partial parsing of Argo responses to retrieve the name
type WorkflowResponse struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

func main() {
	// 1. Load configuration
	cfg := loadConfig()

	// 2. Connect to PostgreSQL
	// DSN format: postgres://user:pass@host:port/dbname?params
	dsn := fmt.Sprintf("postgres://%s:%s@%s", cfg.PgUser, cfg.PgPass, cfg.PgURL)
	var err error
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Error opening database connection: %v", err)
	}
	defer db.Close()

	if err = db.Ping(); err != nil {
		log.Fatalf("Error connecting to database: %v", err)
	}

	// 3. Initialize table (if it doesn't exist)
	initDB()

	// 4. Setup router (Go 1.22+)
	mux := http.NewServeMux()

	// Part A: Workflow Creation
	mux.HandleFunc("POST /api/v1/workflows/{namespace}/submit", func(w http.ResponseWriter, r *http.Request) {
		handleSubmit(w, r, cfg.MedeaScout)
	})

	// Part B: Status, Deletion, Stopping
	mux.HandleFunc("GET /api/v1/workflows/{namespace}/{workflowName}", handleProxy)
	mux.HandleFunc("DELETE /api/v1/workflows/{namespace}/{workflowName}", handleProxy)
	mux.HandleFunc("PUT /api/v1/workflows/{namespace}/{workflowName}/stop", handleProxy)

	log.Println("medea-balancer started. Waiting for requests...")
	if err := http.ListenAndServe(":" + cfg.ServicePort, mux); err != nil {
		log.Fatal(err)
	}
}

// --- Handlers ---

// handleSubmit implements the Workflow Creation Process (Part A)
func handleSubmit(w http.ResponseWriter, r *http.Request, scoutURL string) {
	namespace := r.PathValue("namespace")
	tuz := r.Header.Get("tuz")

	// Read request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusInternalServerError)
		return
	}
	// Restore body for reuse
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	var req SubmitRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Step 2: Resource Calculation
	cpuTotal, memTotal, err := calculateResources(req.SubmitOptions.Parameters)
	if err != nil {
		// Error if memory is not in gigabytes
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("Required resources for workflow: CPU=%.2f, RAM=%.2f GB", cpuTotal, memTotal)

	// Step 3: Request to medea-scout
	targetCluster, err := getTargetCluster(scoutURL, namespace, cpuTotal, memTotal)
	if err != nil {
		log.Printf("Error obtaining cluster from medea-scout: %v", err)
		if strings.Contains(err.Error(), "404") {
			http.Error(w, "Cluster not found", http.StatusNotFound)
		} else {
			http.Error(w, "Scout service error", http.StatusInternalServerError)
		}
		return
	}

	// Step 4: Forward request to the target cluster
	targetURL := fmt.Sprintf("%s/api/v1/workflows/%s/submit", targetCluster, namespace)
	
	// Create a new request for the target cluster
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
		log.Printf("Request error to target cluster %s: %v", targetCluster, err)
		http.Error(w, "Failed to forward request", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// If successful, save to DB
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var wfResp WorkflowResponse
		if err := json.Unmarshal(respBody, &wfResp); err == nil && wfResp.Metadata.Name != "" {
			// Step 5: Save to PostgreSQL
			saveWorkflowToDB(wfResp.Metadata.Name, req.ResourceName, namespace, targetCluster)
		}
	}

	// Return response to client
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// handleProxy implements Status, Delete, or Stop requests (Part B)
func handleProxy(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	workflowName := r.PathValue("workflowName")
	tuz := r.Header.Get("tuz")

	// Check DB to see where the workflow is running
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

	// Proxy the request
	// Construct the target URL, preserving path and query parameters
	targetPath := r.URL.Path // /api/v1/workflows/...
	targetFullURL := fmt.Sprintf("%s%s", clusterURL, targetPath)
	if r.URL.RawQuery != "" {
		targetFullURL += "?" + r.URL.RawQuery
	}

	// Copy request body (if exists, e.g., for DELETE/PUT)
	bodyBytes, _ := io.ReadAll(r.Body)
	proxyReq, err := http.NewRequest(r.Method, targetFullURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		http.Error(w, "Failed to create proxy request", http.StatusInternalServerError)
		return
	}

	// Copy headers
	proxyReq.Header.Set("tuz", tuz)
	proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Failed to contact target cluster", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Return response
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// --- Helper Functions ---

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

	// Formulas from Technical Requirements
	cpuTotal := executorCoresLimit*executorNum + driverCoresLimit
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

	// POST request to medea-scout
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
	// Record to database: id, workflowname, workflowtemplate, namespace, cluster
	query := `INSERT INTO workflows (workflowname, workflowtemplate, namespace, cluster) VALUES ($1, $2, $3, $4)`
	_, err := db.Exec(query, wfName, wfTemplate, ns, cluster)
	if err != nil {
		log.Printf("Error writing to DB: %v", err)
	} else {
		log.Printf("Workflow %s saved to DB (cluster: %s)", wfName, cluster)
	}
}

func getClusterFromDB(wfName, ns string) (string, error) {
	var cluster string
	// Search for cluster by workflow name and namespace
	query := `SELECT cluster FROM workflows WHERE workflowname = $1 AND namespace = $2 ORDER BY id DESC LIMIT 1`
	err := db.QueryRow(query, wfName, ns).Scan(&cluster)
	return cluster, err
}

func loadConfig() Config {
	return Config{
		PgURL:       os.Getenv("POSTGRESQL_URL"),
		PgUser:      os.Getenv("POSTGRESQL_USER"),
		PgPass:      os.Getenv("POSTGRESQL_PASS"),
		MedeaScout:  os.Getenv("MEDEA_SCOUT_URL"),
		ServicePort: os.Getenv("MEDEA_BALANCER_PORT"),
	}
}

func initDB() {
	// Create table on startup
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