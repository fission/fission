package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	DEFAULT_CODE_PATH          = "/userfunc/user"
	DEFAULT_INTERNAL_CODE_PATH = "/bin/userfunc"
)

var specialized bool

type BinaryServer struct {
	fetchedCodePath  string
	internalCodePath string
}

func (bs *BinaryServer) SpecializeHandler(w http.ResponseWriter, r *http.Request) {
	if specialized {
		w.WriteHeader(400)
		w.Write([]byte("Not a generic container"))
		return
	}

	_, err := os.Stat(bs.fetchedCodePath)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(bs.fetchedCodePath + ": not found"))
			return
		} else {
			panic(err)
		}
	}

	// Future: Check if executable is correct architecture/executable.

	// Copy the executable to ensure that file is executable and immutable.
	userFunc, err := ioutil.ReadFile(bs.fetchedCodePath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to read executable."))
		return
	}
	err = ioutil.WriteFile(bs.internalCodePath, userFunc, 0555)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to write executable to target location."))
		return
	}

	fmt.Println("Specializing ...")
	specialized = true
	fmt.Println("Done")
}

func (bs *BinaryServer) InvocationHandler(w http.ResponseWriter, r *http.Request) {
	if !specialized {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Generic container: no requests supported"))
		return
	}

	// CGI-like passing of environment variables
	execEnv := NewEnv(nil)
	execEnv.SetEnv(&EnvVar{"REQUEST_METHOD", r.Method})
	execEnv.SetEnv(&EnvVar{"REQUEST_URI", r.RequestURI})
	execEnv.SetEnv(&EnvVar{"CONTENT_LENGTH", fmt.Sprintf("%d", r.ContentLength)})

	for header, val := range r.Header {
		execEnv.SetEnv(&EnvVar{fmt.Sprintf("HTTP_%s", strings.ToUpper(header)), val[0]})
	}

	// Future: could be improved by keeping subprocess open while environment is specialized
	cmd := exec.Command(bs.internalCodePath)
	cmd.Env = execEnv.ToStringEnv()

	if r.ContentLength != 0 {
		fmt.Println(r.ContentLength)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(fmt.Sprintf("Failed to get STDIN pipe: %s", err)))
			panic(err)
		}
		_, err = io.Copy(stdin, r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(fmt.Sprintf("Failed to pipe input: %s", err)))
		}
		stdin.Close()
	}

	out, err := cmd.Output()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("Function error: %s", err)))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(out)
}

func main() {
	codePath := flag.String("c", DEFAULT_CODE_PATH, "Path to expected fetched executable.")
	internalCodePath := flag.String("i", DEFAULT_INTERNAL_CODE_PATH, "Path to specialized executable.")
	flag.Parse()
	absInternalCodePath, err := filepath.Abs(*internalCodePath)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Using fetched code path: %s\n", *codePath)
	fmt.Printf("Using internal code path: %s\n", absInternalCodePath)

	server := &BinaryServer{*codePath, absInternalCodePath}
	http.HandleFunc("/", server.InvocationHandler)
	http.HandleFunc("/specialize", server.SpecializeHandler)

	fmt.Println("Listening on 8888 ...")
	err = http.ListenAndServe(":8888", nil)
	if err != nil {
		panic(err)
	}
}
