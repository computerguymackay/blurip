// Package config defines blurip's settings, their defaults and validation,
// and maps them to the key/value settings table.
package config

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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
	SplitByType    bool      // DVDs go to OutputDir/DVD, Blu-rays to OutputDir/Blu-ray
	RipMode        RipMode   // defaults | ask
	TitleMode      TitleMode // main_feature
	Eject          bool      // eject after each rip
	MaxFillPercent int       // refuse to rip if the output disk would be fuller than this
	ReadTimeout    int       // minutes to wait for MakeMKV to read a disc; 0 = no limit
	DriveMode      DriveMode // single | sequential | parallel
	MaxConcurrent  int       // parallel mode only; 0 = one per drive
	SingleDrive    string    // single mode with several drives: the device to use ("" = first)
	SetupComplete  bool

	// HandBrake encoding (blurip -Q, blurip encode).
	HandBrakeCLI      string // path or command line of HandBrakeCLI
	PresetDVD         string // HandBrake preset for DVD rips, "General/Fast 1080p30"
	PresetBluRay      string // HandBrake preset for Blu-ray rips
	MoviesDir         string // where encoded films go
	ShowsDir          string // where encoded TV box sets go
	DeleteAfterEncode bool   // delete the rip once its encode has been checked
}

// EncodeReady reports whether encoding has been set up.
func (c Config) EncodeReady() bool {
	return c.HandBrakeCLI != "" && c.PresetDVD != "" && c.PresetBluRay != "" && c.MoviesDir != "" && c.ShowsDir != ""
}

// Preset returns the HandBrake preset for a disc of the given kind ("DVD",
// "Blu-ray" or "" if unknown, which uses the DVD preset).
func (c Config) Preset(kind string) string {
	if kind == "Blu-ray" {
		return c.PresetBluRay
	}
	return c.PresetDVD
}

// Default returns the configuration used before setup has run.
func Default() Config {
	return Config{
		RipMode:        RipDefaults,
		TitleMode:      TitleMainFeature,
		Eject:          true,
		MaxFillPercent: 80,
		ReadTimeout:    DefaultReadTimeout,
		DriveMode:      DriveSingle,
	}
}

// DefaultReadTimeout is how many minutes blurip waits for MakeMKV to read a
// disc. Some discs take MakeMKV several minutes (one took nearly 7).
const DefaultReadTimeout = 20

// Subfolders of OutputDir used when SplitByType is set.
const (
	DVDFolder    = "DVD"
	BluRayFolder = "Blu-ray"
)

// DestDir returns the folder that rips of a disc of the given kind ("DVD",
// "Blu-ray", or "" if unknown) go into. Discs of unknown kind go into
// OutputDir itself.
func (c Config) DestDir(kind string) string {
	if c.SplitByType {
		switch kind {
		case "DVD":
			return filepath.Join(c.OutputDir, DVDFolder)
		case "Blu-ray":
			return filepath.Join(c.OutputDir, BluRayFolder)
		}
	}
	return c.OutputDir
}

// Setting keys in the settings table.
const (
	KeyMakeMKVCon     = "makemkvcon_path"
	KeyOutputDir      = "output_dir"
	KeySplitByType    = "split_by_type"
	KeyRipMode        = "rip_mode"
	KeyTitleMode      = "title_mode"
	KeyEject          = "eject"
	KeyMaxFillPercent = "max_fill_percent"
	KeyReadTimeout    = "read_timeout_minutes"
	KeyDriveMode      = "drive_mode"
	KeyMaxConcurrent  = "max_concurrent"
	KeySingleDrive    = "single_drive"
	KeySetupComplete  = "setup_complete"

	KeyHandBrakeCLI      = "handbrake_path"
	KeyPresetDVD         = "preset_dvd"
	KeyPresetBluRay      = "preset_bluray"
	KeyMoviesDir         = "movies_dir"
	KeyShowsDir          = "shows_dir"
	KeyDeleteAfterEncode = "delete_after_encode"
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
	if c.ReadTimeout < 0 {
		return fmt.Errorf("%s: must be 0 (no limit) or more, got %d", KeyReadTimeout, c.ReadTimeout)
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
		KeySplitByType:    strconv.FormatBool(c.SplitByType),
		KeyRipMode:        string(c.RipMode),
		KeyTitleMode:      string(c.TitleMode),
		KeyEject:          strconv.FormatBool(c.Eject),
		KeyMaxFillPercent: strconv.Itoa(c.MaxFillPercent),
		KeyReadTimeout:    strconv.Itoa(c.ReadTimeout),
		KeyDriveMode:      string(c.DriveMode),
		KeyMaxConcurrent:  strconv.Itoa(c.MaxConcurrent),
		KeySingleDrive:    c.SingleDrive,
		KeySetupComplete:  strconv.FormatBool(c.SetupComplete),

		KeyHandBrakeCLI:      c.HandBrakeCLI,
		KeyPresetDVD:         c.PresetDVD,
		KeyPresetBluRay:      c.PresetBluRay,
		KeyMoviesDir:         c.MoviesDir,
		KeyShowsDir:          c.ShowsDir,
		KeyDeleteAfterEncode: strconv.FormatBool(c.DeleteAfterEncode),
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
	if v, ok := m[KeySingleDrive]; ok {
		c.SingleDrive = v
	}
	if v, ok := m[KeyDriveMode]; ok {
		c.DriveMode = DriveMode(v)
	}
	for key, dst := range map[string]*string{
		KeyHandBrakeCLI: &c.HandBrakeCLI, KeyPresetDVD: &c.PresetDVD, KeyPresetBluRay: &c.PresetBluRay,
		KeyMoviesDir: &c.MoviesDir, KeyShowsDir: &c.ShowsDir,
	} {
		if v, ok := m[key]; ok {
			*dst = v
		}
	}
	setBool(KeyDeleteAfterEncode, &c.DeleteAfterEncode)
	setBool(KeyEject, &c.Eject)
	setBool(KeySplitByType, &c.SplitByType)
	setBool(KeySetupComplete, &c.SetupComplete)
	setInt(KeyMaxFillPercent, &c.MaxFillPercent)
	setInt(KeyReadTimeout, &c.ReadTimeout)
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

// Set changes one setting by key, returning the validated result. Booleans
// also accept yes/no and on/off.
func (c Config) Set(key, value string) (Config, error) {
	m := c.ToMap()
	old, ok := m[key]
	if !ok {
		return c, fmt.Errorf("unknown setting %q (settings: %s)", key, strings.Join(Keys(), ", "))
	}
	if old == "true" || old == "false" {
		switch strings.ToLower(value) {
		case "yes", "y", "on":
			value = "true"
		case "no", "n", "off":
			value = "false"
		}
	}
	m[key] = value
	return FromMap(m)
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
