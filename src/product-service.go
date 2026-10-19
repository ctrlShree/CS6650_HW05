package main

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Product matches the Product schema in api.yaml.
type Product struct {
	ProductID    int32  `json:"product_id"`
	SKU          string `json:"sku"`
	Manufacturer string `json:"manufacturer"`
	CategoryID   int32  `json:"category_id"`
	Weight       int32  `json:"weight"`
	SomeOtherID  int32  `json:"some_other_id"`
}

// productInput is what the POST body is decoded into.
// A nil pointer means the field was missing from the JSON.
type productInput struct {
	ProductID    *int64  `json:"product_id"`
	SKU          *string `json:"sku"`
	Manufacturer *string `json:"manufacturer"`
	CategoryID   *int64  `json:"category_id"`
	Weight       *int64  `json:"weight"`
	SomeOtherID  *int64  `json:"some_other_id"`
}

// apiError matches the Error schema in api.yaml.
type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// productStore keeps products in memory, guarded by a read-write lock.
type productStore struct {
	mu       sync.RWMutex
	products map[int32]Product
}

func newProductStore() *productStore {
	return &productStore{products: make(map[int32]Product)}
}

func (s *productStore) get(id int32) (Product, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.products[id]
	return p, ok
}

// update replaces an existing product. It returns false if the ID isn't in the store.
func (s *productStore) update(p Product) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.products[p.ProductID]; !ok {
		return false
	}
	s.products[p.ProductID] = p
	return true
}

// seed fills the store with products 1..n at startup, because api.yaml
// has no endpoint that creates a product.
func (s *productStore) seed(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 1; i <= n; i++ {
		id := int32(i)
		s.products[id] = Product{
			ProductID:    id,
			SKU:          fmt.Sprintf("SKU-%06d", i),
			Manufacturer: "Acme Corporation",
			CategoryID:   int32(i%20 + 1),
			Weight:       int32(100 + (i*37)%4900),
			SomeOtherID:  id,
		}
	}
}

// parseProductID applies the api.yaml rules: integer, int32, minimum 1.
func parseProductID(raw string) (int32, error) {
	id, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("productId must be an integer between 1 and %d", math.MaxInt32)
	}
	return int32(id), nil
}

// validateProduct checks the Product schema rules from api.yaml.
func validateProduct(in productInput) (Product, []string) {
	var problems []string

	checkInt := func(name string, v *int64, lowest int64) int32 {
		if v == nil {
			problems = append(problems, name+" is required")
			return 0
		}
		if *v < lowest || *v > math.MaxInt32 {
			problems = append(problems, fmt.Sprintf("%s must be between %d and %d", name, lowest, math.MaxInt32))
			return 0
		}
		return int32(*v)
	}

	checkStr := func(name string, v *string, maxLen int) string {
		if v == nil {
			problems = append(problems, name+" is required")
			return ""
		}
		if n := utf8.RuneCountInString(*v); n < 1 || n > maxLen {
			problems = append(problems, fmt.Sprintf("%s must be 1 to %d characters long", name, maxLen))
			return ""
		}
		return *v
	}

	p := Product{
		ProductID:    checkInt("product_id", in.ProductID, 1),
		SKU:          checkStr("sku", in.SKU, 100),
		Manufacturer: checkStr("manufacturer", in.Manufacturer, 200),
		CategoryID:   checkInt("category_id", in.CategoryID, 1),
		Weight:       checkInt("weight", in.Weight, 0),
		SomeOtherID:  checkInt("some_other_id", in.SomeOtherID, 1),
	}
	return p, problems
}

// writeError sends a response in the api.yaml Error format.
func writeError(c *gin.Context, status int, code, message, details string) {
	c.AbortWithStatusJSON(status, apiError{Error: code, Message: message, Details: details})
}

// faultInjection forces a 500 so it can be demonstrated. It only fires when
// ENABLE_FAULT_INJECTION=true and the request has "X-Simulate-Error: 500".
func faultInjection(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if enabled && c.GetHeader("X-Simulate-Error") == "500" {
			panic("simulated failure")
		}
		c.Next()
	}
}

// RED metrics: two counters (rate and errors) and a histogram (duration).
// promauto registers them so /metrics serves them automatically.
var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "All HTTP requests, by method, route and status code.",
	}, []string{"method", "route", "status"})

	httpErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_request_errors_total",
		Help: "HTTP requests that finished with a non-2xx status code.",
	}, []string{"method", "route", "status"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Time the server took to handle a request, in seconds.",
		Buckets: []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	}, []string{"method", "route"})
)

// logger writes one JSON object per line to stdout. On ECS, stdout goes to CloudWatch Logs.
var logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

// observe times every request and records it in the metrics above.
// It runs outside the recovery middleware, so it still sees the final 500
// when a handler panics.
func observe() gin.HandlerFunc {
	return func(c *gin.Context) {
		route := c.FullPath() // the route template, e.g. /products/:productId
		if route == "/metrics" {
			c.Next() // don't count Prometheus's own scrapes
			return
		}
		if route == "" {
			route = "unmatched"
		}

		start := time.Now()
		c.Next() // run the handler
		elapsed := time.Since(start)

		status := c.Writer.Status()
		code := strconv.Itoa(status)
		method := c.Request.Method

		httpRequests.WithLabelValues(method, route, code).Inc()
		if status < 200 || status > 299 {
			httpErrors.WithLabelValues(method, route, code).Inc()
		}
		httpDuration.WithLabelValues(method, route).Observe(elapsed.Seconds())
		logger.Info("request",
			slog.String("endpoint", route),
			slog.String("method", method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", status),
			slog.Float64("duration_ms", float64(elapsed.Nanoseconds())/1e6),
		)
	}
}

func main() {
	store := newProductStore()
	store.seed(100)

	r := gin.New()
	// Prometheus reads metrics from here. Not part of api.yaml.
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.Use(observe(), gin.CustomRecovery(func(c *gin.Context, recovered any) {
		// api.yaml requires an Error body on 500.
		writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", "unexpected server error")
	}))

	products := r.Group("/products", faultInjection(os.Getenv("ENABLE_FAULT_INJECTION") == "true"))

	// GET /products/{productId}: 200, 404, 500.
	products.GET("/:productId", func(c *gin.Context) {
		id, err := parseProductID(c.Param("productId"))
		if err != nil {
			// api.yaml lists no 400 for GET, and nothing can exist at an invalid ID.
			writeError(c, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product not found", err.Error())
			return
		}
		p, ok := store.get(id)
		if !ok {
			writeError(c, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product not found",
				fmt.Sprintf("no product with ID %d", id))
			return
		}
		c.JSON(http.StatusOK, p)
	})

	// POST /products/{productId}/details: 204, 400, 404, 500.
	products.POST("/:productId/details", func(c *gin.Context) {
		id, err := parseProductID(c.Param("productId"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_INPUT", "Invalid input data", err.Error())
			return
		}

		var in productInput
		if err := c.ShouldBindJSON(&in); err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_INPUT", "Invalid input data", "invalid JSON: "+err.Error())
			return
		}

		p, problems := validateProduct(in)
		if len(problems) > 0 {
			writeError(c, http.StatusBadRequest, "INVALID_INPUT", "Invalid input data", strings.Join(problems, "; "))
			return
		}

		if p.ProductID != id {
			writeError(c, http.StatusBadRequest, "INVALID_INPUT", "Invalid input data",
				fmt.Sprintf("product_id in the body (%d) does not match productId in the path (%d)", p.ProductID, id))
			return
		}

		if !store.update(p) {
			writeError(c, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product not found",
				fmt.Sprintf("no product with ID %d", id))
			return
		}

		c.Status(http.StatusNoContent)
	})

	r.Run(":8080")
}
