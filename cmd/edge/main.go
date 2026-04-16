package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"cloudtracing/internal/app"
	"cloudtracing/internal/httpx"
	"cloudtracing/pkg/telemetry"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type edgeServer struct {
	client       *http.Client
	catalogURL   string
	inventoryURL string
	ordersURL    string
	paymentsURL  string
}

type product struct {
	SKU   string  `json:"sku"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

type searchResponse struct {
	Products []product `json:"products"`
}

type reserveResponse struct {
	SKU       string `json:"sku"`
	Available int    `json:"available"`
	Reserved  bool   `json:"reserved"`
}

type paymentResponse struct {
	Status    string `json:"status"`
	Reference string `json:"reference"`
	Error     string `json:"error,omitempty"`
}

type availabilityResponse struct {
	Items map[string]int `json:"items"`
}

type orderRecord struct {
	OrderRef  string    `json:"order_ref"`
	SKU       string    `json:"sku"`
	Total     float64   `json:"total"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type ordersResponse struct {
	Orders []orderRecord `json:"orders"`
}

func main() {
	ctx := context.Background()
	shutdown, err := telemetry.Init(ctx, "edge-api")
	if err != nil {
		log.Fatalf("init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	s := &edgeServer{
		client:       &http.Client{Timeout: 8 * time.Second},
		catalogURL:   mustEnv("CATALOG_URL"),
		inventoryURL: mustEnv("INVENTORY_URL"),
		ordersURL:    mustEnv("ORDERS_URL"),
		paymentsURL:  mustEnv("PAYMENTS_URL"),
	}

	mux := http.NewServeMux()
	mux.Handle("/api/search", otelhttp.NewHandler(http.HandlerFunc(s.search), "GET /api/search"))
	mux.Handle("/api/checkout", otelhttp.NewHandler(http.HandlerFunc(s.checkout), "GET /api/checkout"))
	mux.Handle("/api/orders/history", otelhttp.NewHandler(http.HandlerFunc(s.history), "GET /api/orders/history"))
	mux.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	addr := ":8080"
	log.Printf("edge-api listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func (s *edgeServer) search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	app.AnnotateRequestSpanFromHeaders(ctx, r)
	q := r.URL.Query().Get("q")
	if q == "" {
		q = "trail"
	}
	scenarioID := app.ScenarioIDFromRequest(r)
	batchID := app.BatchIDFromRequest(r)

	var catalog searchResponse
	status, err := httpx.DoJSON(ctx, s.client, http.MethodGet, fmt.Sprintf("%s/internal/search?q=%s", s.catalogURL, url.QueryEscape(q)), nil, scenarioID, batchID, &catalog)
	if err != nil || status >= http.StatusBadRequest {
		http.Error(w, fmt.Sprintf("catalog search failed: %v", err), http.StatusBadGateway)
		return
	}

	skus := make([]string, 0, 3)
	for i, p := range catalog.Products {
		if i == 3 {
			break
		}
		skus = append(skus, p.SKU)
	}

	var availability availabilityResponse
	if len(skus) > 0 {
		_, _ = httpx.DoJSON(ctx, s.client, http.MethodGet, fmt.Sprintf("%s/internal/availability?skus=%s", s.inventoryURL, url.QueryEscape(strings.Join(skus, ","))), nil, scenarioID, batchID, &availability)
	}

	app.WriteJSON(w, http.StatusOK, map[string]any{
		"products":     catalog.Products,
		"availability": availability.Items,
	})
}

func (s *edgeServer) checkout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	app.AnnotateRequestSpanFromHeaders(ctx, r)
	sku := r.URL.Query().Get("sku")
	if sku == "" {
		sku = "sku-14"
	}
	scenarioID := app.ScenarioIDFromRequest(r)
	batchID := app.BatchIDFromRequest(r)

	var details product
	status, err := httpx.DoJSON(ctx, s.client, http.MethodGet, fmt.Sprintf("%s/internal/product?sku=%s", s.catalogURL, url.QueryEscape(sku)), nil, scenarioID, batchID, &details)
	if err != nil || status >= http.StatusBadRequest {
		http.Error(w, fmt.Sprintf("catalog detail failed: %v", err), http.StatusBadGateway)
		return
	}

	var reserve reserveResponse
	status, err = httpx.DoJSON(ctx, s.client, http.MethodGet, fmt.Sprintf("%s/internal/reserve?sku=%s", s.inventoryURL, url.QueryEscape(sku)), nil, scenarioID, batchID, &reserve)
	if err != nil || status >= http.StatusBadRequest {
		http.Error(w, fmt.Sprintf("inventory reserve failed: %v", err), http.StatusBadGateway)
		return
	}
	if !reserve.Reserved {
		http.Error(w, "item unavailable", http.StatusConflict)
		return
	}

	var payment paymentResponse
	status, err = httpx.DoJSON(ctx, s.client, http.MethodGet, fmt.Sprintf("%s/internal/charge?sku=%s&amount=%.2f", s.paymentsURL, url.QueryEscape(sku), details.Price), nil, scenarioID, batchID, &payment)
	if err != nil {
		http.Error(w, fmt.Sprintf("payment call failed: %v", err), http.StatusBadGateway)
		return
	}
	if status >= http.StatusBadRequest {
		app.WriteJSON(w, http.StatusBadGateway, map[string]any{
			"error":   "payment dependency failed",
			"payment": payment,
		})
		return
	}

	app.WriteJSON(w, http.StatusOK, map[string]any{
		"product":   details,
		"inventory": reserve,
		"payment":   payment,
	})
}

func (s *edgeServer) history(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	app.AnnotateRequestSpanFromHeaders(ctx, r)
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "user-4"
	}
	scenarioID := app.ScenarioIDFromRequest(r)
	batchID := app.BatchIDFromRequest(r)

	var orders ordersResponse
	status, err := httpx.DoJSON(ctx, s.client, http.MethodGet, fmt.Sprintf("%s/internal/history?user_id=%s", s.ordersURL, url.QueryEscape(userID)), nil, scenarioID, batchID, &orders)
	if err != nil || status >= http.StatusBadRequest {
		http.Error(w, fmt.Sprintf("order history failed: %v", err), http.StatusBadGateway)
		return
	}

	var featured product
	if len(orders.Orders) > 0 {
		_, _ = httpx.DoJSON(ctx, s.client, http.MethodGet, fmt.Sprintf("%s/internal/product?sku=%s", s.catalogURL, url.QueryEscape(orders.Orders[0].SKU)), nil, scenarioID, batchID, &featured)
	}

	app.WriteJSON(w, http.StatusOK, map[string]any{
		"orders":           orders.Orders,
		"featured_product": featured,
	})
}

func mustEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf("%s is required", key)
	}
	return strings.TrimRight(value, "/")
}
