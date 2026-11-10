// Package provider defines the cloud-provider seam used by jauntbox.
package provider

import (
	"context"
	"errors"
	"time"
)

// ErrVolumeInUse means the volume is attached to a server that still exists,
// so waiting will not detach it.
var ErrVolumeInUse = errors.New("volume is attached to an existing server")

// Box is a running (or starting) jauntbox server.
type Box struct {
	ID         int64
	Name       string
	Status     string
	ServerType string
	Created    time.Time
}

// Volume is the persistent home volume.
type Volume struct {
	ID     int64
	Name   string
	Device string // /dev/disk/by-id/scsi-0HC_Volume_<ID>
}

// CreateBoxSpec describes a server to create.
type CreateBoxSpec struct {
	Name, ServerType, Image, Location, UserData string
	Labels                                      map[string]string
	VolumeID, SSHKeyID, FirewallID              int64
}

// ServerType describes an orderable server type.
type ServerType struct {
	Name       string
	Cores      int
	MemoryGB   float64
	Arch       string // "amd64" or "arm64"
	Locations  []string
	Deprecated bool
	// PriceMonthly is the monthly gross price in EUR per location name.
	PriceMonthly map[string]float64
}

// Provider manages the cloud resources of one jauntbox user.
// "Ensure" means find by name, create if absent, never modify or delete an existing resource.
type Provider interface {
	EnsureSSHKey(ctx context.Context, name, publicKey string, labels map[string]string) (int64, error)
	// EnsureFirewall creates a firewall with no inbound rules, applied via label
	// selector jauntbox=<user>,role=box (derived from labels with role replaced by box).
	EnsureFirewall(ctx context.Context, name string, labels map[string]string) (int64, error)
	EnsureVolume(ctx context.Context, name string, sizeGB int, location string, labels map[string]string) (Volume, error)
	// FindBox returns nil, nil when none match; an error if more than one does.
	FindBox(ctx context.Context, labels map[string]string) (*Box, error)
	// CreateBox enables public IPv4+IPv6 (egress) and attaches the volume with automount false.
	CreateBox(ctx context.Context, spec CreateBoxSpec) (Box, error)
	DeleteBox(ctx context.Context, id int64) error
	WaitDeleted(ctx context.Context, id int64) error
	// WaitVolumeDetached returns once the volume is attached to no server. It
	// respects ctx. The provider may keep reporting a volume as attached for a
	// while after its server is gone; that lag is waited out. A volume attached
	// to a live server fails fast with an error wrapping ErrVolumeInUse.
	WaitVolumeDetached(ctx context.Context, id int64) error
}

// TypeLister lists server types.
type TypeLister interface {
	ServerTypes(ctx context.Context) ([]ServerType, error)
}
