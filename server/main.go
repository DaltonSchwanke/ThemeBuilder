package main

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

type App struct {
	DB *pgxpool.Pool
}

func main() {
	_ = godotenv.Load(".env") // or just ".env" if you run from backend/
	dsn := mustGetenv("DATABASE_URL")
	port := getenv("API_PORT", "8080")
	origins := strings.Split(getenv("CORS_ALLOWED_ORIGINS", "http://localhost:4200"), ",")

	// logging
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{}))
	slog.SetDefault(logger)

	// DB connect
	pool := mustDB(dsn)
	defer pool.Close()

	app := &App{DB: pool}

	// router
	r := chi.NewRouter()
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	r.Use(recoverer, loggerMW)

	// routes
	r.Get("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	r.Get("/v1/themes", app.listThemes) // example domain endpoint

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// start
	errCh := make(chan error, 1)
	go func() {
		slog.Info("api: listening", "port", port)
		errCh <- srv.ListenAndServe()
	}()

	// graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		log.Fatal(err)
	case <-sigCh:
		slog.Info("api: shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func (a *App) listThemes(w http.ResponseWriter, r *http.Request) {
	// Create the table once (or use migrations). Example schema:
	// CREATE TABLE IF NOT EXISTS themes (
	//   id BIGSERIAL PRIMARY KEY,
	//   name TEXT NOT NULL,
	//   color1 TEXT NOT NULL,
	//   color2 TEXT NOT NULL,
	//   color3 TEXT NOT NULL,
	//   owner TEXT NOT NULL,
	//   created_at TIMESTAMPTZ DEFAULT now()
	// );

	rows, err := a.DB.Query(r.Context(),
		`SELECT id, name, color1, color2, color3, owner, created_at
		   FROM themes
		   ORDER BY id DESC
		   LIMIT 50`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	type Theme struct {
		ID        int64     `json:"id"`
		Name      string    `json:"name"`
		Color1    string    `json:"color1"`
		Color2    string    `json:"color2"`
		Color3    string    `json:"color3"`
		Owner     string    `json:"owner"`
		CreatedAt time.Time `json:"created_at"`
	}

	var out []Theme
	for rows.Next() {
		var t Theme
		if err := rows.Scan(&t.ID, &t.Name, &t.Color1, &t.Color2, &t.Color3, &t.Owner, &t.CreatedAt); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func mustDB(dsn string) *pgxpool.Pool {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("db parse: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		log.Fatalf("db new: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}
	return pool
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func loggerMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"dur_ms", time.Since(start).Milliseconds(),
			"ip", r.RemoteAddr,
		)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func mustGetenv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing env %s", k)
	}
	return v
}