package main

import (
	"net/http"

	"github.com/gozsunday/gater/internal/jsonutil"
)

func (a *application) checkHealth(w http.ResponseWriter, r *http.Request) {
	type data struct {
		Status string `json:"status"`
	}
	jsonutil.Write(w, http.StatusOK, data{Status: "OK"})
}
