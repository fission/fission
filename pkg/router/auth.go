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
	"github.com/gorilla/mux"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	config "github.com/fission/fission/pkg/featureconfig"
)

var (
	errMalformedToken = errors.New("unauthorized: malformed token")
	errExpiredToken   = errors.New("unauthorized: token is either expired or not active yet")
	errInvalidCreds   = errors.New("unauthorized: invalid username or password")
)

func checkAuthToken(r *http.Request) error {
	authHeader := strings.Split(r.Header.Get("Authorization"), "Bearer ")
	if len(authHeader) != 2 || len(authHeader[1]) == 0 {
		// malformed token
		return errMalformedToken
	}

	jwtToken := authHeader[1]
	token, err := jwt.Parse(jwtToken, func(token *jwt.Token) (interface{}, error) {
		return []byte(os.Getenv("JWT_SIGNING_KEY")), nil
	})

	if token != nil && token.Valid {
		// valid token
		return nil
	}

	if ve, ok := err.(*jwt.ValidationError); ok {
		if ve.Errors&jwt.ValidationErrorMalformed != 0 {
			// malformed token
			err = errMalformedToken
		} else if ve.Errors&(jwt.ValidationErrorExpired|jwt.ValidationErrorNotValidYet) != 0 {
			// token is either expired or not active yet
			err = errExpiredToken
		} else {
			err = fmt.Errorf("unauthorized: %w", err)
		}
	}

	if err == nil {
		err = errors.New("unauthorized: invalid token")
	}

	return err
}

func authMiddleware(featureConfig *config.FeatureConfig) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != featureConfig.AuthConfig.AuthUriPath && r.URL.Path != "/router-healthz" {
				err := checkAuthToken(r)
				if err != nil {
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

type AuthConf struct {
	username      string
	password      string
	jwtSigningKey string
}

func parseAuthConf(auth *AuthConf) error {
	username, ok := os.LookupEnv("AUTH_USERNAME")
	if !ok || len(username) == 0 {
		return fmt.Errorf("username not configured  or invalid")
	}

	password, ok := os.LookupEnv("AUTH_PASSWORD")
	if !ok || len(password) == 0 {
		return fmt.Errorf("password not configured or invalid")
	}

	signingKey, ok := os.LookupEnv("JWT_SIGNING_KEY")
	if !ok || len(signingKey) == 0 {
		return fmt.Errorf("signing key not configured or invalid")
	}

	auth.username = username
	auth.password = password
	auth.jwtSigningKey = signingKey
	return nil
}

func authLoginHandler(featureConfig *config.FeatureConfig) func(w http.ResponseWriter, r *http.Request) {
	var (
		err       error
		validConf bool
	)

	validConf = true

	auth := &AuthConf{}
	if err = parseAuthConf(auth); err != nil {
		validConf = false
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
			http.Error(w, errInvalidCreds.Error(), http.StatusUnauthorized)
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
		_, _ = w.Write(resp)
	}

}
