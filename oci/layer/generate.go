/*
 * umoci: Umoci Modifies Open Containers' Images
 * Copyright (C) 2016 SUSE LLC.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package layer

import (
	"io"
	"path/filepath"
	"sort"

	"github.com/Sirupsen/logrus"
	"github.com/pkg/errors"
	"github.com/vbatts/go-mtree"
)

// NOTE: This currently requires a version of go-mtree which has my Compare()
//       PR added. While we don't use this interface here, my work also
//       implemented the InodeDelta and supporting interfaces. Hopefully my PR
//       will be merged soon. https://github.com/vbatts/go-mtree/pull/48

// inodeDeltas is a wrapper around []mtree.InodeDelta that allows for sorting
// the set of deltas by the pathname.
type inodeDeltas []mtree.InodeDelta

func (ids inodeDeltas) Len() int           { return len(ids) }
func (ids inodeDeltas) Less(i, j int) bool { return ids[i].Path() < ids[j].Path() }
func (ids inodeDeltas) Swap(i, j int)      { ids[i], ids[j] = ids[j], ids[i] }

// GenerateLayer creates a new OCI diff layer based on the mtree diff provided.
// All of the mtree.Modified and mtree.Extra blobs are read relative to the
// provided path (which should be the rootfs of the layer that was diffed). The
// returned reader is for the *raw* tar data, it is the caller's responsibility
// to gzip it.
func GenerateLayer(path string, deltas []mtree.InodeDelta, opt *MapOptions) (io.ReadCloser, error) {
	var mapOptions MapOptions
	if opt != nil {
		mapOptions = *opt
	}

	reader, writer := io.Pipe()

	go func() (Err error) {
		// Close with the returned error.
		defer func() {
			writer.CloseWithError(errors.Wrap(Err, "generate layer"))
		}()

		// We can't just dump all of the file contents into a tar file. We need
		// to emulate a proper tar generator. Luckily there aren't that many
		// things to emulate (and we can do them all in tar.go).
		tg := newTarGenerator(writer, mapOptions)

		// Sort the delta paths.
		// FIXME: We need to add whiteouts first, otherwise we might end up
		//        doing something silly like deleting a file which we actually
		//        meant to modify.
		sort.Sort(inodeDeltas(deltas))

		for _, delta := range deltas {
			name := delta.Path()
			fullPath := filepath.Join(path, name)

			// XXX: It's possible that if we unlink a hardlink, we're going to
			//      AddFile() for no reason. Maybe we should drop nlink= from
			//      the set of keywords we care about?

			switch delta.Type() {
			case mtree.Modified, mtree.Extra:
				if err := tg.AddFile(name, fullPath); err != nil {
					logrus.Warnf("generate layer: could not add file '%s': %s", name, err)
					return errors.Wrap(err, "generate layer file")
				}
			case mtree.Missing:
				if err := tg.AddWhiteout(name); err != nil {
					logrus.Warnf("generate layer: could not add whiteout '%s': %s", name, err)
					return errors.Wrap(err, "generate whiteout layer file")
				}
			}
		}

		if err := tg.tw.Close(); err != nil {
			logrus.Warnf("generate layer: could not close tar.Writer: %s", err)
			return errors.Wrap(err, "close tar writer")
		}

		return nil
	}()

	return reader, nil
}