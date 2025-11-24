package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

var execCommand = exec.Command

func stateHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
		return
	}

	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtSecret, nil
	})

	if err != nil || !token.Valid {
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		http.Error(w, "Invalid token claims", http.StatusUnauthorized)
		return
	}
	username := claims["username"].(string)
	key := fmt.Sprintf("user:%s:state", username)

	if r.Method == "GET" {
		val, err := redisClient.Get(ctx, key).Result()
		if err == redis.Nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("{}")) // Return empty JSON if no state found
			return
		} else if err != nil {
			http.Error(w, "Failed to get state", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(val))
	} else {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func getServerState(ctx context.Context) (*ServerState, error) {
	key := "server:global:state"
	val, err := redisClient.Get(ctx, key).Result()
	if err == redis.Nil {
		return &ServerState{}, nil
	} else if err != nil {
		return nil, err
	}

	var state ServerState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func incrementUsedInverseModeServerSlot(ctx context.Context) (uint64, error) {
	key := "server:global:state"
	var newSlot uint64
	maxRetries := 100

	for i := 0; i < maxRetries; i++ {
		err := redisClient.Watch(ctx, func(tx *redis.Tx) error {
			val, err := tx.Get(ctx, key).Result()
			if err != nil && err != redis.Nil {
				return err
			}

			var state ServerState
			if err != redis.Nil {
				if json.Unmarshal([]byte(val), &state) != nil {
					return err
				}
			}

			state.UsedInverseModeServerSlot++
			newSlot = state.UsedInverseModeServerSlot

			data, err := json.Marshal(state)
			if err != nil {
				return err
			}

			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, data, 0)
				return nil
			})
			return err
		}, key)

		if err == nil {
			return newSlot, nil
		}
		if err == redis.TxFailedErr {
			// Optimistic lock failed, retry
			continue
		}
		return 0, err
	}

	return 0, fmt.Errorf("failed to increment after %d retries", maxRetries)
}

type UserLimits struct {
	MaxForwarderServerSlots uint64 `json:"max_slots"`
}

func getUserLimits(ctx context.Context, username string) (*UserLimits, error) {
	isUserCoreStargazer, err := isCoreStargazer(username)
	if err != nil {
		return nil, err
	}
	if isUserCoreStargazer {
		return &UserLimits{
			MaxForwarderServerSlots: 5,
		}, nil
	}
	return &UserLimits{
		MaxForwarderServerSlots: 1,
	}, nil
}

func allocateSlotHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
		return
	}
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		http.Error(w, "Invalid token claims", http.StatusUnauthorized)
		return
	}
	username := claims["username"].(string)
	userKey := fmt.Sprintf("user:%s:state", username)

	// 1. Get User State
	var userState UserState
	val, err := redisClient.Get(ctx, userKey).Result()
	if err != nil && err != redis.Nil {
		http.Error(w, "Failed to get user state", http.StatusInternalServerError)
		return
	}
	if err == nil {
		json.Unmarshal([]byte(val), &userState)
	}

	// 2. Check User Limits
	userLimits, err := getUserLimits(ctx, username)
	if err != nil {
		http.Error(w, "Failed to get user limits", http.StatusInternalServerError)
		return
	}

	if uint64(len(userState.AssociatedForwarderServerSlots)) >= userLimits.MaxForwarderServerSlots {
		http.Error(w, "Slot allocation limit reached", http.StatusForbidden)
		return
	}

	// 3. Allocate Slot
	newSlot, err := incrementUsedInverseModeServerSlot(ctx)
	if err != nil {
		http.Error(w, "Failed to allocate slot", http.StatusInternalServerError)
		return
	}

	// 4. Update User State
	userState.AssociatedForwarderServerSlots = append(userState.AssociatedForwarderServerSlots, newSlot)
	userState.UsedInverseModeServerSlot++

	data, err := json.Marshal(userState)
	if err != nil {
		http.Error(w, "Failed to marshal user state", http.StatusInternalServerError)
		return
	}

	err = redisClient.Set(ctx, userKey, data, 0).Err()
	if err != nil {
		http.Error(w, "Failed to save user state", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]uint64{"allocated_slot": newSlot})
}

func generateTokenHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
		return
	}
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		http.Error(w, "Invalid token claims", http.StatusUnauthorized)
		return
	}
	username := claims["username"].(string)

	var req GenerateTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// 1. Get User State
	userKey := fmt.Sprintf("user:%s:state", username)
	var userState UserState
	val, err := redisClient.Get(ctx, userKey).Result()
	if err != nil {
		http.Error(w, "Failed to get user state", http.StatusInternalServerError)
		return
	}
	if err := json.Unmarshal([]byte(val), &userState); err != nil {
		http.Error(w, "Failed to parse user state", http.StatusInternalServerError)
		return
	}

	// 2. Verify Slot Ownership
	found := false
	for _, slot := range userState.AssociatedForwarderServerSlots {
		if slot == req.SlotID {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "Slot not owned by user", http.StatusForbidden)
		return
	}

	// 3. Execute v2ray binary
	cmd := execCommand(v2rayPath, "engineering", "request-rtt-reverser-gen-token", "-access-passphrase", accessPassphrase, "-u", fmt.Sprintf("%d", req.SlotID))
	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to generate token: %v", err), http.StatusInternalServerError)
		return
	}

	// 4. Parse Output
	lines := strings.Split(string(output), "\n")
	var private, public string
	for _, line := range lines {
		if strings.HasPrefix(line, "private: ") {
			private = strings.TrimPrefix(line, "private: ")
		}
		if strings.HasPrefix(line, "public: ") {
			public = strings.TrimPrefix(line, "public: ")
		}
	}

	if private == "" || public == "" {
		http.Error(w, "Failed to parse token output", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(GenerateTokenResponse{
		Private: strings.TrimSpace(private),
		Public:  strings.TrimSpace(public),
	})
}
