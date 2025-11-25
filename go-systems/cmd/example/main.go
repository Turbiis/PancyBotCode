// Example application demonstrating how to use the Go essential systems.
// This file shows initialization and usage patterns for all packages.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Turbiis/PancyBotCode/go-systems/pkg/database"
	"github.com/Turbiis/PancyBotCode/go-systems/pkg/logger"
	"github.com/Turbiis/PancyBotCode/go-systems/pkg/mqtt"
	"github.com/Turbiis/PancyBotCode/go-systems/pkg/webserver"
	"go.mongodb.org/mongo-driver/bson"
)

func main() {
	// =========================================================================
	// 1. Initialize Logger
	// =========================================================================
	logConfig := logger.Config{
		LogLevel:     logger.SystemLevel,
		LogDir:       "logs",
		ErrorWebhook: os.Getenv("ERROR_WEBHOOK"),
		LogsWebhook:  os.Getenv("LOGS_WEBHOOK"),
		Version:      "0.2.0",
		MaxFileSize:  500 * 1024 * 1024, // 500MB
		MaxFiles:     5,
	}

	log, err := logger.New(logConfig)
	if err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Close()

	log.System("Starting PancyBot Go Systems Example", "MAIN")

	// =========================================================================
	// 2. Initialize Database
	// =========================================================================
	dbConfig := database.Config{
		URI:            os.Getenv("MONGODB_URI"),
		DatabaseName:   "PancyBot",
		ConnectTimeout: 5 * time.Second,
		MaxPoolSize:    100,
	}

	db := database.New(dbConfig)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	if err := db.Connect(ctx); err != nil {
		log.Warn(fmt.Sprintf("Failed to connect to database: %v", err), "DB")
		log.Info("Running in offline mode - writes will be queued", "DB")
	} else {
		log.Success("Connected to database", "DB")
	}
	cancel()
	defer db.Disconnect(context.Background())

	// Create DataManagers for different collections
	guildsManager := database.NewDataManager(db, "guilds", database.DataManagerOptions{
		MaxCacheSize: 1000,
	})

	usersManager := database.NewDataManager(db, "users", database.DataManagerOptions{
		MaxCacheSize: 2500,
	})

	// Example: Get a guild from database
	guild, err := guildsManager.Get(context.Background(), bson.M{"id": "123456789"})
	if err != nil {
		log.Warn(fmt.Sprintf("Error getting guild: %v", err), "DB")
	} else if guild != nil {
		log.Info(fmt.Sprintf("Found guild: %v", guild["id"]), "DB")
	}

	// Example: Set guild data
	_, err = guildsManager.Set(context.Background(), bson.M{"id": "123456789"}, bson.M{
		"name":    "Test Guild",
		"ownerId": "987654321",
	})
	if err != nil {
		log.Warn(fmt.Sprintf("Error setting guild: %v", err), "DB")
	}

	// =========================================================================
	// 3. Initialize MQTT Communication (optional)
	// =========================================================================
	var mqttComm *mqtt.Communicator

	mqttHost := os.Getenv("MQTT_HOST")
	if mqttHost != "" {
		mqttConfig := mqtt.Config{
			Host:     mqttHost,
			Port:     1883,
			Username: os.Getenv("MQTT_USER"),
			Password: os.Getenv("MQTT_PASSWORD"),
			ClientID: "pancybot_go_example",
		}

		mqttComm, err = mqtt.NewCommunicator(mqttConfig)
		if err != nil {
			log.Warn(fmt.Sprintf("Failed to connect to MQTT: %v", err), "MQTT")
		} else {
			log.Success("Connected to MQTT broker", "MQTT")
			defer mqttComm.Destroy()

			// Register a handler for music control requests
			mqttComm.On("music/+/play", func(payload map[string]interface{}) (interface{}, error) {
				topic := payload["_topic"].(string)
				log.Info(fmt.Sprintf("Received play request on topic: %s", topic), "MQTT")
				return map[string]interface{}{
					"success": true,
					"message": "Playback resumed",
				}, nil
			})

			// Example: Send a request
			go func() {
				time.Sleep(2 * time.Second)
				response, err := mqttComm.Request("status", nil, 5*time.Second)
				if err != nil {
					log.Debug(fmt.Sprintf("MQTT request error: %v", err), "MQTT")
				} else {
					log.Info(fmt.Sprintf("MQTT response: %v", response), "MQTT")
				}
			}()
		}
	}

	// =========================================================================
	// 4. Initialize Web Server
	// =========================================================================
	serverConfig := webserver.Config{
		Port:            8080,
		TrustProxy:      true,
		LogsWebhook:     os.Getenv("LOGS_WEBHOOK"),
		AllowedHosts:    ".*", // Allow all hosts for example
		RateLimit:       100,
		RateLimitWindow: time.Minute,
	}

	server, err := webserver.New(serverConfig)
	if err != nil {
		log.Error(err, "WEB")
		os.Exit(1)
	}

	// Register routes
	server.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		status, online := db.GetStatus()
		webserver.JSONResponse(w, http.StatusOK, map[string]interface{}{
			"status":   "ok",
			"database": status,
			"online":   online,
		})
	})

	server.HandleFunc("/api/guilds", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			webserver.ErrorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}

		guildID := r.URL.Query().Get("id")
		if guildID == "" {
			webserver.ErrorResponse(w, http.StatusBadRequest, "Guild ID required")
			return
		}

		guild, err := guildsManager.Get(r.Context(), bson.M{"id": guildID})
		if err != nil {
			webserver.ErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}

		if guild == nil {
			webserver.ErrorResponse(w, http.StatusNotFound, "Guild not found")
			return
		}

		webserver.JSONResponse(w, http.StatusOK, guild)
	})

	// Add static file serving (example)
	server.Handle("/public/", http.StripPrefix("/public/", http.FileServer(http.Dir("public"))))

	// =========================================================================
	// 5. Start Server with Graceful Shutdown
	// =========================================================================
	go func() {
		log.Info(fmt.Sprintf("Starting web server on port %d", serverConfig.Port), "WEB")
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "WEB")
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Warn("Shutting down...", "MAIN")

	// Graceful shutdown with timeout
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Error(err, "WEB")
	}

	log.Success("Server stopped gracefully", "MAIN")

	// Keep a reference to usersManager to avoid unused variable error
	_ = usersManager
}
