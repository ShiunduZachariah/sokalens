package predictions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/recommendations"
)

func GenerationFollowUps(source jobs.Job, now time.Time) []jobs.NewJob {
	token := sourceToken(source)
	generationKey := "prediction-refresh:" + token
	payload, _ := json.Marshal(map[string]string{"generation_key": generationKey})
	return []jobs.NewJob{{
		JobKey: recommendations.GenerateCandidatesJobKey, Queue: "personalization", Priority: 60,
		Payload: payload, IdempotencyKey: "followup:recommendations:" + token,
		DeduplicateCompleted: true, RunAfter: now.UTC(),
	}}
}

func SettlementFollowUps(source jobs.Job, now time.Time) []jobs.NewJob {
	token := sourceToken(source)
	return []jobs.NewJob{{
		JobKey: SnapshotAccuracyJobKey, Queue: "predictions", Priority: 50,
		Payload: []byte(`{}`), IdempotencyKey: "followup:accuracy:" + token,
		DeduplicateCompleted: true, RunAfter: now.UTC(),
	}}
}

func sourceToken(source jobs.Job) string {
	identity := source.IdempotencyKey
	if identity == "" {
		identity = "job:" + strconv.FormatInt(source.ID, 10)
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:12])
}
