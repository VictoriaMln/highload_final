package main

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

type Metric struct {
	Timestamp int64   `json:"timestamp"`
	CPU       float64 `json:"cpu"`
	RPS       float64 `json:"rps"`
}

type Analysis struct {
	Count       int     `json:"count"`
	WindowSize  int     `json:"windowSize"`
	RollingAvg  float64 `json:"rollingAvg"`
	StdDev      float64 `json:"stdDev"`
	ZScore      float64 `json:"zScore"`
	IsAnomaly   bool    `json:"isAnomaly"`
	LastRPS     float64 `json:"lastRps"`
	LastCPU     float64 `json:"lastCpu"`
	LastTs      int64   `json:"lastTimestamp"`
	ThresholdZ  float64 `json:"thresholdZ"`
	ComputedAt  int64   `json:"computedAt"`
}

const (
	windowSize = 50
	zThreshold = 2.0

	redisWindowKey = "rps_window"
	redisLastKey   = "last_analysis"
)

var (
	ingestTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ingest_requests_total",
		Help: "Total number of ingested metrics",
	})
	ingestLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "ingest_latency_seconds",
		Help:    "Latency of ingest endpoint",
		Buckets: prometheus.DefBuckets,
	})
	currentRollingAvg = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rolling_avg_rps",
		Help: "Current rolling average of RPS",
	})
	anomalyTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "anomalies_total",
		Help: "Total detected anomalies",
	})
	anomalyRate = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "anomaly_rate",
		Help: "Anomaly flag as 0/1 for latest sample",
	})
)

func init() {
	prometheus.MustRegister(ingestTotal, ingestLatency, currentRollingAvg, anomalyTotal, anomalyRate)
}

type Service struct {
	metricsCh chan Metric
	rdb       *redis.Client
	ctx       context.Context
}

func NewService(rdb *redis.Client) *Service {
	return &Service{
		metricsCh: make(chan Metric, 10_000),
		rdb:       rdb,
		ctx:       context.Background(),
	}
}

func (s *Service) StartWorkers(n int) {
	for i := 0; i < n; i++ {
		go s.worker(i)
	}
}

func (s *Service) worker(id int) {
	for m := range s.metricsCh {
		if err := s.rdb.LPush(s.ctx, redisWindowKey, m.RPS).Err(); err != nil {
			log.Printf("[worker %d] redis LPUSH error: %v", id, err)
			continue
		}
		if err := s.rdb.LTrim(s.ctx, redisWindowKey, 0, windowSize-1).Err(); err != nil {
			log.Printf("[worker %d] redis LTRIM error: %v", id, err)
			continue
		}

		values, err := s.rdb.LRange(s.ctx, redisWindowKey, 0, windowSize-1).Result()
		if err != nil {
			log.Printf("[worker %d] redis LRANGE error: %v", id, err)
			continue
		}

		nums := make([]float64, 0, len(values))
		var sum float64
		for _, v := range values {
			f, convErr := strconv.ParseFloat(v, 64)
			if convErr != nil {
				continue
			}
			nums = append(nums, f)
			sum += f
		}

		count := len(nums)
		mean := 0.0
		stddev := 0.0
		if count > 0 {
			mean = sum / float64(count)
			var variance float64
			for _, x := range nums {
				d := x - mean
				variance += d * d
			}
			variance /= float64(count)
			stddev = math.Sqrt(variance)
		}

		z := 0.0
		if count > 1 && stddev > 0 {
			z = (m.RPS - mean) / stddev
		}
		isAnomaly := math.Abs(z) > zThreshold

		anal := Analysis{
			Count:      count,
			WindowSize: windowSize,
			RollingAvg: mean,
			StdDev:     stddev,
			ZScore:     z,
			IsAnomaly:  isAnomaly,
			LastRPS:    m.RPS,
			LastCPU:    m.CPU,
			LastTs:     m.Timestamp,
			ThresholdZ: zThreshold,
			ComputedAt: time.Now().Unix(),
		}

		b, _ := json.Marshal(anal)
		if err := s.rdb.Set(s.ctx, redisLastKey, b, 0).Err(); err != nil {
			log.Printf("[worker %d] redis SET last_analysis error: %v", id, err)
		}

		currentRollingAvg.Set(mean)
		if isAnomaly {
			anomalyTotal.Inc()
			anomalyRate.Set(1)
		} else {
			anomalyRate.Set(0)
		}
	}
}

func (s *Service) handleIngest(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() { ingestLatency.Observe(time.Since(start).Seconds()) }()

	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var m Metric
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if m.Timestamp == 0 {
		m.Timestamp = time.Now().Unix()
	}

	select {
	case s.metricsCh <- m:
		ingestTotal.Inc()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	default:
		http.Error(w, "overloaded", http.StatusServiceUnavailable)
	}
}

func (s *Service) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	val, err := s.rdb.Get(s.ctx, redisLastKey).Result()
	if err == redis.Nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		http.Error(w, "redis error: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(val))
}

func main() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "redis-master:6379"
	}

	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping failed: %v", err)
	}
	log.Println("connected to redis:", redisAddr)

	svc := NewService(rdb)
	svc.StartWorkers(2)

	mux := http.NewServeMux()
	mux.HandleFunc("/ingest", svc.handleIngest)
	mux.HandleFunc("/analyze", svc.handleAnalyze)
	mux.Handle("/metrics", promhttp.Handler())

	addr := ":8080"
	log.Println("listening on", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
