/*
Copyright 2021 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package services

import (
	"context"

	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"

	"github.com/zmb3/teleport/api/types"
)

// ReconcilerConfig is the resource reconciler configuration.
type ReconcilerConfig struct {
	// Matcher is used to match resources.
	Matcher Matcher
	// GetCurrentResources returns currently registered resources.
	GetCurrentResources func() types.ResourcesWithLabelsMap
	// GetNewResources returns resources to compare current resources against.
	GetNewResources func() types.ResourcesWithLabelsMap
	// OnCreate is called when a new resource is detected.
	OnCreate func(context.Context, types.ResourceWithLabels) error
	// OnUpdate is called when an existing resource is updated.
	OnUpdate func(context.Context, types.ResourceWithLabels) error
	// OnDelete is called when an existing resource is deleted.
	OnDelete func(context.Context, types.ResourceWithLabels) error
	// Log is the reconciler's logger.
	Log logrus.FieldLogger
}

// Matcher is used by reconciler to match resources.
type Matcher func(types.ResourceWithLabels) bool

// CheckAndSetDefaults validates the reconciler configuration and sets defaults.
func (c *ReconcilerConfig) CheckAndSetDefaults() error {
	if c.Matcher == nil {
		return trace.BadParameter("missing reconciler Matcher")
	}
	if c.GetCurrentResources == nil {
		return trace.BadParameter("missing reconciler GetCurrentResources")
	}
	if c.GetNewResources == nil {
		return trace.BadParameter("missing reconciler GetNewResources")
	}
	if c.OnCreate == nil {
		return trace.BadParameter("missing reconciler OnCreate")
	}
	if c.OnUpdate == nil {
		return trace.BadParameter("missing reconciler OnUpdate")
	}
	if c.OnDelete == nil {
		return trace.BadParameter("missing reconciler OnDelete")
	}
	if c.Log == nil {
		c.Log = logrus.WithField(trace.Component, "reconciler")
	}
	return nil
}

// NewReconciler creates a new reconciler with provided configuration.
func NewReconciler(cfg ReconcilerConfig) (*Reconciler, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	return &Reconciler{
		cfg: cfg,
		log: cfg.Log,
	}, nil
}

// Reconciler reconciles currently registered resources with new resources and
// creates/updates/deletes them appropriately.
//
// It's used in combination with watchers by agents (app, database, desktop)
// to enable dynamically registered resources.
type Reconciler struct {
	cfg ReconcilerConfig
	log logrus.FieldLogger
}

// Reconcile reconciles currently registered resources with new resources and
// creates/updates/deletes them appropriately.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	currentResources := r.cfg.GetCurrentResources()
	newResources := r.cfg.GetNewResources()

	r.log.Debugf("Reconciling %v current resources with %v new resources.",
		len(currentResources), len(newResources))

	var errs []error

	// Process already registered resources to see if any of them were removed.
	for _, current := range currentResources {
		if err := r.processRegisteredResource(ctx, newResources, current); err != nil {
			errs = append(errs, trace.Wrap(err))
		}
	}

	// Add new resources if there are any or refresh those that were updated.
	for _, new := range newResources {
		if err := r.processNewResource(ctx, currentResources, new); err != nil {
			errs = append(errs, trace.Wrap(err))
		}
	}

	return trace.NewAggregate(errs...)
}

// processRegisteredResource checks the specified registered resource against the
// new list of resources.
func (r *Reconciler) processRegisteredResource(ctx context.Context, newResources types.ResourcesWithLabelsMap, registered types.ResourceWithLabels) error {
	// See if this registered resource is still present among "new" resources.
	if new := newResources[registered.GetName()]; new != nil {
		return nil
	}

	r.log.Infof("%v %v removed, deleting.", registered.GetKind(), registered.GetName())
	if err := r.cfg.OnDelete(ctx, registered); err != nil {
		return trace.Wrap(err, "failed to delete  %v %v", registered.GetKind(), registered.GetName())
	}

	return nil
}

// processNewResource checks the provided new resource agsinst currently
// registered resources.
func (r *Reconciler) processNewResource(ctx context.Context, currentResources types.ResourcesWithLabelsMap, new types.ResourceWithLabels) error {
	// First see if the resource is already registered and if not, whether it
	// matches the selector labels and should be registered.
	registered := currentResources[new.GetName()]
	if registered == nil {
		if r.cfg.Matcher(new) {
			r.log.Infof("%v %v matches, creating.", new.GetKind(), new.GetName())
			if err := r.cfg.OnCreate(ctx, new); err != nil {
				return trace.Wrap(err, "failed to create %v %v", new.GetKind(), new.GetName())
			}
			return nil
		}
		r.log.Debugf("%v %v doesn't match, not creating.", new.GetKind(), new.GetName())
		return nil
	}

	// Don't overwrite resource of a different origin (e.g., keep static resource from config and ignore dynamic resource)
	if registered.Origin() != new.Origin() {
		r.log.Warnf("%v has different origin (%v vs %v), not updating.", new.GetName(),
			new.Origin(), registered.Origin())
		return nil
	}

	// If the resource is already registered but was updated, see if its
	// labels still match.
	if CompareResources(new, registered) != Equal {
		if r.cfg.Matcher(new) {
			r.log.Infof("%v %v updated, updating.", new.GetKind(), new.GetName())
			if err := r.cfg.OnUpdate(ctx, new); err != nil {
				return trace.Wrap(err, "failed to update %v %v", new.GetKind(), new.GetName())
			}
			return nil
		}
		r.log.Infof("%v %v updated and no longer matches, deleting.", new.GetKind(), new.GetName())
		if err := r.cfg.OnDelete(ctx, registered); err != nil {
			return trace.Wrap(err, "failed to delete %v %v", new.GetKind(), new.GetName())
		}
		return nil
	}

	r.log.Debugf("%v %v is already registered.", new.GetKind(), new.GetName())
	return nil
}
