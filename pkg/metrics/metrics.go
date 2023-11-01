package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	namespace = "ipfspodcasting_updater"

	JobsHistogram = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "job_seconds",
			Help:      "Time spent on a job",
		},
		[]string{
			"job_type",
			"status",
		},
	)
)

func RegisterIPFSPeersTotalFunc(fn func() float64) {
	promauto.NewCounterFunc(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "peers_total",
		Help:      "Number of connected IPFS peers",
	}, fn)
}

func ObserveJob(jobType string, isErr bool, duration time.Duration) {
	status := "success"
	if isErr {
		status = "error"
	}

	JobsHistogram.With(prometheus.Labels{
		"job_type": jobType,
		"status":   status,
	}).Observe(duration.Seconds())
}
