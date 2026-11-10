// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package recovery_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/juju/tc"

	"github.com/juju/juju/core/semversion"
	loggertesting "github.com/juju/juju/internal/logger/testing"
	"github.com/juju/juju/internal/recovery"
)

type toolsSuite struct{}

func TestToolsSuite(t *testing.T) { tc.Run(t, &toolsSuite{}) }

func (s *toolsSuite) TestExactArchivedVersion(c *tc.C) {
	for _, version := range []string{"4.1.0", "4.1.0.7"} {
		files := validFiles()
		files["juju-backup/metadata.json"] = []byte(metadataJSON(version))
		files["juju-backup/root.tar"] = agentRoot(c, []string{"4.0.0-ubuntu-amd64", "4.1.0-ubuntu-amd64", "4.1.0.7-ubuntu-amd64", "4.2.0-ubuntu-amd64"}, "")
		path, sum := writeArchive(c, files)
		info, err := recovery.ValidateArchive(c.Context(), loggertesting.WrapCheckLog(c), path, sum)
		c.Assert(err, tc.ErrorIsNil)
		bundle, err := recovery.SelectAgent(c.Context(), path, info)
		c.Assert(err, tc.ErrorIsNil)
		defer bundle.Close()
		c.Check(bundle.Tools.Version.Number, tc.Equals, semversion.MustParse(version))
		f, err := os.Open(strings.TrimPrefix(bundle.Tools.URL, "file://"))
		c.Assert(err, tc.ErrorIsNil)
		gz, err := gzip.NewReader(f)
		c.Assert(err, tc.ErrorIsNil)
		tr := tar.NewReader(gz)
		h, err := tr.Next()
		c.Assert(err, tc.ErrorIsNil)
		c.Check(h.Name, tc.Equals, "jujuagentd")
		data, err := io.ReadAll(tr)
		c.Assert(err, tc.ErrorIsNil)
		c.Check(string(data), tc.Equals, version+"-ubuntu-amd64")
		c.Assert(gz.Close(), tc.ErrorIsNil)
		c.Assert(f.Close(), tc.ErrorIsNil)
	}
}

func (s *toolsSuite) TestMissingExactVersion(c *tc.C) {
	files := validFiles()
	files["juju-backup/root.tar"] = agentRoot(c, []string{"4.0.0-ubuntu-amd64", "4.2.0-ubuntu-amd64"}, "")
	path, sum := writeArchive(c, files)
	info, err := recovery.ValidateArchive(c.Context(), loggertesting.WrapCheckLog(c), path, sum)
	c.Assert(err, tc.ErrorIsNil)
	_, err = recovery.SelectAgent(c.Context(), path, info)
	c.Check(err, tc.ErrorMatches, "archive has no unambiguous agent for version 4.1.0")
}

func (s *toolsSuite) TestPlatformAmbiguityAndSourceLink(c *tc.C) {
	for _, link := range []string{"", "/var/lib/juju/tools/4.1.0-ubuntu-arm64"} {
		files := validFiles()
		files["juju-backup/root.tar"] = agentRoot(c, []string{"4.1.0-ubuntu-amd64", "4.1.0-ubuntu-arm64"}, link)
		path, sum := writeArchive(c, files)
		info, err := recovery.ValidateArchive(c.Context(), loggertesting.WrapCheckLog(c), path, sum)
		c.Assert(err, tc.ErrorIsNil)
		bundle, err := recovery.SelectAgent(c.Context(), path, info)
		if link == "" {
			c.Check(err, tc.ErrorMatches, "archive has no unambiguous agent.*")
			continue
		}
		c.Assert(err, tc.ErrorIsNil)
		c.Check(bundle.Tools.Version.Arch, tc.Equals, "arm64")
		c.Assert(bundle.Close(), tc.ErrorIsNil)
	}
}

func (s *toolsSuite) TestChecksumChanged(c *tc.C) {
	files := validFiles()
	files["juju-backup/root.tar"] = agentRoot(c, []string{"4.1.0-ubuntu-amd64"}, "")
	path, sum := writeArchive(c, files)
	info, err := recovery.ValidateArchive(c.Context(), loggertesting.WrapCheckLog(c), path, sum)
	c.Assert(err, tc.ErrorIsNil)
	info.Checksum = strings.Repeat("0", 64)
	_, err = recovery.SelectAgent(c.Context(), path, info)
	c.Check(err, tc.ErrorMatches, "archive checksum changed while selecting agent")
}

func (s *toolsSuite) TestForceVersionWithEmbeddedBuild(c *tc.C) {
	for _, forced := range []string{"4.1.0", "4.1.0.7", "4.1.0.8", "4.0.0", "invalid"} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for name, content := range map[string]string{"jujuagentd": "archived agent", "FORCE-VERSION": forced} {
			c.Assert(tw.WriteHeader(&tar.Header{Name: "var/lib/juju/tools/4.1.0.7-ubuntu-amd64/" + name, Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(content))}), tc.ErrorIsNil)
			_, err := tw.Write([]byte(content))
			c.Assert(err, tc.ErrorIsNil)
		}
		c.Assert(tw.Close(), tc.ErrorIsNil)
		files := validFiles()
		files["juju-backup/metadata.json"] = []byte(metadataJSON("4.1.0.7"))
		files["juju-backup/root.tar"] = buf.Bytes()
		path, sum := writeArchive(c, files)
		info, err := recovery.ValidateArchive(c.Context(), loggertesting.WrapCheckLog(c), path, sum)
		c.Assert(err, tc.ErrorIsNil)
		bundle, err := recovery.SelectAgent(c.Context(), path, info)
		if forced != "4.1.0" && forced != "4.1.0.7" {
			c.Check(err, tc.NotNil)
			continue
		}
		c.Assert(err, tc.ErrorIsNil)
		c.Check(bundle.Tools.Version.Number, tc.Equals, semversion.MustParse("4.1.0.7"))
		c.Assert(bundle.Close(), tc.ErrorIsNil)
	}
}

func agentRoot(c *tc.C, versions []string, link string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, version := range versions {
		c.Assert(tw.WriteHeader(&tar.Header{Name: "var/lib/juju/tools/" + version + "/jujuagentd", Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(version))}), tc.ErrorIsNil)
		_, err := tw.Write([]byte(version))
		c.Assert(err, tc.ErrorIsNil)
	}
	if link != "" {
		c.Assert(tw.WriteHeader(&tar.Header{Name: "var/lib/juju/tools/machine-0", Typeflag: tar.TypeSymlink, Linkname: link}), tc.ErrorIsNil)
	}
	c.Assert(tw.Close(), tc.ErrorIsNil)
	return buf.Bytes()
}
