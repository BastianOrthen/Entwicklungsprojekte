// Command dftracker connects to a Multi-Domain-Simulator DF JSON stream,
// runs a Multi-Hypothesis Particle-Filter Tracker on the incoming bearings,
// and forwards the resulting localizations to the SAW backend over gRPC.
//
// Architecture:
//
//	[MDS DF :50511] ──► dfInput ──► tracker (MHT + PF) ──► sawOutput ──► [SAW gRPC :50051]
//
// One container per logical tracker. To run a second tracker with different
// PF parameters or filtering rules, simply start another instance of this
// binary (or a sibling container) with different flags.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	var (
		serverAddr   = flag.String("server", "situation-awareness:50051", "SAW gRPC endpoint")
		mdsHost      = flag.String("mds-host", "simulator", "MDS DF host")
		mdsPort      = flag.Int("mds-port", 50511, "MDS DF TCP port")
		instanceName = flag.String("name", "df-tracker", "logical tracker name (used as sensor source)")

		numParticles  = flag.Int("particles", 500, "particles per track")
		minSensors    = flag.Int("min-sensors", 2, "minimum distinct sensors required to confirm a track")
		gateChi2      = flag.Float64("gate", 9.21, "associate gate (Mahalanobis-style χ² threshold, default 9.21 ≈ 99%)")
		minRangeM     = flag.Float64("min-range-m", 5000, "minimum range from sensor when seeding particles (m)")
		defaultMaxM   = flag.Float64("default-max-range-m", 200000, "default sensor range when MDS emits none (m)")
		processNoiseM = flag.Float64("process-noise-m", 60, "1-sigma process noise per second on position (m/√s) — base value, scaled per motion model")
		velocityNoise = flag.Float64("velocity-noise-mps", 2.5, "1-sigma process noise per second on velocity (m/s/√s) — base value, scaled per motion model")

		emitInterval  = flag.Duration("emit-interval", 1*time.Second, "min time between localizations per track")
		trackTTL      = flag.Duration("track-ttl", 30*time.Second, "delete tracks not updated within this window")
		maxStdDevM    = flag.Float64("max-stddev-m", 25000, "max position std-dev (m) — beyond this a confirmed track stops emitting until tightened")
		divergeStdDev = flag.Float64("diverge-stddev-m", 80000, "delete tracks whose particle cloud diverges beyond this (m)")

		smoothingAlpha = flag.Float64("smooth-alpha", 0.30, "EMA factor on emitted position (0=disabled, 1=raw mean). 0.20–0.40 gives smooth tracks without lag")
		stationaryV    = flag.Float64("stationary-speed-mps", 3.0, "treat track as stationary on emit when speed below this (m/s); zeroes course/speed and tightens position smoothing")

		httpAddr = flag.String("http", ":8090", "HTTP listen address for the particle visualiser")
	)
	flag.Parse()

	cfg := TrackerConfig{
		NumParticles:    *numParticles,
		MinSensors:      *minSensors,
		GateChi2:        *gateChi2,
		MinRangeM:       *minRangeM,
		DefaultMaxRange: *defaultMaxM,
		ProcessNoiseM:   *processNoiseM,
		VelocityNoiseM:  *velocityNoise,
		EmitInterval:    *emitInterval,
		TrackTTL:        *trackTTL,
		MaxStdDevM:      *maxStdDevM,
		DivergeStdDevM:  *divergeStdDev,
		InstanceName:    *instanceName,
		SmoothingAlpha:           *smoothingAlpha,
		StationarySpeedThreshold: *stationaryV,
	}

	log.Printf("df-tracker [%s]: starting", *instanceName)
	log.Printf("  SAW gRPC : %s", *serverAddr)
	log.Printf("  MDS DF   : %s:%d", *mdsHost, *mdsPort)
	log.Printf("  PF       : N=%d, min-sensors=%d, gate-χ²=%.2f, range=[%.0f .. %.0f m]",
		cfg.NumParticles, cfg.MinSensors, cfg.GateChi2, cfg.MinRangeM, cfg.DefaultMaxRange)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-sigChan
		log.Println("df-tracker: shutdown signal received")
		cancel()
	}()

	out := newSAWOutput(*serverAddr)
	tracker := NewTracker(cfg, out)

	go tracker.Run(ctx) // periodic predict/prune
	go startVisualiser(ctx, *httpAddr, tracker)

	mdsAddr := *mdsHost + ":" + itoa(*mdsPort)
	if err := runDFInput(ctx, mdsAddr, tracker); err != nil && ctx.Err() == nil {
		log.Printf("df-tracker: input loop ended: %v", err)
	}

	out.Close()
	log.Println("df-tracker: bye")
}

// itoa avoids pulling strconv into main for one int.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
