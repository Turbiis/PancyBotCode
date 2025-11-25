# PancyBot Go Essential Systems

Este directorio contiene los sistemas esenciales de PancyBot implementados en Go. Estos paquetes proporcionan funcionalidades comunes que pueden ser utilizadas en diferentes componentes del ecosistema PancyBot.

## 📦 Paquetes Disponibles

| Paquete | Descripción |
|---------|-------------|
| `pkg/logger` | Sistema de logging estructurado con soporte para consola, archivos y webhooks de Discord |
| `pkg/mqtt` | Cliente MQTT con patrón request-response para comunicación entre servicios |
| `pkg/database` | Gestión de conexión MongoDB con caché LRU y modo offline |
| `pkg/webserver` | Servidor HTTP con middleware de rate limiting, logging y validación de hosts |

## 🚀 Instalación

```bash
# Clonar el repositorio
git clone https://github.com/Turbiis/PancyBotCode.git
cd PancyBotCode/go-systems

# Descargar dependencias
go mod download

# Compilar
go build ./...
```

## 📚 Uso de los Paquetes

### Logger

El paquete `logger` proporciona un sistema de logging flexible con múltiples niveles y salidas.

```go
import "github.com/Turbiis/PancyBotCode/go-systems/pkg/logger"

// Configuración
config := logger.Config{
    LogLevel:     logger.SystemLevel,  // Nivel máximo a registrar
    LogDir:       "logs",              // Directorio para archivos de log
    ErrorWebhook: "https://discord...", // Webhook para errores
    LogsWebhook:  "https://discord...", // Webhook para logs generales
    Version:      "1.0.0",             // Versión de la aplicación
    MaxFileSize:  500 * 1024 * 1024,   // 500MB por archivo
    MaxFiles:     5,                   // Archivos a mantener
}

// Crear instancia
log, err := logger.New(config)
if err != nil {
    panic(err)
}
defer log.Close()

// Uso
log.Info("Mensaje informativo", "PREFIX")
log.Success("Operación exitosa", "DB")
log.Warn("Advertencia", "API")
log.Error(err, "MQTT")
log.Critical("Error crítico", "SYSTEM")
log.Debug("Información de debug", "DEV")
log.System("Mensaje del sistema", "SYS")
```

**Niveles de Log:**
- `CriticalLevel` (0) - Errores críticos que requieren atención inmediata
- `ErrorLevel` (1) - Errores que deben ser investigados
- `WarnLevel` (2) - Advertencias sobre posibles problemas
- `SuccessLevel` (3) - Operaciones exitosas
- `InfoLevel` (4) - Mensajes informativos
- `DebugLevel` (5) - Información de depuración
- `SystemLevel` (6) - Mensajes a nivel de sistema

### MQTT Communicator

El paquete `mqtt` implementa comunicación MQTT con patrón request-response usando IDs de correlación.

```go
import "github.com/Turbiis/PancyBotCode/go-systems/pkg/mqtt"

// Configuración
config := mqtt.Config{
    Host:     "localhost",
    Port:     1883,
    Username: "user",
    Password: "pass",
    ClientID: "mi_servicio",
}

// Crear instancia
comm, err := mqtt.NewCommunicator(config)
if err != nil {
    panic(err)
}
defer comm.Destroy()

// Publicar mensaje simple (sin esperar respuesta)
err = comm.Publish("topic/notificacion", map[string]interface{}{
    "mensaje": "Hola mundo",
})

// Hacer una petición y esperar respuesta (5 segundos timeout)
response, err := comm.Request("music/play", map[string]interface{}{
    "guildId": "123456789",
}, 5*time.Second)

// Registrar un handler para responder peticiones
err = comm.On("music/+/play", func(payload map[string]interface{}) (interface{}, error) {
    guildID := extractGuildID(payload["_topic"].(string))
    
    // Procesar la petición...
    
    return map[string]interface{}{
        "success": true,
        "message": "Reproducción iniciada",
    }, nil
})
```

**Estructura de Topics:**
- Peticiones: `pancy/request/{topic}`
- Respuestas: `pancy/response/{topic}/{correlationId}`

### Database

El paquete `database` proporciona gestión de conexión MongoDB con caché LRU y soporte para modo offline.

```go
import (
    "github.com/Turbiis/PancyBotCode/go-systems/pkg/database"
    "go.mongodb.org/mongo-driver/bson"
)

// Configuración
config := database.Config{
    URI:            "mongodb://localhost:27017",
    DatabaseName:   "PancyBot",
    ConnectTimeout: 5 * time.Second,
    MaxPoolSize:    100,
}

// Crear y conectar
db := database.New(config)
ctx := context.Background()

if err := db.Connect(ctx); err != nil {
    log.Warn("DB offline, usando modo en cola")
}
defer db.Disconnect(ctx)

// Crear DataManager para una colección
guildsManager := database.NewDataManager(db, "guilds", database.DataManagerOptions{
    MaxCacheSize: 1000,
    CacheTTL:     0, // Sin expiración
})

// Obtener documento (usa caché si está disponible)
guild, err := guildsManager.Get(ctx, bson.M{"id": "123456789"})

// Crear/Actualizar documento
_, err = guildsManager.Set(ctx, bson.M{"id": "123456789"}, bson.M{
    "name":    "Mi Server",
    "ownerId": "987654321",
})

// Eliminar documento
err = guildsManager.Delete(ctx, bson.M{"id": "123456789"})

// Buscar múltiples documentos
results, err := guildsManager.Find(ctx, bson.M{"premium": true})

// Verificar estado
status, isOnline := db.GetStatus()
latency, err := db.Ping(ctx)
```

**Características:**
- Caché LRU con tamaño configurable
- Modo offline con cola de escrituras
- Reconexión automática
- TTL opcional para entradas de caché

### Web Server

El paquete `webserver` proporciona un servidor HTTP con middleware integrado.

```go
import "github.com/Turbiis/PancyBotCode/go-systems/pkg/webserver"

// Configuración
config := webserver.Config{
    Port:            8080,
    TrustProxy:      true,                    // Confiar en X-Forwarded-For
    LogsWebhook:     "https://discord...",    // Webhook para logs
    AllowedHosts:    `^(.+\.)?midominio\.com$`, // Regex para hosts permitidos
    RateLimit:       100,                     // Peticiones por ventana
    RateLimitWindow: time.Minute,             // Ventana de tiempo
}

// Crear servidor
server, err := webserver.New(config)
if err != nil {
    panic(err)
}

// Registrar rutas
server.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
    webserver.JSONResponse(w, http.StatusOK, map[string]string{
        "status": "ok",
    })
})

server.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        webserver.ErrorResponse(w, http.StatusMethodNotAllowed, "Método no permitido")
        return
    }
    
    // Procesar petición...
    webserver.JSONResponse(w, http.StatusOK, data)
})

// Servir archivos estáticos
server.Handle("/public/", http.StripPrefix("/public/", 
    http.FileServer(http.Dir("public"))))

// Iniciar servidor
go func() {
    if err := server.Start(); err != nil && err != http.ErrServerClosed {
        log.Error(err, "WEB")
    }
}()

// Apagar gracefully
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
server.Shutdown(ctx)
```

**Middleware Incluido:**
- Rate Limiting por IP
- Logging de peticiones
- Validación de hosts
- Notificaciones a Discord

## 🔧 Variables de Entorno

| Variable | Descripción | Requerido |
|----------|-------------|-----------|
| `MONGODB_URI` | URI de conexión a MongoDB | Sí |
| `MQTT_HOST` | Host del broker MQTT | No |
| `MQTT_USER` | Usuario MQTT | No |
| `MQTT_PASSWORD` | Contraseña MQTT | No |
| `ERROR_WEBHOOK` | Webhook de Discord para errores | No |
| `LOGS_WEBHOOK` | Webhook de Discord para logs | No |

## 📁 Estructura del Proyecto

```
go-systems/
├── cmd/
│   └── example/
│       └── main.go          # Ejemplo de uso completo
├── docs/
│   └── README.md            # Esta documentación
├── pkg/
│   ├── database/
│   │   ├── database.go      # Conexión y gestión de MongoDB
│   │   └── datamanager.go   # Gestor de datos con caché
│   ├── logger/
│   │   └── logger.go        # Sistema de logging
│   ├── mqtt/
│   │   └── communicator.go  # Cliente MQTT request-response
│   └── webserver/
│       └── server.go        # Servidor HTTP con middleware
├── go.mod
└── go.sum
```

## 🧪 Ejecutar el Ejemplo

```bash
# Configurar variables de entorno
export MONGODB_URI="mongodb://localhost:27017"
export MQTT_HOST="localhost"

# Ejecutar
cd go-systems
go run cmd/example/main.go
```

## 🔄 Integración con TypeScript

Estos paquetes Go están diseñados para ser compatibles con el sistema existente en TypeScript:

- **MQTT**: Usa el mismo esquema de topics (`pancy/request/`, `pancy/response/`)
- **Database**: Estructura de documentos compatible con los schemas de Mongoose
- **Logger**: Mismo formato de niveles y notificaciones a Discord

## 📝 Licencia

Este proyecto está bajo la licencia MIT. Ver [LICENSE](../LICENSE) para más detalles.

---

Desarrollado con 💫 por PancyStudio
