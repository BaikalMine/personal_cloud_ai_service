package release

import (
	"context"

	"ai-access-gateway/internal/database"
)

// BeginConfiguredDockerGatewayFence uses the original committed connection
// configuration. The ordinary admin session remains caller-supplied and is not
// retained. The returned controller owns its physical session until Close.
func (j *HostJournal) BeginConfiguredDockerGatewayFence(ctx context.Context, sessionToken string, epoch int64) (*database.RuntimeRelease, error) {
	return j.configuredDockerGatewayFence(ctx, sessionToken, epoch, false, newDockerObserver)
}

// ResumeConfiguredDockerGatewayFence needs neither a new DSN nor a normal HTTP
// session. It only reclaims an existing active fence and never repairs missing
// protected configuration, rotates credentials or changes the selected DB.
func (j *HostJournal) ResumeConfiguredDockerGatewayFence(ctx context.Context, epoch int64) (*database.RuntimeRelease, error) {
	return j.configuredDockerGatewayFence(ctx, "", epoch, true, newDockerObserver)
}

func (j *HostJournal) configuredDockerGatewayFence(ctx context.Context, sessionToken string, epoch int64, resume bool, open func(string) (*dockerObserver, error)) (*database.RuntimeRelease, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	plan, err := j.committedDockerPlan()
	if err != nil {
		return nil, err
	}
	receipt, err := j.databaseConfigReceipt()
	if err != nil || receipt == "" {
		return nil, ErrDockerDatabaseConfiguration
	}
	configuration, _, err := j.readDockerDatabaseConfiguration(receipt)
	if err != nil {
		return nil, err
	}
	db, err := openDockerDatabase(ctx, configuration, plan.data.Observed.Database)
	if err != nil {
		return nil, err
	}
	// DB.Close disables pool reconnection but does not close a borrowed Conn.
	// RuntimeRelease retains exactly that physical session, not the pool.
	defer db.Close()
	var controller *database.RuntimeRelease
	if resume {
		controller, err = j.resumeDockerGatewayFenceLocked(ctx, db, epoch)
	} else {
		controller, err = j.beginDockerGatewayFenceLocked(ctx, db, sessionToken, epoch, open)
	}
	if err != nil {
		return nil, err
	}
	if _, _, err := j.readDockerDatabaseConfiguration(receipt); err != nil {
		controller.Close()
		return nil, ErrDockerDatabaseConfiguration
	}
	return controller, nil
}
