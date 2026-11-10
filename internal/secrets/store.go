// Package secrets is the laptop-side store of master copies of secrets (the
// GitHub App key, agent credentials). Files live in <config dir>/secrets/,
// directory 0700, files 0600; unlock pushes Bundle() to the box's RAM.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	AppKeyFile   = "github-app.pem"
	AppMetaFile  = "github-app.json"
	AgentEnvFile = "agents.env"
)

var (
	// NameRE constrains secret file names (no path separators, no dot-prefix).
	NameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	// EnvKeyRE constrains keys in agents.env.
	EnvKeyRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
)

// Bundle is what unlock sends to the box.
type Bundle struct {
	Files map[string][]byte `json:"files"`
}

// Store is a directory of secret files.
type Store struct{ Dir string }

// DirFor is the secrets directory that sits next to the config file.
func DirFor(cfgPath string) string { return filepath.Join(filepath.Dir(cfgPath), "secrets") }

func checkName(name string) error {
	if !NameRE.MatchString(name) {
		return fmt.Errorf("invalid secret name %q", name)
	}
	return nil
}

// Put writes name atomically with mode 0600, creating the directory 0700.
func (s Store) Put(name string, data []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.Dir, name))
}

// Get reads name; a missing file wraps os.ErrNotExist.
func (s Store) Get(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(s.Dir, name))
}

func parseEnv(b []byte) (keys []string, vals map[string]string) {
	vals = map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok || k == "" {
			continue
		}
		if _, seen := vals[k]; !seen {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	return keys, vals
}

// SetEnv sets key=value in agents.env, replacing an existing line for key.
func (s Store) SetEnv(key, value string) error {
	if !EnvKeyRE.MatchString(key) {
		return fmt.Errorf("invalid env key %q (want %s)", key, EnvKeyRE)
	}
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("value must be a single line")
	}
	b, err := s.Get(AgentEnvFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	keys, vals := parseEnv(b)
	if _, ok := vals[key]; !ok {
		keys = append(keys, key)
	}
	vals[key] = value
	var out strings.Builder
	for _, k := range keys {
		out.WriteString(k + "=" + vals[k] + "\n")
	}
	return s.Put(AgentEnvFile, []byte(out.String()))
}

// EnvKeys lists the keys in agents.env, sorted; never the values.
func (s Store) EnvKeys() ([]string, error) {
	b, err := s.Get(AgentEnvFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keys, _ := parseEnv(b)
	sort.Strings(keys)
	return keys, nil
}

// Bundle reads every secret file. A missing directory gives an empty bundle;
// a file readable by group or others is refused.
func (s Store) Bundle() (Bundle, error) {
	b := Bundle{Files: map[string][]byte{}}
	ents, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	for _, e := range ents {
		if !e.Type().IsRegular() || !NameRE.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(s.Dir, e.Name())
		fi, err := os.Stat(p)
		if err != nil {
			return b, err
		}
		if fi.Mode().Perm()&0o077 != 0 {
			return b, fmt.Errorf("secret file %s is readable by others (mode %o); run chmod 600 %s", p, fi.Mode().Perm(), p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return b, err
		}
		b.Files[e.Name()] = data
	}
	return b, nil
}
