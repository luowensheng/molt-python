package installer

import (
	"context"
	"fmt"
	"molt/pkg/types"
)

type Installer struct{ cfg types.InstallConfig }

func New(cfg types.InstallConfig) (*Installer, error) {
	return &Installer{cfg: cfg}, nil
}

func (i *Installer) Install(ctx context.Context, m *types.Manifest, targetDir string) error {
	if i.cfg.DryRun {
		fmt.Println("[dry-run] would install to", targetDir)
		return nil
	}
	fmt.Printf("Installing %s v%s to %s...\n", m.AppName, m.Version, targetDir)
	return nil
}
