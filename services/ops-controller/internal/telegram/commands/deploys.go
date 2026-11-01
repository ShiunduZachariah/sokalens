package commands

import (
	"context"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/deployments"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// deploys renders recent deployments and their rollback state. Spec §10.6.
//
// The pending-train line the spec shows is a Phase T4 concern; this command
// reports only what ops_deployments actually records today.
func (d Deps) deploys(ctx context.Context, _ telegram.Request) (telegram.Response, error) {
	if d.Deployments == nil {
		return telegram.Response{Body: "Deployment store " + unavailable + "."}, nil
	}

	scoped, cancel := d.scope(ctx)
	defer cancel()
	listing, err := d.Deployments.Recent(scoped, deployCap, d.Environment)
	if err != nil {
		return telegram.Response{Body: "Deployment store " + unavailable + "."}, nil
	}
	if len(listing.Deployments) == 0 {
		return telegram.Response{Body: "No deployments recorded."}, nil
	}

	rows := make([][]string, 0, len(listing.Deployments))
	for _, dep := range listing.Deployments {
		class := dep.DeployClass
		if class == "" {
			class = "-"
		}
		rows = append(rows, []string{
			shortSHA(dep.SourceCommit),
			class,
			age(dep.Age) + " ago",
			truncateCell(dep.Service, 14),
			deployMark(dep),
		})
	}

	body := telegram.Bold("Recent deployments") + "\n\n" + telegram.PreTable(rows)
	if listing.Truncated {
		body += "\n(older deployments not shown)"
	}
	return telegram.Response{Body: body}, nil
}

// deployMark renders the outcome glyph for one deployment.
func deployMark(dep deployments.Deployment) string {
	switch dep.RollbackState {
	case deployments.RollbackRolledBack:
		return "↩️ rolled back"
	case deployments.RollbackSuperseded:
		return "superseded"
	}
	switch dep.Status {
	case "succeeded":
		return "✅ healthy"
	case "failed":
		return "❌ failed"
	case "cancelled":
		return "cancelled"
	case "running", "pending":
		return "… " + dep.Status
	default:
		return dep.Status
	}
}
