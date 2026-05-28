package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleJobStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := s.deps.JobManager.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	hist, ch, cancel := j.Subscribe(256)
	defer cancel()

	// Replay history first.
	for _, line := range hist {
		writeSSE(w, "log", line)
	}
	flusher.Flush()

	// Tick to keep the connection open through reverse proxies.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				// Job done: send final status event and close.
				writeSSE(w, "state", string(j.State))
				flusher.Flush()
				return
			}
			writeSSE(w, "log", line)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-j.Done():
			writeSSE(w, "state", string(j.State))
			flusher.Flush()
			return
		}
	}
}

func writeSSE(w http.ResponseWriter, event, data string) {
	// Each line of data must be prefixed with "data: " for SSE.
	lines := strings.Split(data, "\n")
	_, _ = fmt.Fprintf(w, "event: %s\n", event)
	for _, l := range lines {
		_, _ = fmt.Fprintf(w, "data: %s\n", l)
	}
	_, _ = fmt.Fprint(w, "\n")
}
