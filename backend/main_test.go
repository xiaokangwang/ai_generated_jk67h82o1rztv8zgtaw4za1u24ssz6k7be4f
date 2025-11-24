package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

func setup() {
	// Set environment variables if needed
	if len(jwtSecret) == 0 {
		jwtSecret = []byte("testsecret")
	}

	mr, err := miniredis.Run()
	if err != nil {
		panic(err)
	}

	redisClient = redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
}

func TestStateHandler(t *testing.T) {
	setup()

	// 1. Generate Token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"username": "testuser",
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	tokenString, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("Failed to sign token: %v", err)
	}

	// 2. Test POST
	state := map[string]string{"theme": "dark", "last_page": "dashboard"}
	body, _ := json.Marshal(state)
	req, _ := http.NewRequest("POST", "/api/state", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rr := httptest.NewRecorder()

	stateHandler(rr, req)

	if status := rr.Code; status != http.StatusMethodNotAllowed {
		t.Errorf("POST handler returned wrong status code: got %v want %v", status, http.StatusMethodNotAllowed)
	}

	// 3. Test GET (Pre-populate Redis for test)
	key := "user:testuser:state"
	redisClient.Set(context.Background(), key, `{"theme":"dark"}`, 0)
	req, _ = http.NewRequest("GET", "/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rr = httptest.NewRecorder()

	stateHandler(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("GET handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var respState map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &respState); err != nil {
		t.Errorf("Failed to parse response JSON: %v", err)
	}

	if respState["theme"] != "dark" {
		t.Errorf("Expected theme 'dark', got %v", respState["theme"])
	}
}

func TestGetServerStateHelper(t *testing.T) {
	setup()

	// 1. Test Empty State
	state, err := getServerState(context.Background())
	if err != nil {
		t.Fatalf("Failed to get server state: %v", err)
	}
	if state.UsedInverseModeServerSlot != 0 {
		t.Errorf("Expected 0, got %v", state.UsedInverseModeServerSlot)
	}

	// 2. Test Populated State
	expectedState := ServerState{UsedInverseModeServerSlot: 999}
	data, _ := json.Marshal(expectedState)
	redisClient.Set(context.Background(), "server:global:state", data, 0)

	state, err = getServerState(context.Background())
	if err != nil {
		t.Fatalf("Failed to get server state: %v", err)
	}
	if state.UsedInverseModeServerSlot != 999 {
		t.Errorf("Expected 999, got %v", state.UsedInverseModeServerSlot)
	}
}

func TestIncrementHelper(t *testing.T) {
	setup()

	// 1. Increment from 0
	val, err := incrementUsedInverseModeServerSlot(context.Background())
	if err != nil {
		t.Fatalf("Failed to increment: %v", err)
	}
	if val != 1 {
		t.Errorf("Expected 1, got %v", val)
	}

	// 2. Increment again
	val, err = incrementUsedInverseModeServerSlot(context.Background())
	if err != nil {
		t.Fatalf("Failed to increment: %v", err)
	}
	if val != 2 {
		t.Errorf("Expected 2, got %v", val)
	}

	// 3. Verify state persistence
	state, _ := getServerState(context.Background())
	if state.UsedInverseModeServerSlot != 2 {
		t.Errorf("Expected persisted value 2, got %v", state.UsedInverseModeServerSlot)
	}
}

func TestIncrementHelperConcurrency(t *testing.T) {
	setup()

	// Reset state
	redisClient.Del(context.Background(), "server:global:state")

	concurrency := 50
	done := make(chan bool)

	for i := 0; i < concurrency; i++ {
		go func() {
			_, err := incrementUsedInverseModeServerSlot(context.Background())
			if err != nil {
				t.Errorf("Error incrementing: %v", err)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < concurrency; i++ {
		<-done
	}

	state, err := getServerState(context.Background())
	if err != nil {
		t.Fatalf("Failed to get state: %v", err)
	}

	if int(state.UsedInverseModeServerSlot) != concurrency {
		t.Errorf("Race condition detected! Expected %d, got %d", concurrency, state.UsedInverseModeServerSlot)
	}
}

func TestAllocateSlotHandler(t *testing.T) {
	setup()

	// 1. Generate Token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"username": "slotuser",
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	tokenString, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("Failed to sign token: %v", err)
	}

	// 2. Allocate Slot
	req, _ := http.NewRequest("POST", "/api/allocate-slot", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rr := httptest.NewRecorder()

	allocateSlotHandler(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var resp map[string]uint64
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp["allocated_slot"] != 1 {
		t.Errorf("Expected allocated_slot 1, got %v", resp["allocated_slot"])
	}

	// 3. Verify User State
	userKey := "user:slotuser:state"
	val, err := redisClient.Get(context.Background(), userKey).Result()
	if err != nil {
		t.Fatalf("Failed to get user state: %v", err)
	}

	var userState UserState
	if err := json.Unmarshal([]byte(val), &userState); err != nil {
		t.Fatalf("Failed to unmarshal user state: %v", err)
	}

	if len(userState.AssociatedForwarderServerSlots) != 1 || userState.AssociatedForwarderServerSlots[0] != 1 {
		t.Errorf("Expected user state to have slot 1, got %v", userState.AssociatedForwarderServerSlots)
	}

	if userState.UsedInverseModeServerSlot != 1 {
		t.Errorf("Expected user UsedInverseModeServerSlot 1, got %v", userState.UsedInverseModeServerSlot)
	}

	// 4. Verify Global State
	globalState, _ := getServerState(context.Background())
	if globalState.UsedInverseModeServerSlot != 1 {
		t.Errorf("Expected global state 1, got %v", globalState.UsedInverseModeServerSlot)
	}
}

// Mock exec.Command
func fakeExecCommand(command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcess", "--", command}
	cs = append(cs, args...)
	cmd := exec.Command(os.Args[0], cs...)
	cmd.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
	return cmd
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	// Print mock output
	fmt.Println("private: mock_private_key")
	fmt.Println("public: mock_public_key")
	os.Exit(0)
}

func TestGenerateTokenHandler(t *testing.T) {
	setup()

	// Swap execCommand
	oldExecCommand := execCommand
	execCommand = fakeExecCommand
	defer func() { execCommand = oldExecCommand }()

	// 1. Generate Token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"username": "tokenuser",
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	tokenString, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("Failed to sign token: %v", err)
	}

	// 2. Setup User State with Slot 1
	userKey := "user:tokenuser:state"
	userState := UserState{
		AssociatedForwarderServerSlots: []uint64{1},
	}
	data, _ := json.Marshal(userState)
	redisClient.Set(context.Background(), userKey, data, 0)

	// 3. Call Generate Token
	reqBody := map[string]uint64{"slot_id": 1}
	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/api/generate-token", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+tokenString)
	rr := httptest.NewRecorder()

	generateTokenHandler(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var resp GenerateTokenResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp.Private != "mock_private_key" {
		t.Errorf("Expected private key 'mock_private_key', got '%v'", resp.Private)
	}
	if resp.Public != "mock_public_key" {
		t.Errorf("Expected public key 'mock_public_key', got '%v'", resp.Public)
	}
}
