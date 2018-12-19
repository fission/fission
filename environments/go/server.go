package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"plugin"

	"github.com/pkg/errors"

	"github.com/fission/fission/environments/go/context"
)

const (
	CODE_PATH = "/userfunc/user"
)

type (
	FunctionLoadRequest struct {
		// FilePath is an absolute filesystem path to the
		// function. What exactly is stored here is
		// env-specific. Optional.
		FilePath string `json:"filepath"`

		// FunctionName has an environment-specific meaning;
		// usually, it defines a function within a module
		// containing multiple functions. Optional; default is
		// environment-specific.
		FunctionName string `json:"functionName"`

		// URL to expose this function at. Optional; defaults
		// to "/".
		URL string `json:"url"`
	}
)

var userFunc http.HandlerFunc

func loadPlugin(codePath, entrypoint string) (http.HandlerFunc, error) {

	// if codepath's a directory, load the file inside it
	info, err := os.Stat(codePath)
	if err != nil {
		return nil, errors.Wrap(err, "error checking plugin path")
	}
	if info.IsDir() {
		files, err := ioutil.ReadDir(codePath)
		if err != nil {
			return nil, errors.Wrap(err, "error reading directory")
		}
		if len(files) == 0 {
			return nil, errors.New("No files to load")
		}
		fi := files[0]
		codePath = filepath.Join(codePath, fi.Name())
	}

	log.Printf("loading plugin from %v\n", codePath)
	p, err := plugin.Open(codePath)
	if err != nil {
		return nil, errors.Wrap(err, "error loading plugin")
	}
	sym, err := p.Lookup(entrypoint)
	if err != nil {
		return nil, errors.Wrap(err, "entry point not found")
	}

	switch h := sym.(type) {
	case *http.Handler:
		return (*h).ServeHTTP, nil
	case *http.HandlerFunc:
		return *h, nil
	case func(http.ResponseWriter, *http.Request):
		return h, nil
	case func(context.Context, http.ResponseWriter, *http.Request):
		return func(w http.ResponseWriter, r *http.Request) {
			c := context.New()
			h(c, w, r)
		}, nil
	default:
		panic("Entry point not found: bad type")
	}
}

func specializeHandler(w http.ResponseWriter, r *http.Request) {
	if userFunc != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Not a generic container"))
		return
	}

	_, err := os.Stat(CODE_PATH)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			log.Printf(err.Error())
			w.Write([]byte(CODE_PATH + ": not found"))
			return
		} else {
			err = errors.Wrap(err, "unknown error")
			log.Printf(err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}
	}

	log.Println("Specializing ...")
	userFunc, err = loadPlugin(CODE_PATH, "Handler")
	if err != nil {
		err = errors.Wrap(err, "error specializing function")
		log.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	log.Println("Done")
}

func specializeHandlerV2(w http.ResponseWriter, r *http.Request) {
	if userFunc != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Not a generic container"))
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Println(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var loadreq FunctionLoadRequest
	err = json.Unmarshal(body, &loadreq)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	_, err = os.Stat(loadreq.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			log.Printf(err.Error())
			w.Write([]byte(CODE_PATH + ": not found"))
			return
		} else {
			err = errors.Wrap(err, "unknown error")
			log.Printf(err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}
	}

	log.Println("Specializing ...")
	userFunc, err = loadPlugin(loadreq.FilePath, loadreq.FunctionName)
	if err != nil {
		err = errors.Wrap(err, "error specializing function")
		log.Printf(err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	log.Println("Done")
}

func readinessProbeHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func main() {
	http.HandleFunc("/healthz", readinessProbeHandler)
	http.HandleFunc("/specialize", specializeHandler)
	http.HandleFunc("/v2/specialize", specializeHandlerV2)

	// Generic route -- all http requests go to the user function.
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if userFunc == nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Generic container: no requests supported"))
			return
		}
		userFunc(w, r)
	})

	log.Println("Listening on 8888 ...")
	http.ListenAndServe(":8888", nil)
}
