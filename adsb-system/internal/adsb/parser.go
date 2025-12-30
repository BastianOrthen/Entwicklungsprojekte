package adsb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

// RunDump1090Poll polls a dump1090-like JSON endpoint and sends Aircraft updates on the returned channel.
// The caller should cancel ctx to stop polling and close the channel when done.
func RunDump1090Poll(ctx context.Context, url string) <-chan Aircraft {
	out := make(chan Aircraft)
	if url == "" {
		url = "http://127.0.0.1:8080/data/aircraft.json"
	}

	go func() {
		defer close(out)
		client := &http.Client{Timeout: 5 * time.Second}
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			func() {
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					return
				}
				var doc struct {
					Aircraft []map[string]interface{} `json:"aircraft"`
				}
				if err := json.Unmarshal(body, &doc); err != nil {
					return
				}
				now := time.Now()
				for _, a := range doc.Aircraft {
					icao, _ := a["hex"].(string)
					lat, _ := a["lat"].(float64)
					lon, _ := a["lon"].(float64)
					alt := 0
					if v, ok := a["altitude"]; ok {
						switch t := v.(type) {
						case float64:
							alt = int(t)
						case string:
							if n, err := strconv.Atoi(t); err == nil {
								alt = n
							}
						}
					}
					spd := 0
					if v, ok := a["gs"]; ok {
						switch t := v.(type) {
						case float64:
							spd = int(t)
						case string:
							if n, err := strconv.Atoi(t); err == nil {
								spd = n
							}
						}
					}

					// only emit if we have coordinates and icao
					if icao == "" || lat == 0 && lon == 0 {
						continue
					}
					out <- Aircraft{
						ICAO:      icao,
						Latitude:  lat,
						Longitude: lon,
						Altitude:  alt,
						Speed:     spd,
						Seen:      now,
					}
				}
			}()
		}
	}()

	return out
}
