package main

import (
	"context"
	"log/slog"
	"time"

	openfgamodel "github.com/muhananaufal/selaras-platform-go/deploy/openfga"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic"
	clinicpg "github.com/muhananaufal/selaras-platform-go/internal/clinic/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authz"
)

// startProjection bootstraps the OpenFGA store and model and starts applying
// queued tuple changes to it (ADR-030).
//
// Without OPENFGA_URL nothing is projected and that is said out loud:
// consents are still recorded and queued, and no clinician can read any
// patient until the projection runs - the safe direction. With it, a store
// that cannot be reached stops the start, as any other dependency does.
func startProjection(ctx context.Context, log *slog.Logger, cfg clinic.Config, repo *clinicpg.Repository) error {
	if cfg.OpenFGAURL == "" {
		log.Warn("OPENFGA_URL is not set; consent changes are queued but not projected, and no clinician can read any patient")
		return nil
	}
	bootCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	fga, err := authz.Bootstrap(bootCtx, cfg.OpenFGAURL, cfg.OpenFGAStore, openfgamodel.Model)
	if err != nil {
		return err
	}
	projector, err := app.NewProjector(repo, fga, log, time.Now)
	if err != nil {
		return err
	}
	log.Info("projecting consent to OpenFGA", "store", fga.StoreID(), "model", fga.ModelID())
	go projector.Run(ctx)
	return nil
}
