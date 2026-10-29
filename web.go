package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

//go:embed index.html
var indexHTML []byte

type stateJSON struct {
	Config    config   `json:"config"`
	Screen    []string `json:"screen"` // 256 цветов #rrggbb, построчно сверху вниз
	Connected bool     `json:"connected"`
	Error     string   `json:"error,omitempty"`
	Weather   *struct {
		Temp int    `json:"temp"`
		Icon string `json:"icon"`
	} `json:"weather,omitempty"`
	Updated time.Time `json:"updated"`
}

func (s *state) snapshot() stateJSON {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := stateJSON{Config: s.cfg, Connected: s.connected, Error: s.err, Updated: s.updated}
	for _, c := range s.px {
		j.Screen = append(j.Screen, fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2]))
	}
	if s.w.ok {
		j.Weather = &struct {
			Temp int    `json:"temp"`
			Icon string `json:"icon"`
		}{s.w.temp, s.w.icon}
	}
	return j
}

func (s *state) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(s.snapshot())
	})
	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		var c config
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			http.Error(w, "неверный JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.setConfig(c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(s.snapshot())
	})
	return mux
}
