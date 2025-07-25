package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Event represents a billing event with token usage
type Event struct {
	Timestamp        time.Time `json:"timestamp"`
	UserID           string    `json:"user_id"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	RequestID        string    `json:"request_id"`
	Enclave          string    `json:"enclave"`
	RequestPath      string    `json:"request_path"`
	Streaming        bool      `json:"streaming"`
	APIKey           string    `json:"api_key"`
}

// ShadowBillingRequest represents the payload sent to the control plane
type ShadowBillingRequest struct {
	Events []Event `json:"events"`
	Source string  `json:"source"` // "proxy" to distinguish from tfshim
}

// Collector collects billing events in memory and sends them to the control plane
type Collector struct {
	events        []Event
	mu            sync.Mutex
	controlPlane  string
	batchInterval time.Duration
	quit          chan struct{}
	wg            sync.WaitGroup
}

// NewCollector creates a new billing event collector
func NewCollector(controlPlaneURL string) *Collector {
	c := &Collector{
		events:        make([]Event, 0),
		controlPlane:  controlPlaneURL,
		batchInterval: 2 * time.Second, // Match tfshim's interval
		quit:          make(chan struct{}),
	}

	// Only start batch processing if control plane URL is provided
	if controlPlaneURL != "" {
		c.wg.Add(1)
		go c.processBatch()
		log.Infof("Billing collector started with control plane: %s", controlPlaneURL)
	} else {
		log.Info("Billing collector started in log-only mode (no control plane URL)")
	}

	return c
}

// AddEvent adds a billing event and logs it
func (c *Collector) AddEvent(event Event) {
	// Perform JSON marshalling outside the critical section
	eventJSON, err := json.Marshal(event)
	if err != nil {
		log.WithError(err).Error("Failed to marshal billing event")
		return
	}

	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()

	log.WithFields(log.Fields{
		"type": "billing_event",
		"data": string(eventJSON),
	}).Info("Billing event collected")
}

// GetEvents returns all collected events (for testing/debugging)
func (c *Collector) GetEvents() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Return a copy to avoid race conditions
	eventsCopy := make([]Event, len(c.events))
	copy(eventsCopy, c.events)
	return eventsCopy
}

// Clear removes all events (for testing)
func (c *Collector) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = c.events[:0]
}

// Stop gracefully shuts down the collector
func (c *Collector) Stop() {
	close(c.quit)
	c.wg.Wait()
}

// processBatch runs in a goroutine and periodically sends batched events
func (c *Collector) processBatch() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.batchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.sendBatch()
		case <-c.quit:
			// Send any remaining events before shutting down
			c.sendBatch()
			return
		}
	}
}

// sendBatch sends the current batch of events to the control plane
func (c *Collector) sendBatch() {
	c.mu.Lock()
	if len(c.events) == 0 {
		c.mu.Unlock()
		return
	}

	// Copy events and clear the queue
	batch := make([]Event, len(c.events))
	copy(batch, c.events)
	c.events = c.events[:0]
	c.mu.Unlock()

	// Create the request payload
	payload := ShadowBillingRequest{
		Events: batch,
		Source: "proxy",
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.WithError(err).Error("Failed to marshal billing batch")
		return
	}

	// Send to control plane
	url := fmt.Sprintf("%s/api/shim/collect-shadow", c.controlPlane)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		log.WithError(err).Error("Failed to create billing request")
		return
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		log.WithError(err).WithField("url", url).Error("Failed to send billing batch")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.WithFields(log.Fields{
			"status": resp.StatusCode,
			"url":    url,
		}).Error("Billing batch submission failed")
		return
	}

	log.WithField("event_count", len(batch)).Debug("Successfully sent billing batch")
}
