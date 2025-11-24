package main

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

//go:embed bundled/core_stargazer
var coreStargazerFile string

func generateToken(username string) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"username": username,
		"exp":      time.Now().Add(time.Hour * 24).Unix(),
	})
	return token.SignedString(jwtSecret)
}

func meHandler(w http.ResponseWriter, r *http.Request) {
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

	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		username := claims["username"].(string)
		isCoreStargazer, err := isCoreStargazer(username)
		if err != nil {
			http.Error(w, "Failed to check core stargazer status", http.StatusInternalServerError)
			return
		}
		response := UserResponse{
			Username:        username,
			IsCoreStargazer: isCoreStargazer,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	} else {
		http.Error(w, "Invalid token claims", http.StatusUnauthorized)
	}
}

func githubLoginHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)

	// Generate random nonce for this OAuth session
	nonceBytes := make([]byte, 32)
	_, err := rand.Read(nonceBytes)
	if err != nil {
		http.Error(w, "Failed to generate nonce", http.StatusInternalServerError)
		return
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)

	// Deterministically derive code verifier from nonce + server secret
	// This ensures the verifier is never sent to the client
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte("pkce-verifier:" + nonce))
	verifierHash := mac.Sum(nil)
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierHash)

	// Generate code challenge (SHA256 of verifier)
	hash := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(hash[:])

	// Create state with nonce (format: "nonce.hmac")
	// Sign the nonce to prevent tampering
	stateMac := hmac.New(sha256.New, jwtSecret)
	stateMac.Write([]byte("oauth-state:" + nonce))
	signature := base64.RawURLEncoding.EncodeToString(stateMac.Sum(nil))
	state := nonce + "." + signature

	// Build OAuth URL with PKCE parameters
	redirectURL := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&scope=read:user&state=%s&code_challenge=%s&code_challenge_method=S256",
		clientID, base64.RawURLEncoding.EncodeToString([]byte(state)), codeChallenge,
	)
	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

func githubCallbackHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")

	// Decode state parameter
	stateBytes, err := base64.RawURLEncoding.DecodeString(stateParam)
	if err != nil {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}
	state := string(stateBytes)

	// Extract nonce and signature (format: "nonce.hmac")
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		http.Error(w, "Invalid state format", http.StatusBadRequest)
		return
	}
	nonce := parts[0]
	providedSignature := parts[1]

	// Verify HMAC signature
	stateMac := hmac.New(sha256.New, jwtSecret)
	stateMac.Write([]byte("oauth-state:" + nonce))
	expectedSignature := base64.RawURLEncoding.EncodeToString(stateMac.Sum(nil))

	if !hmac.Equal([]byte(providedSignature), []byte(expectedSignature)) {
		http.Error(w, "Invalid state signature", http.StatusBadRequest)
		return
	}

	// Deterministically regenerate the same code verifier from nonce
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte("pkce-verifier:" + nonce))
	verifierHash := mac.Sum(nil)
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierHash)

	requestBodyMap := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"code":          code,
		"code_verifier": codeVerifier,
	}
	requestJSON, _ := json.Marshal(requestBodyMap)

	req, err := http.NewRequest("POST", "https://github.com/login/oauth/access_token", bytes.NewBuffer(requestJSON))
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "Failed to exchange token", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	var tokenResp GitHubAccessTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		http.Error(w, "Failed to parse token response", http.StatusInternalServerError)
		return
	}

	userReq, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		http.Error(w, "Failed to create user request", http.StatusInternalServerError)
		return
	}
	userReq.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)

	userResp, err := http.DefaultClient.Do(userReq)
	if err != nil {
		http.Error(w, "Failed to fetch user", http.StatusInternalServerError)
		return
	}
	defer userResp.Body.Close()

	var user GitHubUser
	if err := json.NewDecoder(userResp.Body).Decode(&user); err != nil {
		http.Error(w, "Failed to parse user response", http.StatusInternalServerError)
		return
	}

	// Generate JWT
	jwtToken, err := generateToken(user.Login)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	// Redirect back to frontend with token
	http.Redirect(w, r, fmt.Sprintf("/?token=%s", jwtToken), http.StatusTemporaryRedirect)
}

func isCoreStargazer(username string) (bool, error) {
	// Read from embedded file
	scanner := bufio.NewScanner(strings.NewReader(coreStargazerFile))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == username {
			return true, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return false, err
	}

	return false, nil
}
