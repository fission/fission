package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

var (
	malformedToken = errors.New("Unauthorized: malformed token")
	expiredToken   = errors.New("Unauthorized: token is either expired or not active yet")
	invalidCreds   = errors.New("Unauthorized: invalid username or password")
)

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := strings.Split(r.Header.Get("Authorization"), "Bearer ")
		if len(authHeader) != 2 || len(authHeader[1]) == 0 {
			// malformed token
			http.Error(w, malformedToken.Error(), http.StatusUnauthorized)
			return
		}

		jwtToken := authHeader[1]
		token, err := jwt.Parse(jwtToken, func(token *jwt.Token) (interface{}, error) {
			return []byte(os.Getenv("JWT_SIGNING_KEY")), nil
		})

		if token != nil && token.Valid {
			// valid token
			next.ServeHTTP(w, r)
			return
		}

		if ve, ok := err.(*jwt.ValidationError); ok {
			if ve.Errors&jwt.ValidationErrorMalformed != 0 {
				// malformed token
				err = malformedToken
			} else if ve.Errors&(jwt.ValidationErrorExpired|jwt.ValidationErrorNotValidYet) != 0 {
				// token is either expired or not active yet
				err = expiredToken
			} else {
				err = fmt.Errorf("Unauthorized: %w", err)
			}
		}

		if err == nil {
			err = errors.New("Unauthorized: invalid token")
		}

		http.Error(w, err.Error(), http.StatusUnauthorized)
	})
}

type AuthConf struct {
	username      string
	password      string
	jwtSigningKey string
}

func parseAuthConf(auth *AuthConf) error {
	username, ok := os.LookupEnv("AUTH_USERNAME")
	if !ok || len(username) == 0 {
		return fmt.Errorf("Username not configured  or invalid")
	}

	password, ok := os.LookupEnv("AUTH_PASSWORD")
	if !ok || len(password) == 0 {
		return fmt.Errorf("Password not configured or invalid")
	}

	signingKey, ok := os.LookupEnv("JWT_SIGNING_KEY")
	if !ok || len(signingKey) == 0 {
		return fmt.Errorf("Signing key not configured or invalid")
	}

	auth.username = username
	auth.password = password
	auth.jwtSigningKey = signingKey
	return nil
}

func authLoginHandler() func(w http.ResponseWriter, r *http.Request) {
	var (
		err       error
		validConf bool
	)

	auth := &AuthConf{}
	if err = parseAuthConf(auth); err != nil {
		validConf = true
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if !validConf {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var t fv1.AuthLogin

		err = json.Unmarshal(body, &t)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		rat := &fv1.RouterAuthToken{}

		if t.Username != auth.username || t.Password != auth.password {
			http.Error(w, invalidCreds.Error(), http.StatusUnauthorized)
			return
		}

		claims := &jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(jwt.TimeFunc().Add(featureConfig.AuthConfig.JWTExpiryTime * time.Second)),
			Issuer:    featureConfig.AuthConfig.JWTIssuer,
			NotBefore: jwt.NewNumericDate(jwt.TimeFunc()),
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		ss, err := token.SignedString([]byte(auth.jwtSigningKey))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rat.AccessToken = ss
		rat.TokenType = "Bearer"

		resp, err := json.Marshal(rat)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusCreated)
		fmt.Println(string(resp))
	}

}
