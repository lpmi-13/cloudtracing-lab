package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"cloudtracing/internal/app"
	"cloudtracing/internal/scenario"
	"cloudtracing/pkg/telemetry"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
)

type inventoryServer struct {
	db        *sql.DB
	scenarios map[string]scenario.Definition
}

func main() {
	ctx := context.Background()
	shutdown, err := telemetry.Init(ctx, "inventory-api")
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

	s := &inventoryServer{db: db, scenarios: scenarios}

	mux := http.NewServeMux()
	mux.Handle("/internal/reserve", otelhttp.NewHandler(http.HandlerFunc(s.reserve), "GET /internal/reserve"))
	mux.Handle("/internal/availability", otelhttp.NewHandler(http.HandlerFunc(s.availability), "GET /internal/availability"))
	mux.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	addr := ":8080"
	log.Printf("inventory-api listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func (s *inventoryServer) reserve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	app.AnnotateRequestSpanFromHeaders(ctx, r)
	tracer := otel.Tracer("inventory-api")
	sku := r.URL.Query().Get("sku")
	if sku == "" {
		http.Error(w, "missing sku", http.StatusBadRequest)
		return
	}

	fault := app.FaultForRequest(s.scenarios, r, "inventory-api")
	totalAvailable := 0

	switch fault.Mode {
	case "n_plus_one_queries":
		repeat := fault.Repeat
		if repeat <= 0 {
			repeat = 6
		}

		for warehouseID := 1; warehouseID <= repeat; warehouseID++ {
			stmt := fault.QueryText
			label := fault.QueryLabel
			latency := time.Duration(fault.LatencyMS) * time.Millisecond

			var quantity int
			var reserved int
			err := app.QuerySpan(ctx, tracer, label, stmt, latency, func(ctx context.Context) error {
				return s.db.QueryRowContext(ctx, stmt, sku, warehouseID).Scan(new(int), &quantity, &reserved)
			})
			if err != nil {
				http.Error(w, fmt.Sprintf("reserve inventory: %v", err), http.StatusInternalServerError)
				return
			}
			totalAvailable += quantity - reserved
		}
	default:
		stmt := "select coalesce(sum(quantity - reserved), 0) from stock_levels where sku = $1"
		err := app.QuerySpan(ctx, tracer, "inventory.reserve.check_stock", stmt, 0, func(ctx context.Context) error {
			return s.db.QueryRowContext(ctx, stmt, sku).Scan(&totalAvailable)
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("reserve inventory: %v", err), http.StatusInternalServerError)
			return
		}
	}

	app.WriteJSON(w, http.StatusOK, map[string]any{
		"sku":       sku,
		"available": totalAvailable,
		"reserved":  totalAvailable > 0,
	})
}

func (s *inventoryServer) availability(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	app.AnnotateRequestSpanFromHeaders(ctx, r)
	tracer := otel.Tracer("inventory-api")
	raw := r.URL.Query().Get("skus")
	if raw == "" {
		app.WriteJSON(w, http.StatusOK, map[string]any{"items": map[string]int{}})
		return
	}

	items := map[string]int{}
	skus := strings.Split(raw, ",")
	stmt := "select coalesce(sum(quantity - reserved), 0) from stock_levels where sku = $1"
	for _, sku := range skus {
		sku = strings.TrimSpace(sku)
		if sku == "" {
			continue
		}

		var available int
		err := app.QuerySpan(ctx, tracer, "stock.availability.lookup", stmt, 0, func(ctx context.Context) error {
			return s.db.QueryRowContext(ctx, stmt, sku).Scan(&available)
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("availability lookup: %v", err), http.StatusInternalServerError)
			return
		}
		items[sku] = available
	}

	app.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
