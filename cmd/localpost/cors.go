package main

import "net/http"

// withCORS serves the trusted-localhost policy before application handlers. It
// passes the original writer through so upload deadlines remain enforceable.
func withCORS(next http.Handler, methods string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", methods)
		if request.Method == http.MethodOptions {
			if request.ContentLength != 0 {
				// Do not let net/http drain an incomplete preflight body forever.
				abortMessageBody(w, http.NewResponseController(w))
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, request)
	})
}
