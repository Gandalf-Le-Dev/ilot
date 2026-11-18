package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"

	"dev/internal/appconfig"
)

const defaultTemporalGRPCPort = 7233

func temporalAddress(config appconfig.Config) string {
	grpcPort := config.Apps["temporal"].FixedPorts["grpc"]
	if grpcPort == 0 {
		grpcPort = defaultTemporalGRPCPort
	}
	return "localhost:" + strconv.Itoa(grpcPort)
}

func ensureTemporalNamespace(address string) func(ctx context.Context, namespace string) error {
	return func(ctx context.Context, namespace string) error {
		describe := exec.CommandContext(ctx, "temporal", "operator", "namespace", "describe", "--namespace", namespace, "--address", address)
		if describe.Run() == nil {
			return nil
		}
		output, err := exec.CommandContext(ctx, "temporal", "operator", "namespace", "create", "--namespace", namespace, "--address", address).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, output)
		}
		return nil
	}
}

func deleteTemporalNamespace(address string) func(ctx context.Context, namespace string) error {
	return func(ctx context.Context, namespace string) error {
		output, err := exec.CommandContext(ctx, "temporal", "operator", "namespace", "delete", "--namespace", namespace, "--address", address, "--yes").CombinedOutput()
		if err != nil && bytes.Contains(output, []byte("is not found")) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %s", err, output)
		}
		return nil
	}
}
