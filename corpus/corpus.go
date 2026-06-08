// Package corpus prepares and tracks the benchmark input: for each image it
// pulls the OCI content and decompresses every layer into a raw tar on disk.
// Prep is idempotent — an existing corpus/<image>/ is reused — and pins the
// platform so the raw bytes are identical on every machine (so cross-arch
// determinism compares like with like).
//
// All filesystem access goes through an os.Root anchored at the corpus dir, so a
// crafted manifest (e.g. a layer "file" with ".." or a symlink) cannot read or
// write outside the corpus tree.
package corpus

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/pkg/errors"
)

// DefaultPlatform is pinned so every machine prepares byte-identical raw layers.
const DefaultPlatform = "linux/amd64"

const manifestName = "manifest.json"

// Layer is one decompressed image layer on disk.
type Layer struct {
	Digest  string `json:"digest"`  // diffID (digest of the uncompressed tar)
	File    string `json:"file"`    // path relative to the image dir
	RawSize int64  `json:"rawSize"` // uncompressed size in bytes
}

// Image is a prepared image: its ref, platform, and decompressed layers.
type Image struct {
	Ref      string  `json:"ref"`
	Platform string  `json:"platform"`
	Layers   []Layer `json:"layers"`

	root *os.Root // rooted at the corpus dir; nil after Close
	rel  string   // image dir relative to the corpus root (sanitized ref)
}

// OpenLayer opens a layer's decompressed tar, confined to the corpus root.
func (img *Image) OpenLayer(l Layer) (*os.File, error) {
	f, err := img.root.Open(filepath.Join(img.rel, l.File))
	return f, errors.WithStack(err)
}

// Close releases the corpus directory handle.
func (img *Image) Close() error {
	if img.root == nil {
		return nil
	}
	err := img.root.Close()
	img.root = nil
	return errors.WithStack(err)
}

// Ensure returns the prepared image, prepping it (pull + decompress) if its
// corpus dir does not already exist. With force, it re-pulls even if present.
func Ensure(corpusDir, ref string, force bool) (*Image, error) {
	return EnsurePlatform(corpusDir, ref, DefaultPlatform, force)
}

// EnsurePlatform is Ensure with an explicit platform.
func EnsurePlatform(corpusDir, ref, platform string, force bool) (*Image, error) {
	if err := os.MkdirAll(corpusDir, 0o755); err != nil {
		return nil, errors.WithStack(err)
	}
	root, err := os.OpenRoot(corpusDir)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	rel := sanitize(ref)
	if force {
		if err := root.RemoveAll(rel); err != nil {
			root.Close()
			return nil, errors.WithStack(err)
		}
	}
	if img, err := load(root, rel); err == nil {
		return img, nil
	} else if !os.IsNotExist(errors.Cause(err)) {
		root.Close()
		return nil, err
	}
	img, err := prep(root, rel, ref, platform)
	if err != nil {
		root.Close()
		return nil, err
	}
	return img, nil
}

// load reads an existing prepared image and verifies its layer files are present.
func load(root *os.Root, rel string) (*Image, error) {
	data, err := root.ReadFile(filepath.Join(rel, manifestName))
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var img Image
	if err := json.Unmarshal(data, &img); err != nil {
		return nil, errors.Wrapf(err, "parse manifest in %s", rel)
	}
	img.root = root
	img.rel = rel
	for _, l := range img.Layers {
		if _, err := root.Stat(filepath.Join(rel, l.File)); err != nil {
			return nil, errors.Wrapf(err, "corpus %s incomplete; remove it and re-prep", rel)
		}
	}
	return &img, nil
}

// prep pulls ref at platform and decompresses its layers into the corpus. It
// builds in a sibling .tmp dir and renames into place so a crash never leaves a
// partial corpus that load() would accept.
func prep(root *os.Root, rel, ref, platform string) (*Image, error) {
	plat, err := v1.ParsePlatform(platform)
	if err != nil {
		return nil, errors.Wrapf(err, "platform %q", platform)
	}
	rmImg, err := crane.Pull(ref, crane.WithPlatform(plat))
	if err != nil {
		return nil, errors.Wrapf(err, "pull %s", ref)
	}
	layers, err := rmImg.Layers()
	if err != nil {
		return nil, errors.Wrapf(err, "read layers of %s", ref)
	}

	tmp := rel + ".tmp"
	if err := root.RemoveAll(tmp); err != nil {
		return nil, errors.WithStack(err)
	}
	if err := root.MkdirAll(filepath.Join(tmp, "layers"), 0o755); err != nil {
		return nil, errors.WithStack(err)
	}

	img := &Image{Ref: ref, Platform: platform, root: root, rel: rel}
	for _, l := range layers {
		diffID, err := l.DiffID()
		if err != nil {
			return nil, errors.Wrapf(err, "diffID of layer in %s", ref)
		}
		fileRel := filepath.Join("layers", "sha256_"+diffID.Hex+".tar")
		size, err := writeUncompressed(root, l, filepath.Join(tmp, fileRel))
		if err != nil {
			return nil, err
		}
		img.Layers = append(img.Layers, Layer{Digest: diffID.String(), File: fileRel, RawSize: size})
	}

	data, err := json.MarshalIndent(img, "", "  ")
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if err := root.WriteFile(filepath.Join(tmp, manifestName), data, 0o644); err != nil {
		return nil, errors.WithStack(err)
	}

	if err := root.RemoveAll(rel); err != nil {
		return nil, errors.WithStack(err)
	}
	if err := root.Rename(tmp, rel); err != nil {
		return nil, errors.Wrapf(err, "finalize corpus %s", rel)
	}
	return img, nil
}

func writeUncompressed(root *os.Root, l v1.Layer, relpath string) (int64, error) {
	rc, err := l.Uncompressed()
	if err != nil {
		return 0, errors.WithStack(err)
	}
	defer rc.Close()
	f, err := root.Create(relpath)
	if err != nil {
		return 0, errors.WithStack(err)
	}
	defer f.Close()
	n, err := io.Copy(f, rc)
	if err != nil {
		return 0, errors.Wrapf(err, "decompress layer to %s", relpath)
	}
	return n, errors.WithStack(f.Close())
}

// sanitize turns an image ref into a filesystem-safe directory name.
func sanitize(ref string) string {
	r := strings.NewReplacer("/", "_", ":", "_", "@", "_")
	return r.Replace(ref)
}
