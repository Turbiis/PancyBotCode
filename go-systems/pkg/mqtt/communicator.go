// Package mqtt provides MQTT communication utilities for the PancyBot system.
// It implements request-response patterns using correlation IDs for async communication.
package mqtt

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
)

// Config contains the MQTT connection configuration.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	ClientID string
}

// Request represents an MQTT request with a correlation ID.
type Request struct {
	CorrelationID string      `json:"correlationId"`
	Payload       interface{} `json:"payload,omitempty"`
}

// Response represents an MQTT response.
type Response struct {
	CorrelationID string      `json:"correlationId"`
	Data          interface{} `json:"data"`
	Error         string      `json:"error,omitempty"`
}

// Communicator handles MQTT communication with request-response pattern.
type Communicator struct {
	client           mqtt.Client
	responseHandlers map[string]chan Response
	handlersMu       sync.RWMutex
	clientID         string
}

// NewCommunicator creates a new MQTT communicator instance.
func NewCommunicator(config Config) (*Communicator, error) {
	comm := &Communicator{
		responseHandlers: make(map[string]chan Response),
		clientID:         config.ClientID,
	}

	opts := mqtt.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%d", config.Host, config.Port)).
		SetClientID(fmt.Sprintf("%s_%s", config.ClientID, uuid.New().String())).
		SetUsername(config.Username).
		SetPassword(config.Password).
		SetDefaultPublishHandler(comm.defaultMessageHandler)

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return nil, fmt.Errorf("failed to connect to MQTT broker: %w", token.Error())
	}

	comm.client = client
	return comm, nil
}

// defaultMessageHandler processes incoming MQTT messages for responses.
func (c *Communicator) defaultMessageHandler(client mqtt.Client, msg mqtt.Message) {
	if strings.HasPrefix(msg.Topic(), "pancy/response/") {
		var response Response
		if err := json.Unmarshal(msg.Payload(), &response); err != nil {
			return
		}

		c.handlersMu.RLock()
		if ch, ok := c.responseHandlers[response.CorrelationID]; ok {
			select {
			case ch <- response:
			default:
			}
		}
		c.handlersMu.RUnlock()
	}
}

// Publish sends a message to the specified topic without expecting a response.
func (c *Communicator) Publish(topic string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	token := c.client.Publish(topic, 0, false, data)
	token.Wait()
	return token.Error()
}

// Request sends a request and waits for a response with the specified timeout.
func (c *Communicator) Request(topic string, payload interface{}, timeout time.Duration) (interface{}, error) {
	correlationID := uuid.New().String()
	requestTopic := fmt.Sprintf("pancy/request/%s", topic)
	responseTopic := fmt.Sprintf("pancy/response/%s/%s", topic, correlationID)

	// Create response channel
	responseChan := make(chan Response, 1)
	c.handlersMu.Lock()
	c.responseHandlers[correlationID] = responseChan
	c.handlersMu.Unlock()

	// Cleanup function
	defer func() {
		c.handlersMu.Lock()
		delete(c.responseHandlers, correlationID)
		c.handlersMu.Unlock()
		c.client.Unsubscribe(responseTopic)
	}()

	// Subscribe to response topic
	if token := c.client.Subscribe(responseTopic, 0, nil); token.Wait() && token.Error() != nil {
		return nil, fmt.Errorf("failed to subscribe to response topic: %w", token.Error())
	}

	// Publish request
	request := Request{
		CorrelationID: correlationID,
		Payload:       payload,
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	if token := c.client.Publish(requestTopic, 0, false, data); token.Wait() && token.Error() != nil {
		return nil, fmt.Errorf("failed to publish request: %w", token.Error())
	}

	// Wait for response or timeout
	select {
	case response := <-responseChan:
		if response.Error != "" {
			return nil, errors.New(response.Error)
		}
		return response.Data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("request to '%s' timed out after %v", topic, timeout)
	}
}

// RequestHandler is a function that handles incoming MQTT requests.
type RequestHandler func(payload map[string]interface{}) (interface{}, error)

// On registers a handler for requests on the specified topic.
// The handler will be called when a request is received, and its response
// will be published back to the requester.
func (c *Communicator) On(requestTopic string, handler RequestHandler) error {
	topic := fmt.Sprintf("pancy/request/%s", requestTopic)

	messageHandler := func(client mqtt.Client, msg mqtt.Message) {
		// Check if topic matches (with wildcard support)
		if !c.topicMatches(topic, msg.Topic()) {
			return
		}

		var request Request
		if err := json.Unmarshal(msg.Payload(), &request); err != nil {
			return
		}

		// Extract actual request topic
		actualRequestTopic := strings.TrimPrefix(msg.Topic(), "pancy/request/")
		responseTopic := fmt.Sprintf("pancy/response/%s/%s", actualRequestTopic, request.CorrelationID)

		// Prepare payload with topic info
		payloadMap := make(map[string]interface{})
		if request.Payload != nil {
			if pm, ok := request.Payload.(map[string]interface{}); ok {
				payloadMap = pm
			}
		}
		payloadMap["_topic"] = actualRequestTopic

		// Execute handler
		var response Response
		response.CorrelationID = request.CorrelationID

		data, err := handler(payloadMap)
		if err != nil {
			response.Error = err.Error()
		} else {
			response.Data = data
		}

		// Publish response
		responseData, err := json.Marshal(response)
		if err != nil {
			// Log error but don't fail - send empty response
			response = Response{CorrelationID: request.CorrelationID, Error: "failed to marshal response"}
			responseData, _ = json.Marshal(response)
		}
		if token := c.client.Publish(responseTopic, 0, false, responseData); token.Wait() && token.Error() != nil {
			fmt.Printf("[MQTT] Failed to publish response: %v\n", token.Error())
		}
	}

	if token := c.client.Subscribe(topic, 0, messageHandler); token.Wait() && token.Error() != nil {
		return fmt.Errorf("failed to subscribe to topic '%s': %w", topic, token.Error())
	}

	return nil
}

// topicMatches checks if a received topic matches a pattern (with wildcard support).
func (c *Communicator) topicMatches(pattern, topic string) bool {
	patternParts := strings.Split(pattern, "/")
	topicParts := strings.Split(topic, "/")

	for i, part := range patternParts {
		if part == "#" {
			// # matches everything from this point on
			return true
		}
		if i >= len(topicParts) {
			// Pattern has more parts than topic
			return false
		}
		if part == "+" {
			continue // + matches any single level
		}
		if part != topicParts[i] {
			return false
		}
	}

	// All pattern parts matched; ensure topic doesn't have extra parts
	return len(patternParts) == len(topicParts)
}

// Destroy closes the MQTT connection gracefully.
func (c *Communicator) Destroy() {
	if c.client != nil && c.client.IsConnected() {
		c.client.Disconnect(250)
	}
}

// IsConnected returns true if the client is connected to the broker.
func (c *Communicator) IsConnected() bool {
	return c.client != nil && c.client.IsConnected()
}
