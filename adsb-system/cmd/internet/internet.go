package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/basti/adsb-system/internal/adsb"
)

type bbox struct {
	minLat float64
	minLon float64
	maxLat float64
	maxLon float64
	set    bool
}

func (b *bbox) String() string {
	if b == nil || !b.set {
		return ""
	}
	return fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", b.minLat, b.minLon, b.maxLat, b.maxLon)
}

func (b *bbox) Set(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		*b = bbox{}
		return nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return fmt.Errorf("bbox must be 'minLat,minLon,maxLat,maxLon'")
	}
	var vals [4]float64
	for i := range parts {
		_, err := fmt.Sscanf(strings.TrimSpace(parts[i]), "%f", &vals[i])
		if err != nil {
			return fmt.Errorf("bbox parse: %w", err)
		}
	}
	b.minLat, b.minLon, b.maxLat, b.maxLon = vals[0], vals[1], vals[2], vals[3]
	b.set = true
	return nil
}

type openSkyResponse struct {
	Time   int64           `json:"time"`
	States [][]interface{} `json:"states"`
}

func main() {
	var (
		openSkyURL = flag.String("opensky", "https://opensky-network.org/api/states/all", "OpenSky REST endpoint")
		targetURL  = flag.String("target", "http://localhost:8080/ingest", "ADSB server ingest endpoint")
		interval   = flag.Duration("interval", 30*time.Second, "poll interval")
		once       = flag.Bool("once", false, "poll once and exit")
		username   = flag.String("user", "", "OpenSky username (optional)")
		password   = flag.String("pass", "", "OpenSky password (optional)")
		limit      = flag.Int("limit", 250, "max aircraft per poll (0 = unlimited)")
		bboxFlag   bbox
	)
	flag.Var(&bboxFlag, "bbox", "optional bbox filter: minLat,minLon,maxLat,maxLon")
	flag.Parse()

	// Optional env fallback (useful for Docker secrets/.env)
	if *username == "" {
		*username = strings.TrimSpace(os.Getenv("OPENSKY_USER"))
	}
	if *password == "" {
		*password = strings.TrimSpace(os.Getenv("OPENSKY_PASS"))
	}

	fmt.Printf("[INTERNET] Starting OpenSky poller: %s\n", *openSkyURL)
	fmt.Printf("[INTERNET] Ingest target: %s\n", *targetURL)
	fmt.Printf("[INTERNET] Interval: %s\n", interval.String())
	if bboxFlag.set {
		fmt.Printf("[INTERNET] BBox: %s\n", bboxFlag.String())
	}
	if *limit > 0 {
		fmt.Printf("[INTERNET] Limit: %d\n", *limit)
	}

	client := &http.Client{Timeout: 10 * time.Second}

	sleepFor := time.Duration(0)
	for {
		if sleepFor > 0 {
			time.Sleep(sleepFor)
		}

		posted, polled, err := pollOnce(client, *openSkyURL, *targetURL, *username, *password, bboxFlag, *limit)
		if err != nil {
			if rl, ok := err.(*rateLimitError); ok {
				sleepFor = computeBackoff(*interval, sleepFor, rl.retryAfter)
				if rl.retryAfter > 0 {
					fmt.Printf("[INTERNET] rate limited (HTTP %d). Retry-After=%s, backing off=%s\n", rl.status, rl.retryAfter, sleepFor)
				} else {
					fmt.Printf("[INTERNET] rate limited (HTTP %d). Backing off=%s\n", rl.status, sleepFor)
				}
			} else {
				fmt.Printf("[INTERNET] fetch error: %v\n", err)
				sleepFor = *interval
			}
		} else {
			fmt.Printf("[INTERNET] polled=%d posted=%d\n", polled, posted)
			sleepFor = *interval
		}

		if *once {
			return
		}
	}
}

type rateLimitError struct {
	status     int
	body       string
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	if e.retryAfter > 0 {
		return fmt.Sprintf("opensky http %d (rate limited), retry-after=%s: %s", e.status, e.retryAfter, e.body)
	}
	return fmt.Sprintf("opensky http %d (rate limited): %s", e.status, e.body)
}

func computeBackoff(baseInterval, currentBackoff, retryAfter time.Duration) time.Duration {
	// If the server tells us exactly when to retry, respect it.
	// (OpenSky often provides a long wait window when anonymous.)
	if retryAfter > 0 {
		return retryAfter
	}

	maxBackoff := 10 * time.Minute

	backoff := currentBackoff
	if backoff <= 0 {
		backoff = baseInterval
	}
	if backoff < 5*time.Second {
		backoff = 5 * time.Second
	}
	backoff = backoff * 2
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff
}

func pollOnce(client *http.Client, openSkyURL, targetURL, username, password string, bb bbox, limit int) (posted int, polled int, err error) {
	resp, err := fetchOpenSky(client, openSkyURL, username, password, bb)
	if err != nil {
		return 0, 0, err
	}

	polled = len(resp.States)

	count := 0
	posted = 0
	for _, state := range resp.States {
		a, ok := aircraftFromOpenSkyState(state)
		if !ok {
			continue
		}
		count++
		if limit > 0 && count > limit {
			break
		}

		if err := postIngest(client, targetURL, a); err != nil {
			fmt.Printf("[INTERNET] ingest error for %s: %v\n", a.ICAO, err)
			continue
		}
		posted++
	}

	return posted, polled, nil
}

func fetchOpenSky(client *http.Client, openSkyURL, username, password string, bb bbox) (*openSkyResponse, error) {
	url := openSkyURL
	if bb.set {
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		url = fmt.Sprintf("%s%slamin=%f&lomin=%f&lamax=%f&lomax=%f", url, sep, bb.minLat, bb.minLon, bb.maxLat, bb.maxLon)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "adsb-system/1.0")
	if username != "" {
		req.SetBasicAuth(username, password)
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusTooManyRequests {
		return nil, &rateLimitError{status: res.StatusCode, body: strings.TrimSpace(string(body)), retryAfter: parseRetryAfterFromHeaders(res.Header)}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("opensky http %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed openSkyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseRetryAfterFromHeaders(h http.Header) time.Duration {
	// Standard header
	if d := parseRetryAfter(h.Get("Retry-After")); d > 0 {
		return d
	}

	// OpenSky uses a custom header for the remaining block time.
	// Example: X-Rate-Limit-Retry-After-Seconds: 83850
	if secs, err := strconv.Atoi(strings.TrimSpace(h.Get("X-Rate-Limit-Retry-After-Seconds"))); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}

	return 0
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	// Retry-After can be seconds or an HTTP date
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := time.Parse(time.RFC1123, v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	if t, err := time.Parse(time.RFC1123Z, v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

func postIngest(client *http.Client, targetURL string, a adsb.Aircraft) error {
	buf, err := json.Marshal(a)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("ingest http %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func aircraftFromOpenSkyState(state []interface{}) (adsb.Aircraft, bool) {
	// OpenSky state vector indices:
	//  0 icao24 string
	//  1 callsign string
	//  2 origin_country string
	//  3 time_position int
	//  4 last_contact int
	//  5 longitude float
	//  6 latitude float
	//  7 baro_altitude float (meters)
	//  8 on_ground bool
	//  9 velocity float (m/s)
	// 10 true_track float (degrees)
	// 11 vertical_rate float (m/s)
	// 12 sensors []int (ignored)
	// 13 geo_altitude float (meters)
	// 14 squawk string
	// 15 spi bool
	// 16 position_source int

	icao, ok := asString(state, 0)
	if !ok {
		return adsb.Aircraft{}, false
	}
	icao = strings.ToUpper(strings.TrimSpace(icao))
	if icao == "" {
		return adsb.Aircraft{}, false
	}

	lat, okLat := asFloat(state, 6)
	lon, okLon := asFloat(state, 5)
	if !okLat || !okLon {
		return adsb.Aircraft{}, false
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return adsb.Aircraft{}, false
	}

	callsign, _ := asString(state, 1)
	callsign = strings.TrimSpace(callsign)
	originCountry, _ := asString(state, 2)
	originCountry = strings.TrimSpace(originCountry)

	squawk, _ := asString(state, 14)
	squawk = strings.TrimSpace(squawk)

	onGround, _ := asBool(state, 8)

	// Prefer geo altitude if present, else baro altitude
	geoAltMeters, okGeoAlt := asFloat(state, 13)
	baroAltMeters, okBaroAlt := asFloat(state, 7)
	altMeters := geoAltMeters
	if !okGeoAlt {
		altMeters = baroAltMeters
	}
	altFeet := metersToFeetInt(altMeters)
	geoAltFeet := 0
	if okGeoAlt {
		geoAltFeet = metersToFeetInt(geoAltMeters)
	}
	baroAltFeet := 0
	if okBaroAlt {
		baroAltFeet = metersToFeetInt(baroAltMeters)
	}

	velMS, okVel := asFloat(state, 9)
	speedKnots := 0
	if okVel {
		speedKnots = metersPerSecondToKnotsInt(velMS)
	}

	hdg, okHdg := asFloat(state, 10)
	heading := 0
	if okHdg {
		heading = int(hdg + 0.5)
	}

	vrMS, okVR := asFloat(state, 11)
	verticalRateFPM := 0
	if okVR {
		verticalRateFPM = metersPerSecondToFeetPerMinuteInt(vrMS)
	}

	seen := time.Now()
	if lastContact, okLC := asInt64(state, 4); okLC && lastContact > 0 {
		seen = time.Unix(lastContact, 0)
	}

	a := adsb.Aircraft{
		ICAO:         icao,
		Latitude:     lat,
		Longitude:    lon,
		Altitude:     altFeet,
		Speed:        speedKnots,
		Heading:      heading,
		Track:        heading,
		Callsign:     callsign,
		Squawk:       squawk,
		VerticalRate: verticalRateFPM,
		OnGround:     onGround,
		Source:       "internet",
		Origin:       originCountry,
		GeoAlt:       geoAltFeet,
		BaroAlt:      baroAltFeet,
		Velocity:     velMS,
		Seen:         seen,
	}

	return a, true
}

func asString(v []interface{}, idx int) (string, bool) {
	if idx < 0 || idx >= len(v) || v[idx] == nil {
		return "", false
	}
	s, ok := v[idx].(string)
	return s, ok
}

func asFloat(v []interface{}, idx int) (float64, bool) {
	if idx < 0 || idx >= len(v) || v[idx] == nil {
		return 0, false
	}
	switch t := v[idx].(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func asInt64(v []interface{}, idx int) (int64, bool) {
	if idx < 0 || idx >= len(v) || v[idx] == nil {
		return 0, false
	}
	switch t := v[idx].(type) {
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	case json.Number:
		i, err := t.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

func asBool(v []interface{}, idx int) (bool, bool) {
	if idx < 0 || idx >= len(v) || v[idx] == nil {
		return false, false
	}
	b, ok := v[idx].(bool)
	return b, ok
}

func metersToFeetInt(m float64) int {
	if m == 0 {
		return 0
	}
	return int(m*3.28084 + 0.5)
}

func metersPerSecondToKnotsInt(ms float64) int {
	if ms == 0 {
		return 0
	}
	return int(ms*1.94384 + 0.5)
}

func metersPerSecondToFeetPerMinuteInt(ms float64) int {
	if ms == 0 {
		return 0
	}
	return int(ms*196.850394 + 0.5)
}

func init() {
	// Ensure Windows services don't buffer stdout too aggressively
	_ = os.Stdout
}
