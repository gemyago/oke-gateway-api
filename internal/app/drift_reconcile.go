package app

import (
	"time"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const minimumDriftRequeueInterval = time.Minute
const maxDriftRequeueJitterRatio = 10

func driftRequeue(interval time.Duration) reconcile.Result {
	if interval <= 0 {
		return reconcile.Result{}
	}
	if interval < minimumDriftRequeueInterval {
		interval = minimumDriftRequeueInterval
	}
	jitter := interval / maxDriftRequeueJitterRatio
	if jitter > 0 {
		interval += time.Duration(time.Now().UnixNano() % int64(jitter+1))
	}
	return reconcile.Result{RequeueAfter: interval}
}
