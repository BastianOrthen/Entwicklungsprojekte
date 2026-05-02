package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "situation-awareness/proto/sensorpb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// LocalizationOut is the protocol-agnostic localisation envelope.
type LocalizationOut struct {
	TrackID    string
	Lat        float64
	Lon        float64
	Altitude   float64
	Speed      float64
	SemiMajor  float64 // m
	SemiMinor  float64 // m
	Azimuth    float64 // deg
	Source     string
	Domain     string
	Frequency  float64 // Hz (centre)
	Sensor     string  // tracker name shown in SAW
	SignalType string
	Timestamp  time.Time

	// ContributingBearings carries the bearings that were fused into
	// this position. Each entry's TrackID matches the SAW-side bearing
	// TrackID (mds-plat-…) so the operator can see which bearings were
	// merged and hide them once verified.
	ContributingBearings []LocOutBearing
}

// LocOutBearing is the protocol-agnostic snapshot of a contributing
// bearing handed to the SAW output stage.
type LocOutBearing struct {
	TrackID    string
	Sensor     string
	Source     string
	Domain     string
	OriginLat  float64
	OriginLon  float64
	Direction  float64
	ErrorDeg   float64
	MaxRange   float64
	Frequency  float64
	SignalType string
	Timestamp  time.Time
}

// sawOutput owns a gRPC connection to SAW with auto-reconnect and an
// active stream-health monitor: a dead HTTP/2 stream is detected and
// rebuilt automatically instead of silently dropping every Send.
type sawOutput struct {
	addr string

	mu     sync.Mutex
	conn   *grpc.ClientConn
	client pb.SensorServiceClient
	stream pb.SensorService_StreamSensorDataClient
	cancel context.CancelFunc
	closed bool

	sentOK    atomic.Int64
	sentFail  atomic.Int64
	sentDrop  atomic.Int64
	reconnect atomic.Int64

	// outQ buffers localizations so callers (the tracker's Run loop)
	// never block on gRPC HTTP/2 backpressure. A dedicated sender
	// goroutine drains it.
	outQ chan LocalizationOut
}

const sawOutputQueueSize = 1024

func newSAWOutput(addr string) *sawOutput {
	s := &sawOutput{addr: addr, outQ: make(chan LocalizationOut, sawOutputQueueSize)}
	go s.heartbeat()
	go s.senderLoop()
	return s
}

// keepaliveParams keeps the HTTP/2 connection warm so a silently-dead
// peer (NAT timeout, container restart) is detected within ~30 s.
var keepaliveParams = keepalive.ClientParameters{
	Time:                20 * time.Second,
	Timeout:             10 * time.Second,
	PermitWithoutStream: true,
}

// connect (re)opens the streaming RPC. Caller must hold s.mu.
func (s *sawOutput) connect() error {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	conn, err := grpc.NewClient(s.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepaliveParams),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(8<<20)),
	)
	if err != nil {
		return fmt.Errorf("dial SAW gRPC: %w", err)
	}
	client := pb.NewSensorServiceClient(conn)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.StreamSensorData(ctx)
	if err != nil {
		cancel()
		_ = conn.Close()
		return fmt.Errorf("open SAW stream: %w", err)
	}
	s.conn = conn
	s.client = client
	s.stream = stream
	s.cancel = cancel
	log.Printf("df-tracker: gRPC stream open to %s", s.addr)
	return nil
}

// SendLocalization is non-blocking: it queues the localisation onto
// the output channel, dropping the message if the queue is full so the
// caller (the Run goroutine) is never stalled by gRPC backpressure.
func (s *sawOutput) SendLocalization(loc LocalizationOut) {
	select {
	case s.outQ <- loc:
	default:
		s.sentDrop.Add(1)
	}
}

// senderLoop drains outQ and forwards every localisation over the
// gRPC stream. Runs on its own goroutine — only this goroutine ever
// calls stream.Send, so no other code path can block on it.
func (s *sawOutput) senderLoop() {
	for loc := range s.outQ {
		s.sendLocked(loc)
	}
}

func (s *sawOutput) sendLocked(loc LocalizationOut) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}

	// Detect a stream that died silently (peer reset, keepalive fail).
	if s.stream != nil {
		if err := s.stream.Context().Err(); err != nil {
			log.Printf("df-tracker: stream context dead (%v) — rebuilding", err)
			s.tearDownLocked()
		}
	}
	if s.stream == nil {
		if err := s.connect(); err != nil {
			s.sentFail.Add(1)
			log.Printf("df-tracker: SAW reconnect failed: %v", err)
			return
		}
		s.reconnect.Add(1)
	}

	freqStr := ""
	if loc.Frequency > 0 {
		freqStr = fmt.Sprintf("%.3f MHz", loc.Frequency/1e6)
	}

	// Encode the contributing bearing TrackIDs as classification
	// metadata so the SAW UI can offer a one-click "Merge with MHT
	// bearings" workflow on the pf-* localization. Geometry-only
	// correlation in SAW only proposes 1-to-1 pairs; the MHT knows the
	// full N-way assignment, so we hand it over via this side-channel.
	cls := map[string]string{}
	if len(loc.ContributingBearings) > 0 {
		ids := make([]string, 0, len(loc.ContributingBearings))
		for _, cb := range loc.ContributingBearings {
			if cb.TrackID != "" {
				ids = append(ids, cb.TrackID)
			}
		}
		if len(ids) > 0 {
			cls["mhtContributingBearings"] = strings.Join(ids, ",")
		}
	}

	// Send a regular Localization, NOT a MergedLocating. This way:
	//  • the pf-* track appears in SAW as its own Localization track
	//    (pink diamond, draggable, fully featured),
	//  • SAW's correlation engine pairs pf-* with the mds-plat-*
	//    bearing tracks via "Bearing points to position" / "Similar
	//    frequency", which surfaces a Merge button in the operator UI,
	//  • the operator triggers the actual fusion via that Merge button —
	//    the MHT only suggests, the operator decides.
	msg := &pb.LocalizationMsg{
		TrackId:   loc.TrackID,
		Timestamp: timestamppb.New(loc.Timestamp),
		Position: &pb.GeoPosition{
			Latitude:  loc.Lat,
			Longitude: loc.Lon,
			Altitude:  loc.Altitude,
		},
		ErrorEllipse: &pb.ErrorEllipse{
			SemiMajor: loc.SemiMajor,
			SemiMinor: loc.SemiMinor,
			Azimuth:   loc.Azimuth,
		},
		Sidc:      sidcFor(loc.Domain, loc.Source),
		Altitude:  loc.Altitude,
		Speed:     loc.Speed,
		Source:    loc.Source,
		Frequency: freqStr,
		Domain:    loc.Domain,
		Sensor:    loc.Sensor,
		EmitterParams: &pb.EmitterParams{
			FreqCenter: loc.Frequency,
			SignalType: loc.SignalType,
		},
		ClassificationResults: cls,
	}
	if err := s.stream.Send(&pb.SensorData{
		Data: &pb.SensorData_Localization{Localization: msg},
	}); err != nil {
		s.sentFail.Add(1)
		log.Printf("df-tracker: stream send failed: %v — will reconnect", err)
		s.tearDownLocked()
		return
	}
	s.sentOK.Add(1)
}

// tearDownLocked closes the current stream/conn so the next Send will
// reconnect. Caller must hold s.mu.
func (s *sawOutput) tearDownLocked() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.stream = nil
}

// heartbeat logs send statistics every 5 s and proactively refreshes
// a stream whose context died — guards against the case where SAW
// restarts and our keepalive eventually notices.
func (s *sawOutput) heartbeat() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	var lastOK int64
	for range t.C {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		streamDead := s.stream == nil ||
			s.stream.Context().Err() != nil
		s.mu.Unlock()

		ok := s.sentOK.Load()
		fail := s.sentFail.Load()
		drop := s.sentDrop.Load()
		rc := s.reconnect.Load()
		log.Printf("df-tracker: SAW stats — sent=%d (Δ%d), failed=%d, dropped=%d, reconnects=%d, queue=%d/%d, streamDead=%v",
			ok, ok-lastOK, fail, drop, rc, len(s.outQ), cap(s.outQ), streamDead)
		lastOK = ok

		if streamDead {
			s.mu.Lock()
			if !s.closed {
				if err := s.connect(); err != nil {
					log.Printf("df-tracker: heartbeat reconnect failed: %v", err)
				} else {
					s.reconnect.Add(1)
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *sawOutput) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.stream != nil {
		_, _ = s.stream.CloseAndRecv()
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// sidcFor returns a reasonable default MIL-STD-2525 SIDC for a domain.
// Track-source ELINT/COMINT is encoded as "Suspect" affiliation so it
// is visually distinct from MDS-derived friendly tracks.
func sidcFor(domain, _ string) string {
	switch domain {
	case "Air":
		return "SSAP-----------"
	case "SeaSurface":
		return "SSSP-----------"
	case "Ground":
		return "SSGP-----------"
	}
	return "SSGP-----------"
}
