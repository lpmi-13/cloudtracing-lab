package main

import (
	"bytes"
	"context"
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

	"cloudtracing/internal/app"
	"cloudtracing/internal/scenario"
	"cloudtracing/pkg/telemetry"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type product struct {
	SKU   string  `json:"sku"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

type catalogServer struct {
	db         *sql.DB
	redis      *redis.Client
	httpClient *http.Client
	meiliURL   string
	scenarios  map[string]scenario.Definition
}

type meiliSearchResponse struct {
	Hits []struct {
		SKU   string  `json:"sku"`
		Name  string  `json:"name"`
		Price float64 `json:"price"`
	} `json:"hits"`
}

type meiliTaskResponse struct {
	TaskUID int `json:"taskUid"`
}

type meiliTaskStatus struct {
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func main() {
	ctx := context.Background()
	shutdown, err := telemetry.Init(ctx, "catalog-api")
	if err != nil {
		log.Fatalf("init telemetry: %v", err)
	}
	defer shutdown(context.Background())

	scenarios, err := app.LoadScenarios()
	if err != nil {
		log.Fatalf("load scenarios: %v", err)
	}

	dsn := mustEnv("POSTGRES_DSN")
	db, err := app.OpenPostgres("pgx", dsn)
	if err != nil {
		log.Fatalf("open postgres: %v", err)
	}

	redisClient := redis.NewClient(&redis.Options{Addr: mustEnv("REDIS_ADDR")})
	if err := app.WaitForDependency(ctx, "redis", 2*time.Minute, 2*time.Second, func(ctx context.Context) error {
		return redisClient.Ping(ctx).Err()
	}); err != nil {
		log.Fatalf("ping redis: %v", err)
	}

	s := &catalogServer{
		db:         db,
		redis:      redisClient,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		meiliURL:   strings.TrimRight(mustEnv("MEILISEARCH_URL"), "/"),
		scenarios:  scenarios,
	}

	if err := app.WaitForDependency(ctx, "meilisearch", 2*time.Minute, 2*time.Second, func(ctx context.Context) error {
		return s.seedSearchIndex(ctx)
	}); err != nil {
		log.Fatalf("seed search index: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/internal/search", otelhttp.NewHandler(http.HandlerFunc(s.search), "GET /internal/search"))
	mux.Handle("/internal/product", otelhttp.NewHandler(http.HandlerFunc(s.product), "GET /internal/product"))
	mux.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	addr := ":8080"
	log.Printf("catalog-api listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func (s *catalogServer) search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	app.AnnotateRequestSpanFromHeaders(ctx, r)
	q := r.URL.Query().Get("q")
	if q == "" {
		q = "trail"
	}

	fault := app.FaultForRequest(s.scenarios, r, "catalog-api")
	cacheKey := "search:" + strings.ToLower(q)
	if fault.Mode != "expensive_search_query" {
		if cached, ok := s.redisGetProducts(ctx, cacheKey); ok {
			app.WriteJSON(w, http.StatusOK, map[string]any{"products": cached, "cache": "hit"})
			return
		}
	} else {
		_, _ = s.redisGetProducts(ctx, cacheKey)
	}

	body, statement, latency := s.searchBody(q, fault)
	products, err := s.searchMeili(ctx, "catalog.search.meilisearch", statement, body, latency)
	if err != nil {
		http.Error(w, fmt.Sprintf("search meilisearch: %v", err), http.StatusInternalServerError)
		return
	}

	_ = s.redisSetProducts(ctx, cacheKey, products)
	app.WriteJSON(w, http.StatusOK, map[string]any{"products": products, "cache": "miss"})
}

func (s *catalogServer) product(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	app.AnnotateRequestSpanFromHeaders(ctx, r)
	tracer := otel.Tracer("catalog-api")
	sku := r.URL.Query().Get("sku")
	if sku == "" {
		http.Error(w, "missing sku", http.StatusBadRequest)
		return
	}

	cacheKey := "product:" + strings.ToLower(sku)
	if cached, ok := s.redisGetProduct(ctx, cacheKey); ok {
		app.WriteJSON(w, http.StatusOK, cached)
		return
	}

	stmt := "select sku, name, price from products where sku = $1"
	var p product
	err := app.QuerySpan(ctx, tracer, "products.detail.postgres", stmt, 0, func(ctx context.Context) error {
		return s.db.QueryRowContext(ctx, stmt, sku).Scan(&p.SKU, &p.Name, &p.Price)
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("load product: %v", err), http.StatusInternalServerError)
		return
	}

	_ = s.redisSetProduct(ctx, cacheKey, p)
	app.WriteJSON(w, http.StatusOK, p)
}

func (s *catalogServer) redisGetProducts(ctx context.Context, key string) ([]product, bool) {
	tracer := otel.Tracer("catalog-api")
	var payload []byte
	err := tracedSpan(ctx, tracer, "redis.search.cache.get", "redis", "GET "+key, 0, func(ctx context.Context) error {
		var err error
		payload, err = s.redis.Get(ctx, key).Bytes()
		return err
	})
	if err != nil || len(payload) == 0 {
		return nil, false
	}

	var products []product
	if err := json.Unmarshal(payload, &products); err != nil {
		return nil, false
	}
	return products, true
}

func (s *catalogServer) redisSetProducts(ctx context.Context, key string, products []product) error {
	tracer := otel.Tracer("catalog-api")
	payload, err := json.Marshal(products)
	if err != nil {
		return err
	}
	return tracedSpan(ctx, tracer, "redis.search.cache.set", "redis", "SETEX "+key, 0, func(ctx context.Context) error {
		return s.redis.SetEx(ctx, key, payload, 90*time.Second).Err()
	})
}

func (s *catalogServer) redisGetProduct(ctx context.Context, key string) (product, bool) {
	tracer := otel.Tracer("catalog-api")
	var payload []byte
	err := tracedSpan(ctx, tracer, "redis.product.cache.get", "redis", "GET "+key, 0, func(ctx context.Context) error {
		var err error
		payload, err = s.redis.Get(ctx, key).Bytes()
		return err
	})
	if err != nil || len(payload) == 0 {
		return product{}, false
	}

	var p product
	if err := json.Unmarshal(payload, &p); err != nil {
		return product{}, false
	}
	return p, true
}

func (s *catalogServer) redisSetProduct(ctx context.Context, key string, p product) error {
	tracer := otel.Tracer("catalog-api")
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return tracedSpan(ctx, tracer, "redis.product.cache.set", "redis", "SETEX "+key, 0, func(ctx context.Context) error {
		return s.redis.SetEx(ctx, key, payload, 5*time.Minute).Err()
	})
}

func (s *catalogServer) searchBody(q string, fault scenario.FaultSpec) ([]byte, string, time.Duration) {
	if fault.Mode == "expensive_search_query" {
		body := fmt.Sprintf(`{"q":%q,"limit":12,"attributesToRetrieve":["sku","name","price"]}`, q)
		return []byte(body), body, time.Duration(fault.LatencyMS) * time.Millisecond
	}

	body := fmt.Sprintf(`{"q":%q,"limit":12,"attributesToRetrieve":["sku","name","price"]}`, `"`+q+`"`)
	return []byte(body), body, 0
}

func (s *catalogServer) searchMeili(ctx context.Context, label, statement string, body []byte, latency time.Duration) ([]product, error) {
	tracer := otel.Tracer("catalog-api")
	var parsed meiliSearchResponse
	err := tracedSpan(ctx, tracer, label, "meilisearch", statement, latency, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.meiliURL+"/indexes/products/search", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode >= http.StatusBadRequest {
			payload, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("meilisearch status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
		}

		return json.NewDecoder(resp.Body).Decode(&parsed)
	})
	if err != nil {
		return nil, err
	}

	products := make([]product, 0, len(parsed.Hits))
	for _, hit := range parsed.Hits {
		products = append(products, product{
			SKU:   hit.SKU,
			Name:  hit.Name,
			Price: hit.Price,
		})
	}
	return products, nil
}

func (s *catalogServer) seedSearchIndex(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "select sku, name, description, price, updated_at from products order by sku")
	if err != nil {
		return err
	}
	defer rows.Close()

	docs := make([]map[string]any, 0, 180)
	for rows.Next() {
		var sku string
		var name string
		var description string
		var price float64
		var updatedAt time.Time
		if err := rows.Scan(&sku, &name, &description, &price, &updatedAt); err != nil {
			return err
		}

		docs = append(docs, map[string]any{
			"sku":         sku,
			"name":        name,
			"description": description,
			"price":       price,
			"updated_at":  updatedAt.Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	payload, err := json.Marshal(docs)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.meiliURL+"/indexes/products/documents?primaryKey=sku", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		payload, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("seed documents failed: %s", strings.TrimSpace(string(payload)))
	}

	var task meiliTaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return err
	}
	if task.TaskUID == 0 {
		return nil
	}
	return s.waitForMeiliTask(ctx, task.TaskUID)
}

func (s *catalogServer) waitForMeiliTask(ctx context.Context, uid int) error {
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.meiliURL+"/tasks/"+strconv.Itoa(uid), nil)
		if err != nil {
			return err
		}

		resp, err := s.httpClient.Do(req)
		if err != nil {
			return err
		}

		var task meiliTaskStatus
		if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
			resp.Body.Close()
			return err
		}
		resp.Body.Close()

		switch task.Status {
		case "succeeded":
			return nil
		case "failed", "canceled":
			if task.Error != nil && task.Error.Message != "" {
				return fmt.Errorf("meilisearch task %d failed: %s", uid, task.Error.Message)
			}
			return fmt.Errorf("meilisearch task %d failed with status %s", uid, task.Status)
		}

		time.Sleep(250 * time.Millisecond)
	}

	return fmt.Errorf("timed out waiting for meilisearch task %d", uid)
}

func tracedSpan(ctx context.Context, tracer trace.Tracer, name, system, statement string, latency time.Duration, fn func(context.Context) error) error {
	ctx, span := tracer.Start(ctx, name, trace.WithAttributes(
		attribute.String("db.system", system),
		attribute.String("db.statement", statement),
		attribute.String("lab.query_label", name),
		attribute.String("lab.statement_signature", appStatementSignature(statement)),
	))
	defer span.End()

	if latency > 0 {
		time.Sleep(latency)
	}

	if err := fn(ctx); err != nil {
		span.RecordError(err)
		return err
	}
	return nil
}

func appStatementSignature(statement string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(statement)), " ")
}

func mustEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf("%s is required", key)
	}
	return value
}
