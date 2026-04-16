package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"cloudtracing/internal/app"
	"cloudtracing/internal/scenario"
	"cloudtracing/pkg/telemetry"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
)

type orderRecord struct {
	OrderRef  string    `json:"order_ref"`
	SKU       string    `json:"sku"`
	Total     float64   `json:"total"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type ordersServer struct {
	db        *sql.DB
	scenarios map[string]scenario.Definition
}

func main() {
	ctx := context.Background()
	shutdown, err := telemetry.Init(ctx, "orders-api")
	if err != nil {
		log.Fatalf("init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	scenarios, err := app.LoadScenarios()
	if err != nil {
		log.Fatalf("load scenarios: %v", err)
	}

	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatal("POSTGRES_DSN is required")
	}

	db, err := app.OpenPostgres("pgx", dsn)
	if err != nil {
		log.Fatalf("open postgres: %v", err)
	}

	s := &ordersServer{db: db, scenarios: scenarios}

	mux := http.NewServeMux()
	mux.Handle("/internal/history", otelhttp.NewHandler(http.HandlerFunc(s.history), "GET /internal/history"))
	mux.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	addr := ":8080"
	log.Printf("orders-api listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func (s *ordersServer) history(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	app.AnnotateRequestSpanFromHeaders(ctx, r)
	tracer := otel.Tracer("orders-api")
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "user-1"
	}

	fault := app.FaultForRequest(s.scenarios, r, "orders-api")
	stmt := "select order_ref, sku, total, status, created_at from orders where user_id = $1 order by created_at desc limit 20"
	label := "orders.history.indexed"
	latency := time.Duration(0)

	if fault.Mode == "expensive_sort" {
		stmt = fault.QueryText
		label = fault.QueryLabel
		latency = time.Duration(fault.LatencyMS) * time.Millisecond
	}

	orders := make([]orderRecord, 0, 20)
	err := app.QuerySpan(ctx, tracer, label, stmt, latency, func(ctx context.Context) error {
		rows, err := s.db.QueryContext(ctx, stmt, userID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var rec orderRecord
			if err := rows.Scan(&rec.OrderRef, &rec.SKU, &rec.Total, &rec.Status, &rec.CreatedAt); err != nil {
				return err
			}
			orders = append(orders, rec)
		}
		return rows.Err()
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("load history: %v", err), http.StatusInternalServerError)
		return
	}

	app.WriteJSON(w, http.StatusOK, map[string]any{"orders": orders})
}
