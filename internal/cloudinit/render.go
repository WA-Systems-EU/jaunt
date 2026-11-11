// Package cloudinit renders the cloud-config user-data for a jauntbox server.
package cloudinit

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"text/template"

	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/provision"
)

//go:embed user-data.yaml.tmpl
var tmplSrc string

//go:embed units
var unitsFS embed.FS

// Params are the inputs to Render.
type Params struct {
	Box              boxinfo.BoxInfo
	TailscaleAuthKey string
	HetznerToken     string
	VolumeDevice     string
	Tag              string
}

type file struct{ Path, Perm, B64 string }

// mk gzips then base64-encodes data: Hetzner caps user_data at 32 KiB and the
// provisioning scripts alone are close to that in plain base64.
func mk(path, perm string, data []byte) file {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(data)
	zw.Close()
	return file{path, perm, base64.StdEncoding.EncodeToString(buf.Bytes())}
}

// Render returns the #cloud-config document. All file contents are gzip+base64 so
// secrets never appear in runcmd and need no YAML quoting.
func Render(p Params) (string, error) {
	boxJSON, err := json.MarshalIndent(p.Box, "", "  ")
	if err != nil {
		return "", err
	}
	files := []file{
		mk(boxinfo.Path, "0644", append(boxJSON, '\n')),
		mk("/etc/jauntbox/hcloud.token", "0600", []byte(p.HetznerToken)),
		mk("/etc/jauntbox/ts.key", "0600", []byte(p.TailscaleAuthKey)),
	}
	for _, u := range []string{"jauntbox-supervise.service", "jauntbox-supervise.timer", "jauntbox-safety.service", "jauntbox-safety.timer", "jauntbox-metadata-guard.service", "jauntbox-agent-net.service"} {
		b, err := unitsFS.ReadFile("units/" + u)
		if err != nil {
			return "", err
		}
		files = append(files, mk("/etc/systemd/system/"+u, "0644", b))
	}
	sn, err := unitsFS.ReadFile("units/safety-net.sh")
	if err != nil {
		return "", err
	}
	files = append(files, mk("/opt/jauntbox/safety-net.sh", "0755", sn))
	for _, s := range []string{"dev-setup.sh", "dev-setup-user.sh", "jaunt-repos.sh"} {
		b, err := provision.FS.ReadFile(s)
		if err != nil {
			return "", err
		}
		files = append(files, mk("/opt/jaunt/"+s, "0755", b))
	}
	t, err := template.New("user-data").Parse(tmplSrc)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	data := struct {
		Params
		Files []file
	}{p, files}
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render user-data: %w", err)
	}
	return buf.String(), nil
}
