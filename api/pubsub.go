package main

// Google Cloud Pub/Sub REST client for asynchronous rich-media request processing.
//
// References:
//   https://cloud.google.com/pubsub/docs/reference/rest/v1/projects.topics/publish
//   https://cloud.google.com/pubsub/docs/reference/rest/v1/projects.subscriptions/pull
//   https://cloud.google.com/pubsub/docs/reference/rest/v1/projects.subscriptions/acknowledge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	pubsubBaseURL = "https://pubsub.googleapis.com/v1"

	// Environment variable names that configure Pub/Sub for the rich-media queue.
	envPubSubRichMediaTopic = "PUBSUB_RICH_MEDIA_TOPIC"
	envPubSubRichMediaSub   = "PUBSUB_RICH_MEDIA_SUBSCRIPTION"
)

// pubSubRawMessage is the wire format returned by the pull API before
// the caller decodes the data field.
type pubSubRawMessage struct {
	AckID   string `json:"ackId"`
	Message struct {
		Data       string            `json:"data"` // base64-encoded payload
		Attributes map[string]string `json:"attributes"`
		MessageID  string            `json:"messageId"`
	} `json:"message"`
}

// pubSubClient issues authenticated requests against the Cloud Pub/Sub REST API.
// It borrows the application's existing HTTP client and access-token function so
// that no additional dependency or credential management is needed.
type pubSubClient struct {
	projectID  string
	httpClient *http.Client
	getToken   func() (string, error)
}

func newPubSubClient(projectID string, httpClient *http.Client, getToken func() (string, error)) *pubSubClient {
	return &pubSubClient{
		projectID:  projectID,
		httpClient: httpClient,
		getToken:   getToken,
	}
}

// publish sends a single message to the given topic (short name, not the full
// "projects/…/topics/…" resource path).  attrs may be nil.
func (c *pubSubClient) publish(topic string, data []byte, attrs map[string]string) error {
	token, err := c.getToken()
	if err != nil {
		return fmt.Errorf("pubsub publish: get token: %w", err)
	}

	msg := map[string]interface{}{
		"data": base64.StdEncoding.EncodeToString(data),
	}
	if len(attrs) > 0 {
		msg["attributes"] = attrs
	}

	// Use a pooled buffer to avoid per-call heap allocation.
	buf := getBuf()
	defer putBuf(buf)
	if err := json.NewEncoder(buf).Encode(map[string]interface{}{
		"messages": []map[string]interface{}{msg},
	}); err != nil {
		return fmt.Errorf("pubsub publish: marshal: %w", err)
	}

	reqBody := bytes.NewReader(buf.Bytes())
	apiURL := fmt.Sprintf("%s/projects/%s/topics/%s:publish", pubsubBaseURL, c.projectID, topic)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, reqBody)
	if err != nil {
		return fmt.Errorf("pubsub publish: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("pubsub publish: http: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("pubsub publish: status %d: %s", resp.StatusCode, truncateForLog(string(respBody), 400))
	}
	return nil
}

// pull retrieves up to maxMessages messages from the given subscription
// (short name).  Returns an empty slice (not an error) when there are no
// pending messages.
func (c *pubSubClient) pull(subscription string, maxMessages int) ([]pubSubRawMessage, error) {
	token, err := c.getToken()
	if err != nil {
		return nil, fmt.Errorf("pubsub pull: get token: %w", err)
	}

	body, err := json.Marshal(map[string]interface{}{"maxMessages": maxMessages})
	if err != nil {
		return nil, fmt.Errorf("pubsub pull: marshal: %w", err)
	}

	url := fmt.Sprintf("%s/projects/%s/subscriptions/%s:pull", pubsubBaseURL, c.projectID, subscription)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("pubsub pull: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pubsub pull: http: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("pubsub pull: read body: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pubsub pull: status %d: %s", resp.StatusCode, truncateForLog(string(respBody), 400))
	}

	var result struct {
		ReceivedMessages []pubSubRawMessage `json:"receivedMessages"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("pubsub pull: unmarshal: %w", err)
	}
	return result.ReceivedMessages, nil
}

// acknowledge confirms processing of the given ack IDs, preventing redelivery.
func (c *pubSubClient) acknowledge(subscription string, ackIDs []string) error {
	if len(ackIDs) == 0 {
		return nil
	}
	token, err := c.getToken()
	if err != nil {
		return fmt.Errorf("pubsub ack: get token: %w", err)
	}

	body, err := json.Marshal(map[string]interface{}{"ackIds": ackIDs})
	if err != nil {
		return fmt.Errorf("pubsub ack: marshal: %w", err)
	}

	url := fmt.Sprintf("%s/projects/%s/subscriptions/%s:acknowledge", pubsubBaseURL, c.projectID, subscription)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("pubsub ack: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("pubsub ack: http: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("pubsub ack: status %d: %s", resp.StatusCode, truncateForLog(string(respBody), 400))
	}
	return nil
}

// ensureTopicExists creates the Pub/Sub topic if it does not already exist.
// A 409 Conflict response (already exists) is treated as success.
func (c *pubSubClient) ensureTopicExists(topic string) error {
	token, err := c.getToken()
	if err != nil {
		return fmt.Errorf("pubsub ensure-topic: get token: %w", err)
	}

	url := fmt.Sprintf("%s/projects/%s/topics/%s", pubsubBaseURL, c.projectID, topic)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, url, bytes.NewBufferString("{}"))
	if err != nil {
		return fmt.Errorf("pubsub ensure-topic: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("pubsub ensure-topic: http: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("pubsub ensure-topic: status %d: %s", resp.StatusCode, truncateForLog(string(respBody), 400))
	}
	return nil
}

// ensureSubscriptionExists creates a pull subscription attached to topic if it
// does not already exist.  A 409 Conflict response is treated as success.
// The ack deadline is 60 seconds, giving workers enough time to complete TTS
// and Vertex AI calls before Pub/Sub redelivers the message.
func (c *pubSubClient) ensureSubscriptionExists(subscription, topic string) error {
	token, err := c.getToken()
	if err != nil {
		return fmt.Errorf("pubsub ensure-sub: get token: %w", err)
	}

	body, err := json.Marshal(map[string]interface{}{
		"topic":              fmt.Sprintf("projects/%s/topics/%s", c.projectID, topic),
		"ackDeadlineSeconds": 60,
	})
	if err != nil {
		return fmt.Errorf("pubsub ensure-sub: marshal: %w", err)
	}

	url := fmt.Sprintf("%s/projects/%s/subscriptions/%s", pubsubBaseURL, c.projectID, subscription)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, url, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("pubsub ensure-sub: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("pubsub ensure-sub: http: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("pubsub ensure-sub: status %d: %s", resp.StatusCode, truncateForLog(string(respBody), 400))
	}
	return nil
}
