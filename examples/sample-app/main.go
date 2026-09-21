// Command sample-app is a tiny HTTP service used as the demo target for
// harmonia. It deliberately contains one real vulnerability (command
// injection) and one mitigated one (validated SQL interpolation) so a
// triage run has something to vote on. It is not intended for production.
package main

import (
	"log"
	"net/http"
	"os/exec"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", pingHandler)
	mux.HandleFunc("/users", usersHandler)

	log.Println("sample-app listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// pingHandler pings the host given in ?host= and returns the raw output.
func pingHandler(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" {
		http.Error(w, "missing ?host=", http.StatusBadRequest)
		return
	}

	out, err := exec.Command("sh", "-c", "ping -c 1 "+host).Output()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(out)
}
