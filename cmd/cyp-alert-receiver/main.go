// cyp-alert-receiver is a small loopback-only receiver for cyp-agent alerts.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxRequestBytes = 1 << 20
	defaultDataFile = "data/alerts.jsonl"
)

type receiver struct {
	dataFile string
	mu       sync.Mutex
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	host := flag.String("host", "127.0.0.1", "HTTP listen host (use loopback unless protected by a reverse proxy)")
	port := flag.Int("port", 8081, "HTTP listen port")
	dataFile := flag.String("data-file", defaultDataFile, "JSONL file used to store received alerts")
	flag.Parse()
	if *port < 1 || *port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if !isLoopbackHost(*host) {
		return errors.New("refusing a non-loopback listener; place a reverse proxy with authentication in front if remote access is required")
	}

	receiver := &receiver{dataFile: *dataFile}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", receiver.health)
	mux.HandleFunc("GET /alerts", receiver.alerts)
	mux.HandleFunc("POST /webhook/alerts", receiver.receive)

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	address := net.JoinHostPort(*host, strconv.Itoa(*port))
	server := &http.Server{
		Addr:              address,
		Handler:           securityHeaders(mux),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	fmt.Printf("alert receiver started: http://%s\n", address)
	fmt.Printf("webhook endpoint: http://%s/webhook/alerts\n", address)

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	case <-rootContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}

func (r *receiver) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (r *receiver) receive(w http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(w, request.Body, maxRequestBytes)
	defer request.Body.Close()
	decoder := json.NewDecoder(request.Body)
	decoder.UseNumber()
	var alert map[string]any
	if err := decoder.Decode(&alert); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body must be a JSON object"})
		return
	}
	if alert == nil || strings.TrimSpace(stringValue(alert["msg"])) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "alert msg is required"})
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body must contain one JSON object"})
		return
	}
	if err := r.append(alert); err != nil {
		fmt.Fprintf(os.Stderr, "store alert: %v\n", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store alert"})
		return
	}
	fmt.Printf("alert received: level=%s msg=%s\n", stringValue(alert["level"]), stringValue(alert["msg"]))
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (r *receiver) alerts(w http.ResponseWriter, request *http.Request) {
	limit := 100
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 1000"})
			return
		}
		limit = parsed
	}
	alerts, err := r.recent(limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read alerts: %v\n", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read alerts"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

func (r *receiver) append(alert map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(r.dataFile), 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(alert)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(r.dataFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func (r *receiver) recent(limit int) ([]map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	file, err := os.Open(r.dataFile)
	if errors.Is(err, os.ErrNotExist) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make([]map[string]any, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxRequestBytes)
	for scanner.Scan() {
		var alert map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &alert); err != nil {
			continue // A damaged historic line must not hide later alerts.
		}
		if len(result) == limit {
			copy(result, result[1:])
			result[len(result)-1] = alert
		} else {
			result = append(result, alert)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result, nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func stringValue(value any) string {
	stringValue, _ := value.(string)
	return stringValue
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
