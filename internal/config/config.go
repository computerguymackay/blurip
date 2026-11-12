// Package config defines blurip's settings, their defaults and validation,
// and maps them to the key/value settings table.
package config

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/computerguymackay/blurip/internal/store"
)

// RipMode decides whether a disc is ripped with the saved defaults or the
// user is asked each time.
type RipMode string

const (
	RipDefaults RipMode = "defaults"
	RipAsk      RipMode = "ask"
)

// TitleMode decides which titles are ripped by default.
type TitleMode string

const (
	TitleMainFeature TitleMode = "main_feature" // largest title
)

// DriveMode decides how several drives are handled.
type DriveMode string

const (
	DriveSingle     DriveMode = "single"
	DriveSequential DriveMode = "sequential"
	DriveParallel   DriveMode = "parallel"
)

// Config is blurip's full configuration.
type Config struct {
	MakeMKVCon     string    // path to makemkvcon
	OutputDir      string    // where finished rips go
	RipMode        RipMode   // defaults | ask
	TitleMode      TitleMode // main_feature
	Eject          bool      // eject after each rip
	MaxFillPercent int       // refuse to rip if the output disk would be fuller than this
	DriveMode      DriveMode // single | sequential | parallel
	MaxConcurrent  int       // parallel mode only; 0 = one per drive
	SetupComplete  bool
}

// Default returns the configuration used before setup has run.
func Default() Config {
	return Config{
		RipMode:        RipDefaults,
		TitleMode:      TitleMainFeature,
		Eject:          true,
		MaxFillPercent: 80,
		DriveMode:      DriveSingle,
	}
}

// Setting keys in the settings table.
const (
	KeyMakeMKVCon     = "makemkvcon_path"
	KeyOutputDir      = "output_dir"
	KeyRipMode        = "rip_mode"
	KeyTitleMode      = "title_mode"
	KeyEject          = "eject"
	KeyMaxFillPercent = "max_fill_percent"
	KeyDriveMode      = "drive_mode"
	KeyMaxConcurrent  = "max_concurrent"
	KeySetupComplete  = "setup_complete"
)

// Validate reports the first invalid setting.
func (c Config) Validate() error {
	switch c.RipMode {
	case RipDefaults, RipAsk:
	default:
		return fmt.Errorf("%s: must be %q or %q, got %q", KeyRipMode, RipDefaults, RipAsk, c.RipMode)
	}
	if c.TitleMode != TitleMainFeature {
		return fmt.Errorf("%s: must be %q, got %q", KeyTitleMode, TitleMainFeature, c.TitleMode)
	}
	switch c.DriveMode {
	case DriveSingle, DriveSequential, DriveParallel:
	default:
		return fmt.Errorf("%s: must be single, sequential or parallel, got %q", KeyDriveMode, c.DriveMode)
	}
	if c.MaxFillPercent < 1 || c.MaxFillPercent > 100 {
		return fmt.Errorf("%s: must be between 1 and 100, got %d", KeyMaxFillPercent, c.MaxFillPercent)
	}
	if c.MaxConcurrent < 0 {
		return fmt.Errorf("%s: must be 0 or more, got %d", KeyMaxConcurrent, c.MaxConcurrent)
	}
	return nil
}

// ToMap converts the config to settings-table key/values.
func (c Config) ToMap() map[string]string {
	return map[string]string{
		KeyMakeMKVCon:     c.MakeMKVCon,
		KeyOutputDir:      c.OutputDir,
		KeyRipMode:        string(c.RipMode),
		KeyTitleMode:      string(c.TitleMode),
		KeyEject:          strconv.FormatBool(c.Eject),
		KeyMaxFillPercent: strconv.Itoa(c.MaxFillPercent),
		KeyDriveMode:      string(c.DriveMode),
		KeyMaxConcurrent:  strconv.Itoa(c.MaxConcurrent),
		KeySetupComplete:  strconv.FormatBool(c.SetupComplete),
	}
}

// FromMap builds a config from settings-table key/values, starting from the
// defaults. Unknown keys are ignored; malformed values are errors.
func FromMap(m map[string]string) (Config, error) {
	c := Default()
	var err error
	setBool := func(key string, dst *bool) {
		if v, ok := m[key]; ok && err == nil {
			if *dst, err = strconv.ParseBool(v); err != nil {
				err = fmt.Errorf("%s: %q is not true/false", key, v)
			}
		}
	}
	setInt := func(key string, dst *int) {
		if v, ok := m[key]; ok && err == nil {
			if *dst, err = strconv.Atoi(v); err != nil {
				err = fmt.Errorf("%s: %q is not a number", key, v)
			}
		}
	}
	if v, ok := m[KeyMakeMKVCon]; ok {
		c.MakeMKVCon = v
	}
	if v, ok := m[KeyOutputDir]; ok {
		c.OutputDir = v
	}
	if v, ok := m[KeyRipMode]; ok {
		c.RipMode = RipMode(v)
	}
	if v, ok := m[KeyTitleMode]; ok {
		c.TitleMode = TitleMode(v)
	}
	if v, ok := m[KeyDriveMode]; ok {
		c.DriveMode = DriveMode(v)
	}
	setBool(KeyEject, &c.Eject)
	setBool(KeySetupComplete, &c.SetupComplete)
	setInt(KeyMaxFillPercent, &c.MaxFillPercent)
	setInt(KeyMaxConcurrent, &c.MaxConcurrent)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}

// Keys returns all setting keys in display order.
func Keys() []string {
	keys := make([]string, 0, len(Default().ToMap()))
	for k := range Default().ToMap() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Load reads the config from the store.
func Load(ctx context.Context, s *store.Store) (Config, error) {
	m, err := s.Settings(ctx)
	if err != nil {
		return Config{}, err
	}
	c, err := FromMap(m)
	if err != nil {
		return c, fmt.Errorf("invalid setting in %s: %w", s.Path(), err)
	}
	return c, nil
}

// Save validates and writes the config to the store.
func Save(ctx context.Context, s *store.Store, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return s.SetSettings(ctx, c.ToMap())
}
