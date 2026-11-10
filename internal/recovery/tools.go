// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package recovery

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/juju/juju/core/semversion"
	domainrecovery "github.com/juju/juju/domain/recovery"
	"github.com/juju/juju/internal/errors"
	coretools "github.com/juju/juju/internal/tools"
)

// ArchiveTools owns the temporary bundle of the exact archived agent.
type ArchiveTools struct {
	Tools *coretools.Tools
	dir   string
}

func (t *ArchiveTools) Close() error { return os.RemoveAll(t.dir) }

// SelectAgent never orders versions or falls back to local binaries. Only
// regular entries in the recorded version directory can supply agent bytes.
// Multiple platforms require an unambiguous archived machine-tools link.
func SelectAgent(ctx context.Context, archivePath string, info *domainrecovery.ArchiveInfo) (_ *ArchiveTools, err error) {
	if info == nil || info.Checksum == "" {
		return nil, errors.New("validated archive information is required")
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, errors.Capture(err)
	}
	defer f.Close()
	hash := sha256.New()
	input := io.TeeReader(contextReader{ctx: ctx, reader: f}, hash)
	gz, err := gzip.NewReader(input)
	if err != nil {
		return nil, errors.Capture(err)
	}
	defer gz.Close()
	outer := tar.NewReader(gz)
	var bundle *ArchiveTools
	for {
		h, err := outer.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("archive has no tools bundle")
		}
		if err != nil {
			return nil, errors.Capture(err)
		}
		if path.Clean(h.Name) != canonicalPaths.FilesArchive {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return nil, errors.New("archive tools bundle is not a regular file")
		}
		bundle, err = selectBundledAgent(ctx, tar.NewReader(outer), info)
		if err != nil {
			return nil, err
		}
		break
	}
	defer func() {
		if err != nil {
			bundle.Close()
		}
	}()
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return nil, errors.Capture(err)
	}
	if _, err := io.Copy(io.Discard, input); err != nil {
		return nil, errors.Capture(err)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), strings.TrimPrefix(info.Checksum, "sha256-")) {
		return nil, errors.New("archive checksum changed while selecting agent")
	}
	return bundle, nil
}

func selectBundledAgent(ctx context.Context, reader *tar.Reader, info *domainrecovery.ArchiveInfo) (_ *ArchiveTools, err error) {
	dir, err := os.MkdirTemp("", "juju-recovery-agent-")
	if err != nil {
		return nil, errors.Capture(err)
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	candidates := make(map[string]semversion.Binary)
	selected := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Capture(err)
		}
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.Capture(err)
		}
		if path.IsAbs(h.Name) || strings.Contains(h.Name, "\\") || containsParent(h.Name) {
			return nil, errors.New("unsafe path in archived tools bundle")
		}
		parts := strings.Split(path.Clean(h.Name), "/")
		if h.Typeflag == tar.TypeSymlink && len(parts) > 1 && parts[len(parts)-2] == "tools" && parts[len(parts)-1] == "machine-"+info.MachineName {
			v, err := semversion.ParseBinary(path.Base(h.Linkname))
			if err == nil && v.Number == info.AgentVersion {
				selected = v.String()
			}
		}
		if len(parts) < 3 || parts[len(parts)-3] != "tools" {
			continue
		}
		v, err := semversion.ParseBinary(parts[len(parts)-2])
		if err != nil || v.Number != info.AgentVersion {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			continue
		}
		name := parts[len(parts)-1]
		switch name {
		case "jujuagentd", "jujuc", "jujud", "FORCE-VERSION", "jujuagentd-versions.yaml", "juju-versions.yaml", "downloaded-tools.txt":
		default:
			continue
		}
		candidate := filepath.Join(dir, v.String())
		if err := os.MkdirAll(candidate, 0700); err != nil {
			return nil, errors.Capture(err)
		}
		out, err := os.OpenFile(filepath.Join(candidate, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0777)
		if err != nil {
			return nil, errors.Errorf("conflicting archived agent entry: %w", err)
		}
		_, copyErr := io.Copy(out, contextReader{ctx: ctx, reader: reader})
		closeErr := out.Close()
		if copyErr != nil {
			return nil, errors.Capture(copyErr)
		}
		if closeErr != nil {
			return nil, errors.Capture(closeErr)
		}
		if name == "jujuagentd" {
			if h.Mode&0111 == 0 {
				return nil, errors.New("archived agent is not executable")
			}
			candidates[v.String()] = v
		}
	}
	if selected == "" && len(candidates) == 1 {
		for name := range candidates {
			selected = name
		}
	}
	v, ok := candidates[selected]
	if !ok {
		return nil, errors.Errorf("archive has no unambiguous agent for version %s", info.AgentVersion)
	}
	force, err := os.ReadFile(filepath.Join(dir, selected, "FORCE-VERSION"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errors.Capture(err)
	}
	if err == nil {
		forced, err := semversion.Parse(strings.TrimSpace(string(force)))
		if err != nil {
			return nil, errors.New("invalid archived FORCE-VERSION")
		}
		// An omitted build takes the build embedded in the archived binary.
		// The target additionally checks its full running version on import.
		if forced.Build == 0 {
			forced.Build = info.AgentVersion.Build
		}
		if forced != info.AgentVersion {
			return nil, errors.New("archived FORCE-VERSION disagrees with metadata")
		}
	}
	filename := filepath.Join(dir, "agent.tgz")
	out, err := os.Create(filename)
	if err != nil {
		return nil, errors.Capture(err)
	}
	hash := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(out, hash))
	tw := tar.NewWriter(gz)
	entries, err := os.ReadDir(filepath.Join(dir, selected))
	if err != nil {
		out.Close()
		return nil, errors.Capture(err)
	}
	for _, ent := range entries {
		if ent.Name() == "downloaded-tools.txt" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, selected, ent.Name()))
		if err != nil {
			out.Close()
			return nil, errors.Capture(err)
		}
		fi, err := ent.Info()
		if err != nil {
			out.Close()
			return nil, errors.Capture(err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: ent.Name(), Size: int64(len(data)), Mode: int64(fi.Mode().Perm()), Typeflag: tar.TypeReg}); err != nil {
			out.Close()
			return nil, errors.Capture(err)
		}
		if _, err := tw.Write(data); err != nil {
			out.Close()
			return nil, errors.Capture(err)
		}
	}
	if err := tw.Close(); err != nil {
		out.Close()
		return nil, errors.Capture(err)
	}
	if err := gz.Close(); err != nil {
		out.Close()
		return nil, errors.Capture(err)
	}
	if err := out.Close(); err != nil {
		return nil, errors.Capture(err)
	}
	fi, err := os.Stat(filename)
	if err != nil {
		return nil, errors.Capture(err)
	}
	return &ArchiveTools{dir: dir, Tools: &coretools.Tools{Version: v, URL: "file://" + filename, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: fi.Size()}}, nil
}

func containsParent(name string) bool {
	return slices.Contains(strings.Split(name, "/"), "..")
}
