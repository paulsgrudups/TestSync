package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

type Message struct {
	Command string          `json:"command"`
	Content json.RawMessage `json:"content"`
}

func main() {
	httpURL := getEnv("TESTSYNC_HTTP_URL", "http://localhost:9104")
	wsURL := getEnv("TESTSYNC_WS_URL", "ws://localhost:9105")
	username := getEnv("TESTSYNC_USER", "exampleUserName")
	password := getEnv("TESTSYNC_PASS", "examplePassWord")

	testID := 12345
	payload := []byte("payload-e2e")

	if err := httpCreate(httpURL, testID, payload, username, password); err != nil {
		exitErr(err)
	}

	if err := httpRead(httpURL, testID, payload, username, password); err != nil {
		exitErr(err)
	}

	if err := wsFlow(wsURL, testID, payload, username, password); err != nil {
		exitErr(err)
	}

	fmt.Println("E2E flow completed successfully")
}

func httpCreate(baseURL string, testID int, payload []byte, user, pass string) error {
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/tests/%d", baseURL, testID), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, pass)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	return nil
}

func httpRead(baseURL string, testID int, payload []byte, user, pass string) error {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/tests/%d", baseURL, testID), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, pass)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	read, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if !bytes.Equal(read, payload) {
		return fmt.Errorf("GET returned unexpected payload: %q", string(read))
	}

	return nil
}

func wsFlow(baseURL string, testID int, payload []byte, user, pass string) error {
	url := fmt.Sprintf("%s/register/%d", baseURL, testID)
	header := http.Header{}
	if user != "" || pass != "" {
		header.Set("Authorization", "Basic "+basicAuth(user, pass))
	}

	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := writeWS(conn, "read_data", map[string]string{}); err != nil {
		return err
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	if !bytes.Equal(msg, payload) {
		return fmt.Errorf("WS read_data returned unexpected payload: %q", string(msg))
	}

	if err := writeWS(conn, "get_connection_count", map[string]string{}); err != nil {
		return err
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err = conn.ReadMessage()
	if err != nil {
		return err
	}

	var countMsg Message
	if err := json.Unmarshal(msg, &countMsg); err != nil {
		return fmt.Errorf("failed to unmarshal count message: %w", err)
	}
	if countMsg.Command != "get_connection_count" {
		return fmt.Errorf("unexpected WS command: %s", countMsg.Command)
	}
	var countPayload struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(countMsg.Content, &countPayload); err != nil {
		return fmt.Errorf("failed to parse count payload: %w", err)
	}
	if countPayload.Count < 1 {
		return fmt.Errorf("unexpected connection count: %d", countPayload.Count)
	}

	if err := writeWS(conn, "wait_checkpoint", map[string]interface{}{
		"identifier":   "checkpoint-1",
		"target_count": 1,
	}); err != nil {
		return err
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err = conn.ReadMessage()
	if err != nil {
		return err
	}

	var checkpointMsg Message
	if err := json.Unmarshal(msg, &checkpointMsg); err != nil {
		return fmt.Errorf("failed to unmarshal checkpoint message: %w", err)
	}
	if checkpointMsg.Command != "wait_checkpoint" {
		return fmt.Errorf("unexpected checkpoint command: %s", checkpointMsg.Command)
	}

	return nil
}

func writeWS(conn *websocket.Conn, command string, content interface{}) error {
	body, err := json.Marshal(content)
	if err != nil {
		return err
	}

	message, err := json.Marshal(Message{Command: command, Content: body})
	if err != nil {
		return err
	}

	return conn.WriteMessage(websocket.TextMessage, message)
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}

	return fallback
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}
