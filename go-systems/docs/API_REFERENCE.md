# PancyBot Go Systems - API Reference

## Database Package (`pkg/database`)

### Types

#### `Config`
```go
type Config struct {
    URI            string        // MongoDB connection string
    DatabaseName   string        // Name of the database
    ConnectTimeout time.Duration // Connection timeout
    MaxPoolSize    uint64        // Maximum connection pool size
}
```

#### `QueuedOperation`
```go
type QueuedOperation struct {
    Collection string  // Collection name
    Query      bson.M  // Query filter
    Operation  string  // "set" or "delete"
    Data       bson.M  // Data for set operations
}
```

#### `DataManagerOptions`
```go
type DataManagerOptions struct {
    MaxCacheSize int           // Maximum cache size (-1 for unlimited)
    CacheTTL     time.Duration // Cache TTL (0 for no expiration)
}
```

### Database Methods

| Method | Description |
|--------|-------------|
| `New(config Config) *Database` | Creates a new Database instance |
| `Connect(ctx context.Context) error` | Connects to MongoDB |
| `Disconnect(ctx context.Context) error` | Closes the connection |
| `IsConnected() bool` | Returns connection status |
| `Ping(ctx context.Context) (time.Duration, error)` | Tests connection and returns latency |
| `GetStatus() (string, bool)` | Returns status string and online flag |
| `Collection(name string) *mongo.Collection` | Returns a collection reference |
| `AddToWriteQueue(op QueuedOperation)` | Queues an operation for offline mode |

### DataManager Methods

| Method | Description |
|--------|-------------|
| `NewDataManager(db, collectionName, opts)` | Creates a new DataManager |
| `Get(ctx, query bson.M) (bson.M, error)` | Gets a document (cache-first) |
| `Set(ctx, query, data bson.M) (bson.M, error)` | Updates/inserts a document |
| `Delete(ctx, query bson.M) error` | Deletes a document |
| `Find(ctx, query bson.M, opts...) ([]bson.M, error)` | Finds multiple documents |
| `CacheSize() int` | Returns current cache size |
| `ClearCache()` | Clears all cached items |

---

## Logger Package (`pkg/logger`)

### Log Levels

| Level | Value | Color | Description |
|-------|-------|-------|-------------|
| `CriticalLevel` | 0 | Bold Red | Critical errors |
| `ErrorLevel` | 1 | Red | Errors |
| `WarnLevel` | 2 | Yellow | Warnings |
| `SuccessLevel` | 3 | Green | Success messages |
| `InfoLevel` | 4 | Cyan | Informational |
| `DebugLevel` | 5 | Magenta | Debug info |
| `SystemLevel` | 6 | Blue | System messages |

### Types

#### `Config`
```go
type Config struct {
    LogLevel     Level  // Minimum level to log
    LogDir       string // Directory for log files
    ErrorWebhook string // Discord webhook for errors
    LogsWebhook  string // Discord webhook for general logs
    Version      string // Application version
    MaxFileSize  int64  // Max file size before rotation
    MaxFiles     int    // Max rotated files to keep
}
```

### Logger Methods

| Method | Description |
|--------|-------------|
| `New(config Config) (*Logger, error)` | Creates a new logger |
| `Critical(message, prefix string)` | Logs critical message |
| `Error(err error, prefix string)` | Logs error with stack trace |
| `ErrorMsg(message, prefix string)` | Logs error message string |
| `Warn(message, prefix string)` | Logs warning |
| `Success(message, prefix string)` | Logs success |
| `Info(message, prefix string)` | Logs info |
| `Debug(message, prefix string)` | Logs debug |
| `System(message, prefix string)` | Logs system message |
| `Close() error` | Closes log files |

### Global Functions

```go
logger.Init(config)      // Initialize global logger
logger.Default()         // Get global logger instance
logger.Info(msg, prefix) // Use global logger
```

---

## MQTT Package (`pkg/mqtt`)

### Types

#### `Config`
```go
type Config struct {
    Host     string // MQTT broker host
    Port     int    // MQTT broker port
    Username string // Authentication username
    Password string // Authentication password
    ClientID string // Client identifier
}
```

#### `Request`
```go
type Request struct {
    CorrelationID string      `json:"correlationId"`
    Payload       interface{} `json:"payload,omitempty"`
}
```

#### `Response`
```go
type Response struct {
    CorrelationID string      `json:"correlationId"`
    Data          interface{} `json:"data"`
    Error         string      `json:"error,omitempty"`
}
```

#### `RequestHandler`
```go
type RequestHandler func(payload map[string]interface{}) (interface{}, error)
```

### Communicator Methods

| Method | Description |
|--------|-------------|
| `NewCommunicator(config Config) (*Communicator, error)` | Creates new instance |
| `Publish(topic string, payload interface{}) error` | Publishes without waiting |
| `Request(topic string, payload interface{}, timeout time.Duration) (interface{}, error)` | Request-response |
| `On(requestTopic string, handler RequestHandler) error` | Registers request handler |
| `Destroy()` | Closes connection |
| `IsConnected() bool` | Returns connection status |

### Topic Patterns

- Supports `+` wildcard: `music/+/play` matches `music/123/play`
- Supports `#` wildcard: `events/#` matches `events/guild/join`

---

## WebServer Package (`pkg/webserver`)

### Types

#### `Config`
```go
type Config struct {
    Port            int           // Listen port
    TrustProxy      bool          // Trust X-Forwarded-For
    LogsWebhook     string        // Discord webhook for logs
    AllowedHosts    string        // Regex for allowed hosts
    RateLimit       int           // Requests per window
    RateLimitWindow time.Duration // Rate limit window
}
```

### Server Methods

| Method | Description |
|--------|-------------|
| `New(config Config) (*Server, error)` | Creates new server |
| `HandleFunc(pattern string, handler http.HandlerFunc)` | Registers handler func |
| `Handle(pattern string, handler http.Handler)` | Registers handler |
| `Start() error` | Starts listening |
| `Shutdown(ctx context.Context) error` | Graceful shutdown |

### Helper Functions

| Function | Description |
|----------|-------------|
| `JSONResponse(w, status, data)` | Sends JSON response |
| `ErrorResponse(w, status, message)` | Sends error response |

### Middleware (Applied Automatically)

1. **Host Validation** - Validates request host against allowed pattern
2. **Rate Limiting** - Limits requests per IP per time window
3. **Logging** - Logs requests to console and Discord webhook

---

## Error Handling

All packages follow Go conventions for error handling:

```go
// Check errors explicitly
result, err := someOperation()
if err != nil {
    log.Error(err, "CONTEXT")
    return
}

// Use error wrapping for context
if err := db.Connect(ctx); err != nil {
    return fmt.Errorf("failed to initialize database: %w", err)
}
```

---

## Thread Safety

All packages are designed to be thread-safe:

- `Database`: Uses mutex for connection state
- `DataManager`: Uses RWMutex for cache operations
- `Logger`: Uses mutex for file writes
- `Communicator`: Uses mutex for response handlers
- `Server`: Uses mutex for rate limiter

---

## Best Practices

1. **Always defer cleanup functions**
   ```go
   log, _ := logger.New(config)
   defer log.Close()
   
   db := database.New(config)
   defer db.Disconnect(ctx)
   ```

2. **Use context for timeouts**
   ```go
   ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
   defer cancel()
   result, err := db.Ping(ctx)
   ```

3. **Handle offline mode gracefully**
   ```go
   if !db.IsConnected() {
       // Operations will be queued automatically
       log.Warn("Operating in offline mode", "DB")
   }
   ```

4. **Use appropriate log levels**
   - `Critical`: System failures requiring immediate attention
   - `Error`: Errors that should be investigated
   - `Warn`: Potential issues that don't stop operation
   - `Info`: General operational messages
   - `Debug`: Detailed debugging information
   - `System`: Low-level system messages
