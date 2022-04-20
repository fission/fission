package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"testing"

	"github.com/gorilla/mux"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/utils/httpserver"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func setup(tb testing.TB) func(tb testing.TB) {

	os.Setenv("AUTH_USERNAME", "Foo")
	os.Setenv("AUTH_PASSWORD", "Bar")
	os.Setenv("JWT_SIGNING_KEY", "test")
	return func(tb testing.TB) {
		os.Unsetenv("AUTH_USERNAME")
		os.Unsetenv("AUTH_PASSWORD")
		os.Unsetenv("JWT_SIGNING_KEY")
	}
}

func GetRouterWithAuth() *mux.Router {
	testHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := io.WriteString(w, "OK")
		if err != nil {
			fmt.Println(fmt.Errorf("Error in writing string: %s", err))
		}
	}

	featureConfig := &config.FeatureConfig{}
	featureConfig.AuthConfig.JWTIssuer = "fission"
	featureConfig.AuthConfig.JWTExpiryTime = 120
	featureConfig.AuthConfig.AuthUriPath = "/auth/login"
	muxRouter := mux.NewRouter()
	muxRouter.Use(authMiddleware)
	muxRouter.HandleFunc("/auth/login", authLoginHandler(featureConfig)).Methods("POST")
	// We should be able to access health without login
	muxRouter.HandleFunc("/router-healthz", routerHealthHandler).Methods("GET")
	muxRouter.HandleFunc("/test", testHandler).Methods("GET")
	return muxRouter
}

func TestRouterAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	teardown := setup(t)
	defer teardown(t)
	logger := loggerfactory.GetLogger()
	testmux := GetRouterWithAuth()

	go httpserver.StartServer(ctx, logger, "test", "8990", testmux)

	postBody, _ := json.Marshal(map[string]string{
		"username": "Foo",
		"password": "Bar",
	})
	responseBody := bytes.NewBuffer(postBody)

	tests := []struct {
		URL        string
		StatusCode int
		Body       string
		AuthReq    bool
	}{
		{
			URL:        "http://localhost:8990/router-healthz",
			StatusCode: http.StatusOK,
			Body:       "",
			AuthReq:    false,
		},
		{
			URL:        "http://localhost:8990/router-healthz",
			StatusCode: http.StatusOK,
			Body:       "",
			AuthReq:    true,
		},
		{
			URL:        "http://localhost:8990/test",
			StatusCode: http.StatusOK,
			Body:       "OK",
			AuthReq:    true,
		},
		{
			URL:        "http://localhost:8990/test",
			StatusCode: http.StatusUnauthorized,
			Body:       "Unauthorized: malformed token\n",
			AuthReq:    false,
		},
	}

	loginResp, err := http.Post("http://localhost:8990/auth/login", "application/json", responseBody)
	if err != nil {
		t.Error(err)
	}
	defer loginResp.Body.Close()

	body, err := io.ReadAll(loginResp.Body)
	if err != nil {
		t.Error(err, "error creating token")
	}

	var rat fv1.RouterAuthToken
	if loginResp.StatusCode == http.StatusCreated {
		err = json.Unmarshal(body, &rat)
		if err != nil {
			t.Error(err)
		}
	}

	client := &http.Client{}

	for _, test := range tests {
		req, err := http.NewRequest("GET", test.URL, nil)
		if err != nil {
			t.Errorf("failed to make get request %v: %v", test.URL, err)
		}
		if test.AuthReq == true {
			req.Header.Add("Authorization", fmt.Sprintf("Bearer %v", rat.AccessToken))
		}

		resp, err := client.Do(req)
		if err != nil {
			t.Error(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != test.StatusCode {
			t.Errorf("expected status code %v, got %v", test.StatusCode, resp.StatusCode)
		}
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			t.Errorf("failed to read response body: %v", err)
		}
		if string(body) != test.Body {
			t.Errorf("expected body \"%v\", got \"%v\"", test.Body, string(body))
		}
	}
}
