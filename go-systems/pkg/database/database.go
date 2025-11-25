// Package database provides a MongoDB database connection manager with caching support.
// It implements an offline-first pattern with write queuing for resilience.
package database

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// QueuedOperation represents a pending database operation for offline mode.
type QueuedOperation struct {
	Collection string
	Query      bson.M
	Operation  string // "set" or "delete"
	Data       bson.M
}

// Config contains the database connection configuration.
type Config struct {
	// URI is the MongoDB connection string.
	URI string
	// DatabaseName is the name of the database to use.
	DatabaseName string
	// ConnectTimeout is the timeout for establishing a connection.
	ConnectTimeout time.Duration
	// MaxPoolSize is the maximum number of connections in the pool.
	MaxPoolSize uint64
}

// DefaultConfig returns a default database configuration.
func DefaultConfig() Config {
	return Config{
		DatabaseName:   "PancyBot",
		ConnectTimeout: 5 * time.Second,
		MaxPoolSize:    100,
	}
}

// Database manages the MongoDB connection and provides caching functionality.
type Database struct {
	client     *mongo.Client
	db         *mongo.Database
	config     Config
	mu         sync.RWMutex
	connected  bool
	writeQueue []QueuedOperation
	queueMu    sync.Mutex

	// Reconnection
	reconnectTicker *time.Ticker
	reconnectStop   chan struct{}
}

// New creates a new Database instance with the given configuration.
func New(config Config) *Database {
	return &Database{
		config:        config,
		writeQueue:    make([]QueuedOperation, 0),
		reconnectStop: make(chan struct{}),
	}
}

// Connect establishes a connection to MongoDB.
func (d *Database) Connect(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.connected {
		return nil
	}

	clientOptions := options.Client().
		ApplyURI(d.config.URI).
		SetServerSelectionTimeout(d.config.ConnectTimeout).
		SetMaxPoolSize(d.config.MaxPoolSize)

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Ping the database to verify connection
	pingCtx, cancel := context.WithTimeout(ctx, d.config.ConnectTimeout)
	defer cancel()

	if err := client.Ping(pingCtx, nil); err != nil {
		client.Disconnect(ctx)
		return fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	d.client = client
	d.db = client.Database(d.config.DatabaseName)
	d.connected = true

	// Stop any existing reconnection attempts
	if d.reconnectTicker != nil {
		d.reconnectTicker.Stop()
		d.reconnectTicker = nil
	}

	// Sync any queued operations
	go d.syncOfflineWrites()

	return nil
}

// handleDisconnection starts the reconnection loop.
func (d *Database) handleDisconnection() {
	d.mu.Lock()
	wasConnected := d.connected
	d.connected = false
	d.mu.Unlock()

	if !wasConnected {
		return
	}

	if d.reconnectTicker != nil {
		return // Already trying to reconnect
	}

	d.reconnectTicker = time.NewTicker(15 * time.Second)

	go func() {
		for {
			select {
			case <-d.reconnectTicker.C:
				ctx, cancel := context.WithTimeout(context.Background(), d.config.ConnectTimeout)
				if err := d.Connect(ctx); err == nil {
					cancel()
					return
				}
				cancel()
			case <-d.reconnectStop:
				return
			}
		}
	}()
}

// syncOfflineWrites processes the write queue when connection is restored.
func (d *Database) syncOfflineWrites() {
	d.queueMu.Lock()
	if len(d.writeQueue) == 0 {
		d.queueMu.Unlock()
		return
	}

	operations := make([]QueuedOperation, len(d.writeQueue))
	copy(operations, d.writeQueue)
	d.writeQueue = d.writeQueue[:0]
	d.queueMu.Unlock()

	ctx := context.Background()

	for _, op := range operations {
		collection := d.db.Collection(op.Collection)

		var err error
		switch op.Operation {
		case "set":
			_, err = collection.UpdateOne(ctx, op.Query, bson.M{"$set": op.Data}, options.Update().SetUpsert(true))
		case "delete":
			_, err = collection.DeleteOne(ctx, op.Query)
		}

		if err != nil {
			// Re-queue failed operation
			d.queueMu.Lock()
			d.writeQueue = append(d.writeQueue, op)
			d.queueMu.Unlock()
		}
	}
}

// AddToWriteQueue adds an operation to the offline write queue.
func (d *Database) AddToWriteQueue(op QueuedOperation) {
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	d.writeQueue = append(d.writeQueue, op)
}

// IsConnected returns the current connection status.
func (d *Database) IsConnected() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.connected
}

// Ping checks the database connection and returns the latency.
func (d *Database) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	if err := d.client.Ping(ctx, nil); err != nil {
		d.handleDisconnection()
		return 0, err
	}
	return time.Since(start), nil
}

// Disconnect closes the database connection.
func (d *Database) Disconnect(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.reconnectTicker != nil {
		d.reconnectTicker.Stop()
		close(d.reconnectStop)
	}

	if d.client != nil {
		if err := d.client.Disconnect(ctx); err != nil {
			return err
		}
	}

	d.connected = false
	return nil
}

// GetStatus returns the current database status.
func (d *Database) GetStatus() (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.client == nil {
		return "🔴 | Disconnected", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := d.client.Ping(ctx, nil); err != nil {
		return "🔴 | Disconnected", false
	}

	return "🟢 | Online", true
}

// Collection returns a reference to a MongoDB collection.
func (d *Database) Collection(name string) *mongo.Collection {
	return d.db.Collection(name)
}

// DB returns the underlying mongo.Database instance.
func (d *Database) DB() *mongo.Database {
	return d.db
}
