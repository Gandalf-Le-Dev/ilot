package ilotctl

import (
	"context"
	"fmt"
	"strings"

	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
)

type imageFailure struct {
	appInstance plan.Instance
	err         error
}

func registryOfImage(image string) string {
	firstPart, _, hasPath := strings.Cut(image, "/")
	if hasPath && (strings.ContainsAny(firstPart, ".:") || firstPart == "localhost") {
		return firstPart
	}
	return "docker.io"
}

func imagePullHint(image, pullOutput string) string {
	for _, marker := range []string{"Authentication is required", "unauthorized", "no basic auth credentials", "denied:"} {
		if strings.Contains(pullOutput, marker) {
			loginCommand := "docker login"
			if registry := registryOfImage(image); registry != "docker.io" {
				loginCommand += " " + registry
			}
			return "log in to the registry, then run the command again: " + loginCommand
		}
	}
	return ""
}

func imagePullError(image, pullOutput string, pullErr error) error {
	if hint := imagePullHint(image, pullOutput); hint != "" {
		return fmt.Errorf("pull %s: %w\n%s", image, pullErr, hint)
	}
	return fmt.Errorf("pull %s: %w", image, pullErr)
}

// An image shared by several apps is checked once, and its failure is reported for each of them.
func (controller *Controller) ensureImages(snapshot plan.Snapshot, ctx context.Context, startOrder []plan.Instance, needsImage func(plan.Instance) bool, stopAtFirstFailure bool) []imageFailure {
	errByImage := map[string]error{}
	var failures []imageFailure
	for _, appInstance := range startOrder {
		if err := ctx.Err(); err != nil {
			return append(failures, imageFailure{appInstance: appInstance, err: err})
		}
		if !snapshot.IsContainer(appInstance) || !needsImage(appInstance) {
			continue
		}
		image := snapshot.App(appInstance).Container.Image
		imageErr, isChecked := errByImage[image]
		if !isChecked {
			pullOutput, err := controller.EnsureImage(ctx, image)
			if err != nil {
				imageErr = imagePullError(image, pullOutput, err)
			}
			errByImage[image] = imageErr
		}
		if imageErr == nil {
			continue
		}
		failures = append(failures, imageFailure{appInstance: appInstance, err: imageErr})
		if stopAtFirstFailure {
			return failures
		}
	}
	return failures
}
