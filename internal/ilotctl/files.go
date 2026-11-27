package ilotctl

import (
	"context"
	"os"
	"path/filepath"

	"github.com/Gandalf-Le-Dev/ilot/internal/plan"
)

func (controller *Controller) applyAll(ctx context.Context, snapshot plan.Snapshot) error {
	var specs []ProcessSpec
	for _, runningInstance := range snapshot.Wanted() {
		spec, err := snapshot.Spec(runningInstance)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(spec.LogFile), 0o700); err != nil {
			return err
		}
		specs = append(specs, spec)
		watcher, isWatched, err := snapshot.Watcher(runningInstance)
		if err != nil {
			return err
		}
		if isWatched {
			specs = append(specs, watcher)
		}
	}
	if err := controller.Supervisor.Apply(ctx, specs); err != nil {
		return err
	}
	return controller.Supervisor.SetRoutes(ctx, snapshot.Routes())
}
