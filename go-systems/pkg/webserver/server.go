// Package webserver provides an HTTP server with middleware support.
// It includes rate limiting, logging, and Discord webhook integration.
package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// Config contains the web server configuration.
type Config struct {
	// Port is the port to listen on.
	Port int
	// TrustProxy enables trusting X-Forwarded-For headers.
	TrustProxy bool
	// LogsWebhook is the Discord webhook URL for request logging.
	LogsWebhook string
	// AllowedHosts is a regex pattern for allowed hosts.
	AllowedHosts string
	// RateLimit is the maximum requests per window.
	RateLimit int
	// RateLimitWindow is the time window for rate limiting.
	RateLimitWindow time.Duration
}

// DefaultConfig returns a default web server configuration.
func DefaultConfig() Config {
	return Config{
		Port:            3000,
		TrustProxy:      false,
		AllowedHosts:    ".*",
		RateLimit:       100,
		RateLimitWindow: time.Minute,
	}
}

// Server is an HTTP server with middleware support.
type Server struct {
	config      Config
	mux         *http.ServeMux
	httpServer  *http.Server
	rateLimiter *RateLimiter
	webhook     *webhookClient
	hostRegex   *regexp.Regexp
}

// New creates a new Server instance.
func New(config Config) (*Server, error) {
	hostRegex, err := regexp.Compile(config.AllowedHosts)
	if err != nil {
		return nil, fmt.Errorf("invalid allowed hosts pattern: %w", err)
	}

	s := &Server{
		config:    config,
		mux:       http.NewServeMux(),
		hostRegex: hostRegex,
		rateLimiter: NewRateLimiter(RateLimiterConfig{
			Limit:  config.RateLimit,
			Window: config.RateLimitWindow,
		}),
	}

	if config.LogsWebhook != "" {
		s.webhook = &webhookClient{
			url:    config.LogsWebhook,
			client: &http.Client{Timeout: 10 * time.Second},
		}
	}

	return s, nil
}

// HandleFunc registers a handler function for the given pattern.
func (s *Server) HandleFunc(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
}

// Handle registers a handler for the given pattern.
func (s *Server) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
}

// Start begins listening for HTTP requests.
func (s *Server) Start() error {
	handler := s.applyMiddleware(s.mux)

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.config.Port),
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// applyMiddleware wraps the handler with all middleware.
func (s *Server) applyMiddleware(handler http.Handler) http.Handler {
	// Apply middleware in reverse order (last applied = first executed)
	handler = s.loggingMiddleware(handler)
	handler = s.rateLimitMiddleware(handler)
	handler = s.hostValidationMiddleware(handler)
	return handler
}

// hostValidationMiddleware validates the request host.
func (s *Server) hostValidationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostRegex.MatchString(r.Host) {
			s.logSuspiciousRequest(r)
			// Close connection without response for suspicious requests
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					conn.Close()
					return
				}
			}
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware enforces rate limiting.
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := s.getClientIP(r)
		if !s.rateLimiter.Allow(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Demasiadas solicitudes, por favor intente de nuevo más tarde.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware logs requests to console and webhook.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		duration := time.Since(start)

		// Log to console
		fmt.Printf("[LOG] %s %s - %d (%v)\n", r.Method, r.URL.Path, wrapped.statusCode, duration)

		// Log to webhook if configured
		if s.webhook != nil && s.hostRegex.MatchString(r.Host) {
			go s.webhook.logRequest(r, wrapped.statusCode, duration)
		}
	})
}

// getClientIP extracts the client IP address from the request.
func (s *Server) getClientIP(r *http.Request) string {
	if s.config.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return xff
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}
	return r.RemoteAddr
}

// logSuspiciousRequest logs suspicious requests to the webhook.
func (s *Server) logSuspiciousRequest(r *http.Request) {
	fmt.Printf("[LOG] Suspicious request: %s %s from %s\n", r.Method, r.URL.Path, r.RemoteAddr)
	if s.webhook != nil {
		go s.webhook.logSuspicious(r)
	}
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// webhookClient handles Discord webhook notifications.
type webhookClient struct {
	url    string
	client *http.Client
}

// DiscordEmbed represents a Discord embed for webhook messages.
type DiscordEmbed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Color       int    `json:"color"`
	Timestamp   string `json:"timestamp"`
}

// DiscordMessage represents a Discord webhook message.
type DiscordMessage struct {
	Embeds []DiscordEmbed `json:"embeds"`
}

func (wc *webhookClient) logRequest(r *http.Request, status int, duration time.Duration) {
	embed := DiscordEmbed{
		Title:       fmt.Sprintf("💫 | New %s request", r.Method),
		Description: fmt.Sprintf("> **Path:** `%s`\n> **IP:** `%s`\n> **Status:** `%d`\n> **Duration:** `%v`", r.URL.Path, r.RemoteAddr, status, duration),
		Color:       0x00AE86,
		Timestamp:   time.Now().Format(time.RFC3339),
	}

	msg := DiscordMessage{Embeds: []DiscordEmbed{embed}}
	data, err := json.Marshal(msg)
	if err != nil {
		fmt.Printf("[WEBHOOK] Failed to marshal log request: %v\n", err)
		return
	}

	req, err := http.NewRequest("POST", wc.url, bytes.NewBuffer(data))
	if err != nil {
		fmt.Printf("[WEBHOOK] Failed to create log request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	wc.client.Do(req)
}

func (wc *webhookClient) logSuspicious(r *http.Request) {
	embed := DiscordEmbed{
		Title:       fmt.Sprintf("💫 | Suspicious Request Rejected: %s %s", r.Method, r.URL.Path),
		Description: fmt.Sprintf("> **Path:** `%s`\n> **IP:** `%s`\n> **Host:** `%s`", r.URL.Path, r.RemoteAddr, r.Host),
		Color:       0xFFA500, // Orange
		Timestamp:   time.Now().Format(time.RFC3339),
	}

	msg := DiscordMessage{Embeds: []DiscordEmbed{embed}}
	data, err := json.Marshal(msg)
	if err != nil {
		fmt.Printf("[WEBHOOK] Failed to marshal suspicious request: %v\n", err)
		return
	}

	req, err := http.NewRequest("POST", wc.url, bytes.NewBuffer(data))
	if err != nil {
		fmt.Printf("[WEBHOOK] Failed to create suspicious request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	wc.client.Do(req)
}

// RateLimiterConfig configures the rate limiter.
type RateLimiterConfig struct {
	Limit  int
	Window time.Duration
}

// RateLimiter implements a simple rate limiter using a sliding window.
type RateLimiter struct {
	config   RateLimiterConfig
	requests map[string][]time.Time
	mu       sync.RWMutex
}

// NewRateLimiter creates a new RateLimiter.
func NewRateLimiter(config RateLimiterConfig) *RateLimiter {
	rl := &RateLimiter{
		config:   config,
		requests: make(map[string][]time.Time),
	}

	// Start cleanup goroutine
	go rl.cleanup()

	return rl
}

// Allow checks if a request from the given key should be allowed.
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-rl.config.Window)

	// Get existing requests for this key
	requests := rl.requests[key]

	// Filter out old requests
	var validRequests []time.Time
	for _, t := range requests {
		if t.After(windowStart) {
			validRequests = append(validRequests, t)
		}
	}

	// Check if under limit
	if len(validRequests) >= rl.config.Limit {
		rl.requests[key] = validRequests
		return false
	}

	// Add current request
	validRequests = append(validRequests, now)
	rl.requests[key] = validRequests

	return true
}

// cleanup periodically removes old entries from the rate limiter.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rl.config.Window)
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		windowStart := now.Add(-rl.config.Window)

		for key, requests := range rl.requests {
			var validRequests []time.Time
			for _, t := range requests {
				if t.After(windowStart) {
					validRequests = append(validRequests, t)
				}
			}
			if len(validRequests) == 0 {
				delete(rl.requests, key)
			} else {
				rl.requests[key] = validRequests
			}
		}
		rl.mu.Unlock()
	}
}

// JSONResponse is a helper function to send JSON responses.
func JSONResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// ErrorResponse is a helper function to send error responses.
func ErrorResponse(w http.ResponseWriter, status int, message string) {
	JSONResponse(w, status, map[string]string{"error": message})
}
