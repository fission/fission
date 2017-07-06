package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"io/ioutil"
)

const (
	CODE_PATH          = "/userfunc/user"
	INTERNAL_CODE_PATH = "/bin/userfunc"
)

var specialized bool

func specializeHandler(w http.ResponseWriter, r *http.Request) {
	if specialized {
		w.WriteHeader(400)
		w.Write([]byte("Not a generic container"))
		return
	}

	_, err := os.Stat(CODE_PATH)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(CODE_PATH + ": not found"))
			return
		} else {
			panic(err)
		}
	}

	// TODO Check if executable is correct architecture/executable.

	// Copy the executable to ensure that file is executable and immutable.
	userFunc, err := ioutil.ReadFile(CODE_PATH);
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to read executable."))
		return
	}
	err = ioutil.WriteFile(INTERNAL_CODE_PATH, userFunc, 0554)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to write executable to target location."))
		return
	}

	fmt.Println("Specializing ...")
	specialized = true
	fmt.Println("Done")
}

func invocationHandler(w http.ResponseWriter, r *http.Request) {
	if !specialized {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Generic container: no requests supported"))
		return
	}

	cmd := exec.Command(INTERNAL_CODE_PATH)
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
	http.HandleFunc("/", invocationHandler)
	http.HandleFunc("/specialize", specializeHandler)

	fmt.Println("Listening on 8888 ...")
	err := http.ListenAndServe(":8888", nil)
	if err != nil {
		panic(err)
	}
}
