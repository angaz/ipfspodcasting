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
	IPFSPeers = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "peers",
		Help:      "Number of connected IPFS peers",
	})
	IPFSRepoDiskUsage = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "repo_disk_used_bytes",
		Help:      "IPFS repo disk usage",
	})
	IPFSRepoStorageMax = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "repo_storage_max_bytes",
		Help:      "IPFS repo max storage limit",
	})
	IPFSRepoObjects = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "repo_objects",
		Help:      "Number of IPFS repo objects",
	})
)

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
