// Package database provides data management with caching capabilities.
package database

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DataManagerOptions configures the DataManager behavior.
type DataManagerOptions struct {
	// MaxCacheSize is the maximum number of items to cache. -1 for unlimited.
	MaxCacheSize int
	// CacheTTL is the time-to-live for cached items. 0 for no expiration.
	CacheTTL time.Duration
}

// DefaultDataManagerOptions returns default DataManager options.
func DefaultDataManagerOptions() DataManagerOptions {
	return DataManagerOptions{
		MaxCacheSize: 1000,
		CacheTTL:     0,
	}
}

// cacheEntry represents a cached item with optional expiration.
type cacheEntry struct {
	data      interface{}
	timestamp time.Time
}

// DataManager provides cached access to a MongoDB collection.
type DataManager struct {
	collection     *mongo.Collection
	collectionName string
	db             *Database
	cache          map[string]*cacheEntry
	cacheOrder     []string // For LRU eviction
	cacheMu        sync.RWMutex
	options        DataManagerOptions
}

// NewDataManager creates a new DataManager for the specified collection.
func NewDataManager(db *Database, collectionName string, opts DataManagerOptions) *DataManager {
	return &DataManager{
		collection:     db.Collection(collectionName),
		collectionName: collectionName,
		db:             db,
		cache:          make(map[string]*cacheEntry),
		cacheOrder:     make([]string, 0),
		options:        opts,
	}
}

// generateCacheKey creates a unique cache key from a query.
func (dm *DataManager) generateCacheKey(query bson.M) string {
	// Sort keys for consistent key generation
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	sortedQuery := make(map[string]interface{})
	for _, k := range keys {
		sortedQuery[k] = query[k]
	}

	data, _ := json.Marshal(sortedQuery)
	return fmt.Sprintf("%s:%s", dm.collectionName, string(data))
}

// moveToFront moves a key to the front of the LRU list (most recently used).
func (dm *DataManager) moveToFront(key string) {
	// Remove from current position
	for i, k := range dm.cacheOrder {
		if k == key {
			dm.cacheOrder = append(dm.cacheOrder[:i], dm.cacheOrder[i+1:]...)
			break
		}
	}
	// Add to front
	dm.cacheOrder = append(dm.cacheOrder, key)
}

// evictOldest removes the oldest item from the cache.
func (dm *DataManager) evictOldest() {
	if len(dm.cacheOrder) > 0 {
		oldestKey := dm.cacheOrder[0]
		dm.cacheOrder = dm.cacheOrder[1:]
		delete(dm.cache, oldestKey)
	}
}

// isExpired checks if a cache entry has expired.
func (dm *DataManager) isExpired(entry *cacheEntry) bool {
	if dm.options.CacheTTL == 0 {
		return false
	}
	return time.Since(entry.timestamp) > dm.options.CacheTTL
}

// Get retrieves a document from cache or database.
func (dm *DataManager) Get(ctx context.Context, query bson.M) (bson.M, error) {
	cacheKey := dm.generateCacheKey(query)

	// Check cache first
	dm.cacheMu.RLock()
	if entry, ok := dm.cache[cacheKey]; ok {
		if !dm.isExpired(entry) {
			dm.cacheMu.RUnlock()
			// Update LRU order
			dm.cacheMu.Lock()
			dm.moveToFront(cacheKey)
			dm.cacheMu.Unlock()
			if data, ok := entry.data.(bson.M); ok {
				return data, nil
			}
		}
	}
	dm.cacheMu.RUnlock()

	// If not in cache or expired, fetch from database
	if !dm.db.IsConnected() {
		return nil, fmt.Errorf("database is not connected")
	}

	var result bson.M
	err := dm.collection.FindOne(ctx, query).Decode(&result)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to fetch document: %w", err)
	}

	// Update cache
	dm.cacheMu.Lock()
	defer dm.cacheMu.Unlock()

	dm.cache[cacheKey] = &cacheEntry{
		data:      result,
		timestamp: time.Now(),
	}
	dm.moveToFront(cacheKey)

	// Evict if over max size
	if dm.options.MaxCacheSize > 0 && len(dm.cache) > dm.options.MaxCacheSize {
		dm.evictOldest()
	}

	return result, nil
}

// Set updates or inserts a document in the database and cache.
func (dm *DataManager) Set(ctx context.Context, query bson.M, data bson.M) (bson.M, error) {
	cacheKey := dm.generateCacheKey(query)

	if dm.db.IsConnected() {
		opts := options.FindOneAndUpdate().
			SetUpsert(true).
			SetReturnDocument(options.After)

		var result bson.M
		err := dm.collection.FindOneAndUpdate(ctx, query, bson.M{"$set": data}, opts).Decode(&result)
		if err != nil && err != mongo.ErrNoDocuments {
			// Queue operation on error
			dm.db.AddToWriteQueue(QueuedOperation{
				Collection: dm.collectionName,
				Query:      query,
				Operation:  "set",
				Data:       data,
			})
			return nil, fmt.Errorf("failed to update document: %w", err)
		}

		// Update cache
		dm.cacheMu.Lock()
		dm.cache[cacheKey] = &cacheEntry{
			data:      result,
			timestamp: time.Now(),
		}
		dm.moveToFront(cacheKey)

		// Evict if over max size
		if dm.options.MaxCacheSize > 0 && len(dm.cache) > dm.options.MaxCacheSize {
			dm.evictOldest()
		}
		dm.cacheMu.Unlock()

		return result, nil
	}

	// Database offline - queue the write
	dm.db.AddToWriteQueue(QueuedOperation{
		Collection: dm.collectionName,
		Query:      query,
		Operation:  "set",
		Data:       data,
	})

	return nil, fmt.Errorf("database offline, operation queued")
}

// Delete removes a document from the database and cache.
func (dm *DataManager) Delete(ctx context.Context, query bson.M) error {
	cacheKey := dm.generateCacheKey(query)

	// Remove from cache
	dm.cacheMu.Lock()
	delete(dm.cache, cacheKey)
	for i, k := range dm.cacheOrder {
		if k == cacheKey {
			dm.cacheOrder = append(dm.cacheOrder[:i], dm.cacheOrder[i+1:]...)
			break
		}
	}
	dm.cacheMu.Unlock()

	if dm.db.IsConnected() {
		_, err := dm.collection.DeleteOne(ctx, query)
		if err != nil {
			dm.db.AddToWriteQueue(QueuedOperation{
				Collection: dm.collectionName,
				Query:      query,
				Operation:  "delete",
			})
			return fmt.Errorf("failed to delete document: %w", err)
		}
		return nil
	}

	// Database offline - queue the delete
	dm.db.AddToWriteQueue(QueuedOperation{
		Collection: dm.collectionName,
		Query:      query,
		Operation:  "delete",
	})

	return fmt.Errorf("database offline, operation queued")
}

// Find retrieves multiple documents matching the query.
func (dm *DataManager) Find(ctx context.Context, query bson.M, opts ...*options.FindOptions) ([]bson.M, error) {
	if !dm.db.IsConnected() {
		return nil, fmt.Errorf("database is not connected")
	}

	cursor, err := dm.collection.Find(ctx, query, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to find documents: %w", err)
	}
	defer cursor.Close(ctx)

	var results []bson.M
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("failed to decode documents: %w", err)
	}

	return results, nil
}

// CacheSize returns the current number of items in the cache.
func (dm *DataManager) CacheSize() int {
	dm.cacheMu.RLock()
	defer dm.cacheMu.RUnlock()
	return len(dm.cache)
}

// ClearCache removes all items from the cache.
func (dm *DataManager) ClearCache() {
	dm.cacheMu.Lock()
	defer dm.cacheMu.Unlock()
	dm.cache = make(map[string]*cacheEntry)
	dm.cacheOrder = make([]string, 0)
}

// PrimeCache is a placeholder that logs cache initialization.
// In this implementation, the cache is populated on demand.
func (dm *DataManager) PrimeCache() {
	// Cache is filled on demand - nothing to do here
}
