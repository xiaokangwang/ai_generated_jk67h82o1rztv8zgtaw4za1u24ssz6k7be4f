package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

	"github.com/redis/go-redis/v9"
)

// Replace these with your actual Client ID and Secret
var (
	clientID         = os.Getenv("GITHUB_CLIENT_ID")
	clientSecret     = os.Getenv("GITHUB_CLIENT_SECRET")
	jwtSecret        = []byte(os.Getenv("JWT_SECRET"))
	redisClient      *redis.Client
	ctx              = context.Background()
	v2rayPath        string
	accessPassphrase string
)

func enableCors(w *http.ResponseWriter) {
	(*w).Header().Set("Access-Control-Allow-Origin", "*")
	(*w).Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
	(*w).Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func helloHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method == "OPTIONS" {
		return
	}

	response := Response{Message: "Hello from Go Backend!"}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func main() {
	v2rayPath = os.Getenv("V2RAY_PATH")
	if v2rayPath == "" {
		v2rayPath = "./v2ray"
	}

	accessPassphrase = os.Getenv("V2RAY_ACCESS_PASSPHRASE")
	if accessPassphrase == "" {
		accessPassphrase = "123"
	}

	// Serve frontend files
	fs := http.FileServer(http.Dir("../frontend"))
	http.Handle("/", fs)

	http.HandleFunc("/api/hello", helloHandler)
	http.HandleFunc("/api/me", meHandler)
	http.HandleFunc("/auth/github/login", githubLoginHandler)
	http.HandleFunc("/auth/github/callback", githubCallbackHandler)
	http.HandleFunc("/api/state", stateHandler)
	http.HandleFunc("/api/allocate-slot", allocateSlotHandler)
	http.HandleFunc("/api/generate-token", generateTokenHandler)

	// Initialize Redis
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	redisClient = redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	// Start server
	port := os.Getenv("PORT")
	if port == "" {
		port = ":8080"
	}
	if port[0] != ':' {
		port = ":" + port
	}
	http.ListenAndServe(port, nil)
}
