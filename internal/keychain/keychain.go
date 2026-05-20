// Package keychain stores and retrieves CLI credentials in the macOS system
// Keychain. Each named profile (e.g. a workspace, environment, or account)
// gets one generic-password item:
//
//	service  = <serviceName constant — set to your CLI name>
//	account  = profile name (e.g. "prod", "staging", "myworkspace.example.com")
//	password = JSON-encoded Entry
//
// A second item tracks the set of known profiles:
//
//	service  = <serviceName>
//	account  = "__index__"
//	password = JSON-encoded []string of profile names
//
// The macOS `security` CLI is used directly — no external Go dependencies.
package keychain

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// serviceName is the Keychain service label for all items this CLI stores.
// Change this to your CLI's name before shipping.
const serviceName = "YOUR_CLI"

const indexAccount = "__index__"

// Entry is the credential record stored for one profile.
// Extend this struct with whatever credentials your CLI needs.
type Entry struct {
	Profile string    `json:"profile"`
	Token   string    `json:"token"`
	SavedAt time.Time `json:"saved_at"`
}

// ErrNotFound is returned when no credential exists for the requested profile.
var ErrNotFound = errors.New("no credentials found for profile")

// Save writes (or overwrites) the credential entry for a profile and registers
// it in the profile index. profile must not be empty.
func Save(e Entry) error {
	if e.Profile == "" {
		return errors.New("keychain.Save: profile must not be empty")
	}
	if e.Token == "" {
		return errors.New("keychain.Save: token must not be empty")
	}
	if e.SavedAt.IsZero() {
		e.SavedAt = time.Now().UTC()
	}

	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("keychain.Save: marshal: %w", err)
	}

	// -U: update if already exists (idempotent).
	out, err := run("security", "add-generic-password",
		"-s", serviceName,
		"-a", e.Profile,
		"-w", string(payload),
		"-U",
	)
	if err != nil {
		return fmt.Errorf("keychain.Save: %w: %s", err, out)
	}

	if err := indexAdd(e.Profile); err != nil {
		return fmt.Errorf("keychain.Save: updating index: %w", err)
	}
	return nil
}

// Load retrieves the credential entry for the given profile.
// Returns ErrNotFound if no item exists.
func Load(profile string) (Entry, error) {
	if profile == "" {
		return Entry{}, errors.New("keychain.Load: profile must not be empty")
	}
	out, err := run("security", "find-generic-password",
		"-s", serviceName,
		"-a", profile,
		"-w",
	)
	if err != nil {
		if isNotFound(out) {
			return Entry{}, ErrNotFound
		}
		return Entry{}, fmt.Errorf("keychain.Load: %w: %s", err, out)
	}

	var e Entry
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &e); err != nil {
		return Entry{}, fmt.Errorf("keychain.Load: corrupt entry for %q: %w", profile, err)
	}
	return e, nil
}

// Delete removes the credential entry for the given profile and removes it
// from the profile index. Returns ErrNotFound if no item exists.
func Delete(profile string) error {
	if profile == "" {
		return errors.New("keychain.Delete: profile must not be empty")
	}
	out, err := run("security", "delete-generic-password",
		"-s", serviceName,
		"-a", profile,
	)
	if err != nil {
		if isNotFound(out) {
			return ErrNotFound
		}
		return fmt.Errorf("keychain.Delete: %w: %s", err, out)
	}

	if err := indexRemove(profile); err != nil {
		return fmt.Errorf("keychain.Delete: updating index: %w", err)
	}
	return nil
}

// List returns all profile entries saved under the service. Profiles whose
// keychain item is missing or corrupt are returned in the second slice so
// callers can surface the issue (e.g. ask the user to re-authenticate).
func List() (entries []Entry, corrupt []string, err error) {
	profiles, err := indexLoad()
	if err != nil {
		return nil, nil, fmt.Errorf("keychain.List: reading index: %w", err)
	}

	for _, p := range profiles {
		e, err := Load(p)
		if errors.Is(err, ErrNotFound) {
			corrupt = append(corrupt, p)
			continue
		}
		if err != nil {
			corrupt = append(corrupt, p)
			continue
		}
		entries = append(entries, e)
	}
	return entries, corrupt, nil
}

// ---------------------------------------------------------------------------
// Default profile
// ---------------------------------------------------------------------------

const defaultAccount = "__default__"

// SetDefault stores profile as the default profile name.
// Overwrites any previously stored default.
func SetDefault(profile string) error {
	if profile == "" {
		return errors.New("keychain.SetDefault: profile must not be empty")
	}
	out, err := run("security", "add-generic-password",
		"-s", serviceName,
		"-a", defaultAccount,
		"-w", profile,
		"-U",
	)
	if err != nil {
		return fmt.Errorf("keychain.SetDefault: %w: %s", err, out)
	}
	return nil
}

// DeleteDefault removes the stored default profile. No-op if none is set.
func DeleteDefault() error {
	out, err := run("security", "delete-generic-password",
		"-s", serviceName,
		"-a", defaultAccount,
	)
	if err != nil {
		if isNotFound(out) {
			return nil
		}
		return fmt.Errorf("keychain.DeleteDefault: %w: %s", err, out)
	}
	return nil
}

// GetDefault returns the stored default profile name.
// Returns ErrNotFound if no default has been set.
func GetDefault() (string, error) {
	out, err := run("security", "find-generic-password",
		"-s", serviceName,
		"-a", defaultAccount,
		"-w",
	)
	if err != nil {
		if isNotFound(out) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("keychain.GetDefault: %w: %s", err, out)
	}
	return strings.TrimSpace(out), nil
}

// ResolveDefault returns a profile name using the following resolution order:
//  1. Stored default (GetDefault).
//  2. If exactly one profile is saved, return it implicitly.
//  3. Otherwise error — ambiguous or empty.
//
// Callers that accept an explicit --profile flag (or equivalent) should check
// it first and only call ResolveDefault when the flag was not provided.
func ResolveDefault() (string, error) {
	// 1. Stored default.
	p, err := GetDefault()
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}

	// 2. Single saved profile.
	entries, _, listErr := List()
	if listErr != nil {
		return "", fmt.Errorf("listing saved profiles: %w", listErr)
	}
	switch len(entries) {
	case 0:
		return "", fmt.Errorf("no saved profiles; run: YOUR_CLI auth login")
	case 1:
		return entries[0].Profile, nil
	default:
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Profile
		}
		return "", fmt.Errorf(
			"multiple profiles saved (%s); set a default with: YOUR_CLI auth default --profile <name>",
			strings.Join(names, ", "),
		)
	}
}

// ---------------------------------------------------------------------------
// Index helpers
// ---------------------------------------------------------------------------

func indexLoad() ([]string, error) {
	out, err := run("security", "find-generic-password",
		"-s", serviceName,
		"-a", indexAccount,
		"-w",
	)
	if err != nil {
		if isNotFound(out) {
			return nil, nil
		}
		return nil, fmt.Errorf("indexLoad: %w: %s", err, out)
	}

	var profiles []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &profiles); err != nil {
		// Corrupt index — return empty rather than failing List entirely.
		return nil, nil
	}
	return profiles, nil
}

func indexSave(profiles []string) error {
	payload, err := json.Marshal(profiles)
	if err != nil {
		return fmt.Errorf("indexSave: marshal: %w", err)
	}
	out, err := run("security", "add-generic-password",
		"-s", serviceName,
		"-a", indexAccount,
		"-w", string(payload),
		"-U",
	)
	if err != nil {
		return fmt.Errorf("indexSave: %w: %s", err, out)
	}
	return nil
}

func indexAdd(profile string) error {
	profiles, err := indexLoad()
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if p == profile {
			return nil
		}
	}
	return indexSave(append(profiles, profile))
}

func indexRemove(profile string) error {
	profiles, err := indexLoad()
	if err != nil {
		return err
	}
	filtered := profiles[:0]
	for _, p := range profiles {
		if p != profile {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == len(profiles) {
		return nil
	}
	return indexSave(filtered)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func isNotFound(out string) bool {
	return strings.Contains(out, "could not be found") ||
		strings.Contains(out, "The specified item could not be found")
}

// run executes a command and returns combined stdout+stderr and any error.
func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}
