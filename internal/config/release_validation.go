package config

import (
	"errors"
	"net"
	"strconv"
	"strings"

	"ai-access-gateway/internal/database"
)

func loadReleaseValidation(restoreDrill bool) (*database.RuntimeValidationRequest, error) {
	enabled, err := booleanEnv("RELEASE_VALIDATION_MODE", false)
	if err != nil {
		return nil, err
	}
	request := database.RuntimeValidationRequest{
		OperationID:           strings.TrimSpace(requiredEnv("RELEASE_OPERATION_ID")),
		CandidateManifestHash: strings.TrimSpace(requiredEnv("RELEASE_CANDIDATE_MANIFEST_SHA256")),
		Token:                 strings.TrimSpace(requiredEnv("RELEASE_OPERATION_TOKEN")),
	}
	epoch := strings.TrimSpace(requiredEnv("RELEASE_OPERATION_EPOCH"))
	if !enabled {
		if request.OperationID != "" || request.Token != "" || request.CandidateManifestHash != "" || epoch != "" {
			return nil, errors.New("release operation credentials require RELEASE_VALIDATION_MODE=true")
		}
		return nil, nil
	}
	request.Epoch, err = strconv.ParseInt(epoch, 10, 64)
	if restoreDrill || err != nil || request.Validate() != nil {
		return nil, errors.New("release validation requires a valid operation identity, epoch, candidate hash and token; restore-drill mode must be disabled")
	}
	for _, key := range []string{"PUBLIC_ADDR", "ADMIN_ADDR"} {
		host, port, err := net.SplitHostPort(requiredEnv(key))
		address := net.ParseIP(host)
		portNumber, portErr := strconv.Atoi(port)
		if err != nil || address == nil || !address.IsLoopback() || portErr != nil || portNumber < 1 || portNumber > 65535 {
			return nil, errors.New("release validation requires explicit loopback IP listener addresses and nonzero ports")
		}
	}
	return &request, nil
}
