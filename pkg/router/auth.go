package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		authHeader := strings.Split(r.Header.Get("Authorization"), "Bearer ")
		if len(authHeader) != 2 || len(authHeader[1]) == 0 {
			// malformed token
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write(createErrorResponse("Unauthorized: malformed Token", http.StatusUnauthorized))
		} else {
			jwtToken := authHeader[1]
			token, err := jwt.Parse(jwtToken, func(token *jwt.Token) (interface{}, error) {
				return []byte(os.Getenv("JWT_SIGNING_KEY")), nil
			})

			if token != nil && token.Valid {
				// valid token
				next.ServeHTTP(w, r)
			} else if ve, ok := err.(*jwt.ValidationError); ok {
				w.WriteHeader(http.StatusUnauthorized)
				if ve.Errors&jwt.ValidationErrorMalformed != 0 {
					// malformed token
					_, _ = w.Write(createErrorResponse("Unauthorized: malformed Token", http.StatusUnauthorized))
				} else if ve.Errors&(jwt.ValidationErrorExpired|jwt.ValidationErrorNotValidYet) != 0 {
					// token is either expired or not active yet
					_, _ = w.Write(createErrorResponse("Unauthorized: token is either expired or not active yet", http.StatusUnauthorized))
				} else {
					_, _ = w.Write(createErrorResponse(fmt.Sprintf("Unauthorized: %v", err.Error()), http.StatusUnauthorized))
				}
			} else {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write(createErrorResponse("Unauthorized", http.StatusUnauthorized))
			}

		}

	})
}

func authLoginHandler(w http.ResponseWriter, r *http.Request) {

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(createErrorResponse("Error while reading request body", http.StatusBadRequest))
		return
	}

	var t fv1.AuthLogin

	err = json.Unmarshal(body, &t)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(createErrorResponse("Error while reading request body", http.StatusBadRequest))
		return
	}

	username, ok := os.LookupEnv("AUTH_USERNAME")
	if !ok || len(username) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(createErrorResponse("Username not found or invalid", http.StatusBadRequest))
		return
	}

	password, ok := os.LookupEnv("AUTH_PASSWORD")
	if !ok || len(password) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(createErrorResponse("Password not found or invalid", http.StatusBadRequest))
		return
	}

	signingKey, ok := os.LookupEnv("JWT_SIGNING_KEY")
	if !ok || len(signingKey) == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(createErrorResponse("Internal server error occurred", http.StatusInternalServerError))
		return
	}

	rat := &fv1.RouterAuthToken{}

	if t.Username == username && t.Password == password {

		claims := &jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(jwt.TimeFunc().Add(featureConfig.AuthConfig.JWTExpiryTime * time.Second)),
			Issuer:    featureConfig.AuthConfig.JWTIssuer,
			NotBefore: jwt.NewNumericDate(jwt.TimeFunc()),
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		ss, err := token.SignedString([]byte(signingKey))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(createErrorResponse("Internal server error occurred", http.StatusInternalServerError))
			return
		}
		rat.AccessToken = ss
		rat.TokenType = "Bearer"

	} else {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(createErrorResponse("Unauthorized: invalid username and/or password", http.StatusUnauthorized))
		return
	}

	resp, err := json.Marshal(rat)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(createErrorResponse("Internal server error occurred", http.StatusInternalServerError))
		return
	}

	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(resp)

}
