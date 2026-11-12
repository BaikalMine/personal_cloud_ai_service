package release

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ai-access-gateway/internal/database"
)

var ErrDockerServiceAction = errors.New("release Docker service action is unavailable or unresolved")

// RuntimeRecoveryBinding binds the original observed physical database, not a
// connection string or a caller-selected label. This does not authorize restore.
func (p DockerHostPlan) RuntimeRecoveryBinding(epoch int64) HostRuntimeRecoveryBinding {
	if !validDockerHostPlan(p.data) {
		return HostRuntimeRecoveryBinding{}
	}
	body, _ := json.Marshal(p.data.Observed.Database)
	binding := HostRuntimeRecoveryBinding{DatabaseSHA256: hostRecoveryDigest(append([]byte("docker-plan/database/v1:"), body...)), ExpectedEpoch: epoch}
	if !validHostRecoveryBinding(binding) {
		return HostRuntimeRecoveryBinding{}
	}
	return binding
}

func (j *HostJournal) dockerServiceMaterial(epoch int64) (DockerHostPlan, database.RuntimeReleaseRequest, error) {
	var plan DockerHostPlan
	var request database.RuntimeReleaseRequest
	plan, err := j.committedDockerPlan()
	if err != nil {
		return plan, request, err
	}
	configurationReceipt, err := j.databaseConfigReceipt()
	if err != nil {
		return plan, request, err
	}
	if configurationReceipt != "" {
		if _, _, err := j.readDockerDatabaseConfiguration(configurationReceipt); err != nil {
			return plan, request, err
		}
	}
	binding := plan.RuntimeRecoveryBinding(epoch)
	if !validHostRecoveryBinding(binding) {
		return plan, request, ErrHostRecoveryUnavailable
	}
	receipt, err := j.recoveryReceipt(binding)
	if err != nil || receipt == "" {
		return plan, request, ErrHostRecoveryUnavailable
	}
	request, _, err = j.readRuntimeRecovery(binding, receipt)
	return plan, request, err
}

func (j *HostJournal) hasAppliedEffect(effect HostEffect, target string) bool {
	intents := make(map[uint64]bool)
	for _, event := range j.events {
		if event.Kind == "intent" && event.Effect == effect && event.TargetSHA256 == target {
			intents[event.Sequence] = true
		}
		if event.Kind == "observe" && intents[event.IntentSequence] && event.Observation == HostApplied {
			return true
		}
	}
	return false
}

// BeginDockerGatewayFence revalidates the saved preparation while the ordinary
// Gateway endpoint is still available, then journals and acquires the DB fence.
// Private plan and recovery material must already be committed. Failure after
// intent leaves the operation unresolved; closing a controller never unfences.
func (j *HostJournal) BeginDockerGatewayFence(ctx context.Context, db *sql.DB, sessionToken string, epoch int64) (*database.RuntimeRelease, error) {
	return j.beginDockerGatewayFence(ctx, db, sessionToken, epoch, newDockerObserver)
}

func (j *HostJournal) beginDockerGatewayFence(ctx context.Context, db *sql.DB, sessionToken string, epoch int64, open func(string) (*dockerObserver, error)) (*database.RuntimeRelease, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.beginDockerGatewayFenceLocked(ctx, db, sessionToken, epoch, open)
}

func (j *HostJournal) beginDockerGatewayFenceLocked(ctx context.Context, db *sql.DB, sessionToken string, epoch int64, open func(string) (*dockerObserver, error)) (*database.RuntimeRelease, error) {
	plan, request, err := j.dockerServiceMaterial(epoch)
	if err != nil {
		return nil, err
	}
	target := j.recoveryTarget(plan.RuntimeRecoveryBinding(epoch))
	if j.state.Pending.Sequence != 0 || j.hasAppliedEffect(HostFence, target) {
		return nil, ErrHostJournalState
	}
	if plan.revalidate(ctx, db, sessionToken, open) != nil {
		return nil, ErrHostPlanUnavailable
	}
	if err := j.append(hostRecord{Kind: "intent", Effect: HostFence, Target: target}); err != nil {
		return nil, err
	}
	controller, err := database.BeginRuntimeReleaseForDatabase(ctx, db, request, plan.data.Observed.Database)
	if err != nil {
		return nil, ErrDockerServiceAction
	}
	err = controller.WithActiveFence(ctx, request, plan.data.Observed.Database, func(database.RuntimeFenceState) error {
		_, _, err := j.dockerServiceMaterial(epoch)
		return err
	})
	if err == nil {
		err = j.append(hostRecord{Kind: "observe", Intent: j.state.Pending.Sequence, Observation: HostApplied,
			Evidence: hostRecoveryDigest([]byte("docker-plan/fence-active/v1:" + target))})
	}
	if err != nil {
		controller.Close()
		return nil, ErrDockerServiceAction
	}
	return controller, nil
}

func dockerStopTarget(plan DockerHostPlan, epoch int64, grace int) string {
	body, _ := json.Marshal(struct {
		Purpose      string
		Plan         HostOperationSpec
		Binding      HostRuntimeRecoveryBinding
		GraceSeconds int
	}{"docker-stop/v1", plan.Operation(), plan.RuntimeRecoveryBinding(epoch), grace})
	return hostRecoveryDigest(body)
}

// ResumeDockerGatewayFence reclaims an existing matching fence without any
// service request, normal HTTP probe, or new fence write. A pending fence intent
// can be acknowledged only after observing the original active DB control row.
func (j *HostJournal) ResumeDockerGatewayFence(ctx context.Context, db *sql.DB, epoch int64) (*database.RuntimeRelease, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.resumeDockerGatewayFenceLocked(ctx, db, epoch)
}

func (j *HostJournal) resumeDockerGatewayFenceLocked(ctx context.Context, db *sql.DB, epoch int64) (*database.RuntimeRelease, error) {
	plan, request, err := j.dockerServiceMaterial(epoch)
	if err != nil {
		return nil, err
	}
	target := j.recoveryTarget(plan.RuntimeRecoveryBinding(epoch))
	pendingFence := j.state.Pending.Effect == HostFence && j.state.Pending.TargetSHA256 == target
	if !pendingFence && !j.hasAppliedEffect(HostFence, target) {
		return nil, ErrHostJournalState
	}
	controller, err := database.ResumeRuntimeReleaseForDatabase(ctx, db, request, plan.data.Observed.Database)
	if err != nil {
		return nil, ErrDockerServiceAction
	}
	err = controller.WithActiveFence(ctx, request, plan.data.Observed.Database, func(database.RuntimeFenceState) error {
		_, _, err := j.dockerServiceMaterial(epoch)
		return err
	})
	if err == nil && pendingFence {
		err = j.append(hostRecord{Kind: "observe", Intent: j.state.Pending.Sequence, Observation: HostApplied,
			Evidence: hostRecoveryDigest([]byte("docker-plan/fence-active/v1:" + target))})
	}
	if err != nil {
		controller.Close()
		return nil, ErrDockerServiceAction
	}
	return controller, nil
}

// StopDockerGateway dispatches at most one stop to the saved full container ID.
// This primitive is not a complete installer. The cooperative host lock does
// not exclude privileged external Docker users; Engine stop has no atomic
// StartedAt precondition. Such users must be excluded operationally.
func (j *HostJournal) StopDockerGateway(ctx context.Context, controller *database.RuntimeRelease, epoch int64, graceSeconds int) (HostObservation, error) {
	return j.dockerStop(ctx, controller, epoch, graceSeconds, false, newDockerObserver)
}

// ReconcileDockerGatewayStop sends GETs only. A still-running container is
// unknown, not not_applied: a lost stop request may still execute at the daemon.
func (j *HostJournal) ReconcileDockerGatewayStop(ctx context.Context, controller *database.RuntimeRelease, epoch int64, graceSeconds int) (HostObservation, error) {
	return j.dockerStop(ctx, controller, epoch, graceSeconds, true, newDockerObserver)
}

func (j *HostJournal) dockerStop(ctx context.Context, controller *database.RuntimeRelease, epoch int64, grace int, reconcile bool, open func(string) (*dockerObserver, error)) (HostObservation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if controller == nil || grace < 0 || grace > 120 {
		return "", ErrHostJournalState
	}
	plan, request, err := j.dockerServiceMaterial(epoch)
	if err != nil {
		return "", err
	}
	target := dockerStopTarget(plan, epoch, grace)
	if !j.hasAppliedEffect(HostFence, j.recoveryTarget(plan.RuntimeRecoveryBinding(epoch))) {
		return "", ErrHostJournalState
	}
	if reconcile {
		if j.state.Pending.Effect != HostStopService || j.state.Pending.TargetSHA256 != target {
			return "", ErrHostJournalState
		}
	} else if j.state.Pending.Sequence != 0 || j.hasAppliedEffect(HostStopService, target) {
		return "", ErrHostJournalState
	}
	var evidence string
	err = controller.WithActiveFence(ctx, request, plan.data.Observed.Database, func(database.RuntimeFenceState) error {
		d, err := open(plan.data.EngineEndpoint)
		if err != nil {
			return ErrDockerServiceAction
		}
		defer d.close()
		if !reconcile {
			if err := d.checkPlannedGateway(ctx, plan); err != nil {
				return err
			}
			if err := j.append(hostRecord{Kind: "intent", Effect: HostStopService, Target: target}); err != nil {
				return err
			}
			// No replay even after a transport error. Reconciliation is separate.
			if err := d.stopContainer(ctx, plan.data.Identity.ContainerID, grace); err != nil {
				return err
			}
		}
		evidence, err = d.observePlannedStop(ctx, plan)
		if err != nil {
			return err
		}
		_, _, err = j.dockerServiceMaterial(epoch)
		return err
	})
	if err != nil {
		if j.state.Pending.Effect == HostStopService && j.state.Pending.TargetSHA256 == target {
			return HostUnknown, ErrDockerServiceAction
		}
		return "", ErrDockerServiceAction
	}
	if err := j.append(hostRecord{Kind: "observe", Intent: j.state.Pending.Sequence, Observation: HostApplied, Evidence: evidence}); err != nil {
		return HostUnknown, err
	}
	return HostApplied, nil
}

func (d *dockerObserver) checkPlannedGateway(ctx context.Context, plan DockerHostPlan) error {
	p := plan.data
	previous, manifest, err := observeHostPackage(HostPackageReference{p.Previous.Directory, p.Previous.ManifestSHA256})
	if err != nil || previous != p.Previous {
		return ErrHostPlanUnavailable
	}
	candidate, _, err := observeHostPackage(HostPackageReference{p.Candidate.Directory, p.Candidate.ManifestSHA256})
	if err != nil || candidate != p.Candidate || d.checkEngine(ctx, p.Identity.EngineID) != nil {
		return ErrHostPlanUnavailable
	}
	base, err := validationOrigin(p.Origin)
	if err != nil {
		return ErrHostPlanUnavailable
	}
	options := DockerGatewayOptions{ContainerID: p.Identity.ContainerID, GatewayPort: p.GatewayPort}
	before, err := d.inspectGateway(ctx, options, base, p.Identity.ImageID)
	if err != nil || before.State.PID != p.Identity.InitPID || before.State.StartedAt != p.Identity.ContainerStartedAt ||
		before.RestartCount != p.Observed.RestartCount || !bytes.Equal(before.configurationJSON, p.Observed.Configuration) {
		return ErrHostPlanUnavailable
	}
	pid, err := d.gatewayProcess(ctx, before)
	artifact, _, ok := dockerGatewayArtifact(manifest)
	if err != nil || pid != p.Identity.GatewayPID || !ok || artifact.File.SHA256 != p.Identity.ExecutableSHA256 ||
		d.checkGatewayExecutable(ctx, p.Identity.ContainerID, artifact.File) != nil {
		return ErrHostPlanUnavailable
	}
	after, err := d.inspectGateway(ctx, options, base, p.Identity.ImageID)
	if err != nil || !sameDockerGateway(before, after) || d.checkEngine(ctx, p.Identity.EngineID) != nil {
		return ErrHostPlanUnavailable
	}
	return nil
}

func (d *dockerObserver) stopContainer(ctx context.Context, id string, grace int) error {
	if !validHostHash(id) || grace < 0 || grace > 120 {
		return ErrDockerServiceAction
	}
	transport := d.transport.Clone()
	transport.ResponseHeaderTimeout = time.Duration(grace+5) * time.Second
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Duration(grace+10) * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	u := url.URL{Scheme: "http", Host: "docker", Path: dockerObservationAPI + "/containers/" + id + "/stop", RawQuery: url.Values{"t": {strconv.Itoa(grace)}}.Encode()}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return ErrDockerServiceAction
	}
	request.Header.Set("User-Agent", "Gateway-Release-Service/1")
	response, err := client.Do(request)
	if err != nil {
		return ErrDockerServiceAction
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusNotModified {
		return ErrDockerServiceAction
	}
	return nil
}

func (d *dockerObserver) observePlannedStop(ctx context.Context, plan DockerHostPlan) (string, error) {
	p := plan.data
	if d.checkEngine(ctx, p.Identity.EngineID) != nil {
		return "", ErrDockerServiceAction
	}
	observed, err := d.readContainer(ctx, p.Identity.ContainerID)
	s := observed.State
	started, startErr := time.Parse(time.RFC3339Nano, s.StartedAt)
	finished, finishErr := time.Parse(time.RFC3339Nano, s.FinishedAt)
	if err != nil || observed.ID != p.Identity.ContainerID || observed.Image != p.Identity.ImageID || observed.Path != "/app/gateway" || len(observed.Args) != 0 ||
		observed.RestartCount != p.Observed.RestartCount || s.StartedAt != p.Identity.ContainerStartedAt || startErr != nil || finishErr != nil || started.IsZero() || finished.Before(started) ||
		s.Status != "exited" || s.Running || s.Paused || s.Restarting || s.Dead || s.OOMKilled || s.PID != 0 || s.Error != "" {
		return "", ErrDockerServiceAction
	}
	// Docker clears runtime network addresses/ports on stop. Persistent config
	// and mount identities must remain exactly as planned.
	var before, after map[string]json.RawMessage
	if json.Unmarshal(p.Observed.Configuration, &before) != nil || json.Unmarshal(observed.configurationJSON, &after) != nil {
		return "", ErrDockerServiceAction
	}
	for _, key := range []string{"Config", "HostConfig", "Mounts"} {
		if !bytes.Equal(before[key], after[key]) {
			return "", ErrDockerServiceAction
		}
	}
	if d.checkEngine(ctx, p.Identity.EngineID) != nil {
		return "", ErrDockerServiceAction
	}
	body, _ := json.Marshal(struct {
		Plan  HostOperationSpec
		State any
	}{plan.Operation(), s})
	return hostRecoveryDigest(body), nil
}
